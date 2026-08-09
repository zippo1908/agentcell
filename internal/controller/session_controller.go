package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/access"
	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

const (
	settleFinalizer = "agentcell.io/settle"
	defaultTTL      = int64(3600)
	pollInterval    = 10 * time.Second
)

// SessionReconciler drives dispatch → work → settle → reclaim. Settle is
// mandatory: it runs as a Job before the finalizer is released, so no
// session — finished, killed or deleted — leaves the worktree behind or
// loses produced commits.
type SessionReconciler struct {
	client.Client
	Registry *access.Registry
}

// settleResult is the JSON the settle applet writes to its termination log.
type settleResult struct {
	Produced bool   `json:"produced"`
	Branch   string `json:"branch"`
	Message  string `json:"message"`
}

func (r *SessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var sess acv1.Session
	if err := r.Get(ctx, req.NamespacedName, &sess); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Terminal phases: nothing to do while the CR lives on as a record.
	if sess.DeletionTimestamp.IsZero() && isTerminal(sess.Status.Phase) {
		return ctrl.Result{}, nil
	}

	var cell acv1.Cell
	if err := r.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Spec.Cell}, &cell); err != nil {
		if apierrors.IsNotFound(err) && !sess.DeletionTimestamp.IsZero() {
			// Cell is gone (its namespace deletion swept pods and jobs);
			// nothing left to settle against.
			controllerutil.RemoveFinalizer(&sess, settleFinalizer)
			return ctrl.Result{}, r.Update(ctx, &sess)
		}
		return r.fail(ctx, &sess, fmt.Errorf("cell %q: %w", sess.Spec.Cell, err))
	}
	applyCellDefaults(&cell)
	ns := ids.WorkloadNamespace(cell.Name)

	// Stable session id, derived once.
	if sess.Status.SessionID == "" {
		id := strings.TrimPrefix(sess.Name, "sess-")
		if id == sess.Name {
			id = ids.NewSessionID()
		}
		sess.Status.SessionID = id
		if err := r.Status().Update(ctx, &sess); err != nil {
			return ctrl.Result{}, err
		}
	}
	id := sess.Status.SessionID

	if !sess.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &sess, &cell, ns, id)
	}
	if controllerutil.AddFinalizer(&sess, settleFinalizer) {
		if err := r.Update(ctx, &sess); err != nil {
			return ctrl.Result{}, err
		}
	}

	switch sess.Status.Phase {
	case "", acv1.SessionQueued:
		return r.dispatch(ctx, &sess, &cell, ns, id)
	case acv1.SessionRunning:
		return r.observeRunning(ctx, &sess, &cell, ns, id)
	case acv1.SessionSettling:
		return r.observeSettle(ctx, &sess, &cell, ns, id)
	}
	log.V(1).Info("no-op", "phase", sess.Status.Phase)
	return ctrl.Result{}, nil
}

func isTerminal(p acv1.SessionPhase) bool {
	switch p {
	case acv1.SessionSettled, acv1.SessionDiscarded, acv1.SessionError:
		return true
	}
	return false
}

// dispatch admits the session through the slot gate and creates its pod.
func (r *SessionReconciler) dispatch(ctx context.Context, sess *acv1.Session, cell *acv1.Cell, ns, id string) (ctrl.Result, error) {
	// Validations without side effects come before the slot claim.
	binding, err := r.Registry.Resolve(sess.Spec.Runner, sess.Spec.Provider, sess.Spec.Model)
	if err != nil {
		return r.fail(ctx, sess, err)
	}
	argv, err := access.HeadlessArgv(sess.Spec.Runner, sess.Spec.Task)
	if err != nil {
		return r.fail(ctx, sess, err)
	}

	claimed, err := r.claimSlot(ctx, sess, id)
	if err != nil {
		// Includes optimistic-concurrency conflicts: requeue and retry.
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if !claimed {
		if sess.Status.Phase != acv1.SessionQueued {
			sess.Status.Phase = acv1.SessionQueued
			sess.Status.Message = fmt.Sprintf("all %d slots busy", cell.Spec.MaxSessions)
			if err := r.Status().Update(ctx, sess); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	if err := r.copyCredential(ctx, sess, ns, id); err != nil {
		r.releaseSlot(ctx, sess.Namespace, sess.Spec.Cell, id)
		return r.fail(ctx, sess, fmt.Errorf("credential secret: %w", err))
	}
	if err := r.ensureSessionPod(ctx, sess, cell, ns, id, binding, argv); err != nil {
		r.releaseSlot(ctx, sess.Namespace, sess.Spec.Cell, id)
		return r.fail(ctx, sess, err)
	}

	// Preview follow: watching work-in-progress is a Cell-level switch.
	// Re-read the Cell — claimSlot just bumped its resourceVersion.
	if sess.Spec.FollowPreview {
		var fresh acv1.Cell
		if err := r.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Spec.Cell}, &fresh); err != nil {
			return ctrl.Result{}, err
		}
		if fresh.Spec.Preview.FollowSession != id {
			fresh.Spec.Preview.FollowSession = id
			if err := r.Update(ctx, &fresh); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	now := metav1.Now()
	sess.Status.Phase = acv1.SessionRunning
	sess.Status.PodName = ids.SessionName(id)
	sess.Status.StartTime = &now
	sess.Status.Message = ""
	if err := r.Status().Update(ctx, sess); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: pollInterval}, nil
}

// claimSlot atomically takes a slot lease on the Cell's status. The
// apiserver's resourceVersion check turns concurrent claims into update
// conflicts, so the gate cannot oversell regardless of reconciler
// concurrency or controller replicas. Idempotent for an already-held lease.
func (r *SessionReconciler) claimSlot(ctx context.Context, sess *acv1.Session, id string) (bool, error) {
	var fresh acv1.Cell
	if err := r.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Spec.Cell}, &fresh); err != nil {
		return false, err
	}
	applyCellDefaults(&fresh)
	for _, l := range fresh.Status.SlotLeases {
		if l == id {
			return true, nil
		}
	}
	if int32(len(fresh.Status.SlotLeases)) >= fresh.Spec.MaxSessions {
		return false, nil
	}
	fresh.Status.SlotLeases = append(fresh.Status.SlotLeases, id)
	if err := r.Status().Update(ctx, &fresh); err != nil {
		return false, err // conflict → caller requeues and retries
	}
	return true, nil
}

// releaseSlot returns a lease, retrying briefly on conflicts. Losing a
// release would leak a slot until operator intervention, so this tries
// harder than claim does.
func (r *SessionReconciler) releaseSlot(ctx context.Context, controlNS, cellName, id string) {
	for range 5 {
		var fresh acv1.Cell
		if err := r.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: cellName}, &fresh); err != nil {
			return // cell gone: nothing to release
		}
		kept := fresh.Status.SlotLeases[:0]
		found := false
		for _, l := range fresh.Status.SlotLeases {
			if l == id {
				found = true
				continue
			}
			kept = append(kept, l)
		}
		if !found {
			return
		}
		fresh.Status.SlotLeases = kept
		if err := r.Status().Update(ctx, &fresh); err == nil {
			return
		}
	}
}

func (r *SessionReconciler) copyCredential(ctx context.Context, sess *acv1.Session, ns, id string) error {
	var src corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Spec.CredentialSecret}, &src); err != nil {
		return err
	}
	if _, ok := src.Data["key"]; !ok {
		return fmt.Errorf("secret %q has no %q entry", sess.Spec.CredentialSecret, "key")
	}
	dst := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.SessionSecretName(id)}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, dst, func() error {
		dst.Data = map[string][]byte{"key": src.Data["key"]}
		return nil
	})
	return err
}

func (r *SessionReconciler) ensureSessionPod(ctx context.Context, sess *acv1.Session, cell *acv1.Cell, ns, id string, binding access.Binding, argv []string) error {
	// Credential indirection: EnvAPIKey comes from the per-session secret;
	// protocol variables reference it with $(VAR) substitution so the
	// literal key never appears in the pod spec.
	env := []corev1.EnvVar{
		{Name: runtimeapi.EnvAPIKey, ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: ids.SessionSecretName(id)},
				Key:                  "key",
			},
		}},
		{Name: runtimeapi.EnvSessionID, Value: id},
		{Name: runtimeapi.EnvTask, Value: sess.Spec.Task},
		{Name: runtimeapi.EnvRunner, Value: sess.Spec.Runner},
		{Name: runtimeapi.EnvBaseBranch, Value: cell.Spec.Repo.Branch},
		{Name: runtimeapi.EnvDescription, Value: cell.Spec.Description},
	}
	for k, v := range r.Registry.SessionEnv(binding, "$("+runtimeapi.EnvAPIKey+")") {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}
	argvJSON, err := json.Marshal(argv)
	if err != nil {
		return err
	}
	env = append(env, corev1.EnvVar{Name: "AGENTCELL_AGENT_ARGV", Value: string(argvJSON)})

	cpu, err := resource.ParseQuantity(cell.Spec.SessionResources.CPU)
	if err != nil {
		return fmt.Errorf("sessionResources.cpu: %w", err)
	}
	mem, err := resource.ParseQuantity(cell.Spec.SessionResources.Memory)
	if err != nil {
		return fmt.Errorf("sessionResources.memory: %w", err)
	}

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.SessionName(id)}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, pod, func() error {
		if !pod.CreationTimestamp.IsZero() {
			return nil // pod spec is immutable; observe only
		}
		pod.Labels = map[string]string{
			ids.CellLabelKey:    cell.Name,
			ids.SessionLabelKey: id,
		}
		pod.Spec = corev1.PodSpec{
			RestartPolicy:   corev1.RestartPolicyNever,
			SecurityContext: podSecurity(),
			// RWO PVC: sessions must land on the anchor's node.
			Affinity: &corev1.Affinity{PodAffinity: &corev1.PodAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
					LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
						ids.AnchorPodLabelKey: ids.AnchorPodLabelVal,
					}},
					TopologyKey: "kubernetes.io/hostname",
				}},
			}},
			Containers: []corev1.Container{{
				Name:            "session",
				Image:           cell.Spec.Image,
				Command:         []string{runtimeapi.RuntimeBin, "session"},
				Env:             env,
				SecurityContext: containerSecurity(),
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: mem},
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}},
			}},
			Volumes: []corev1.Volume{{
				Name: "workspace",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: ids.WorkspacePVC},
				},
			}},
		}
		return nil
	})
	return err
}

// observeRunning watches the session pod and enforces the TTL.
func (r *SessionReconciler) observeRunning(ctx context.Context, sess *acv1.Session, cell *acv1.Cell, ns, id string) (ctrl.Result, error) {
	ttl := sess.Spec.TTLSeconds
	if ttl == 0 {
		ttl = defaultTTL
	}
	var pod corev1.Pod
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.SessionName(id)}, &pod)
	switch {
	case apierrors.IsNotFound(err):
		// Pod vanished (evicted, force-deleted): settle what's on disk.
		return r.startSettle(ctx, sess, cell, ns, id, "session pod disappeared")
	case err != nil:
		return ctrl.Result{}, err
	}

	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return r.startSettle(ctx, sess, cell, ns, id, "agent finished ("+string(pod.Status.Phase)+")")
	}
	if sess.Status.StartTime != nil && time.Since(sess.Status.StartTime.Time) > time.Duration(ttl)*time.Second {
		if err := r.Delete(ctx, &pod); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		return r.startSettle(ctx, sess, cell, ns, id, "TTL exceeded")
	}
	return ctrl.Result{RequeueAfter: pollInterval}, nil
}

func (r *SessionReconciler) startSettle(ctx context.Context, sess *acv1.Session, cell *acv1.Cell, ns, id, why string) (ctrl.Result, error) {
	if err := r.ensureSettleJob(ctx, cell, ns, id); err != nil {
		return r.fail(ctx, sess, err)
	}
	sess.Status.Phase = acv1.SessionSettling
	sess.Status.Message = why
	if err := r.Status().Update(ctx, sess); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: pollInterval}, nil
}

func (r *SessionReconciler) ensureSettleJob(ctx context.Context, cell *acv1.Cell, ns, id string) error {
	var envFrom []corev1.EnvFromSource
	if cell.Spec.Repo.SecretName != "" {
		envFrom = append(envFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: ids.GitSecretName}},
		})
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.SettleJobName(id)}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, job, func() error {
		if !job.CreationTimestamp.IsZero() {
			return nil
		}
		backoff := int32(2)
		job.Spec = batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{ids.SessionLabelKey: id}},
				Spec: corev1.PodSpec{
					RestartPolicy:   corev1.RestartPolicyNever,
					SecurityContext: podSecurity(),
					Affinity: &corev1.Affinity{PodAffinity: &corev1.PodAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
							LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
								ids.AnchorPodLabelKey: ids.AnchorPodLabelVal,
							}},
							TopologyKey: "kubernetes.io/hostname",
						}},
					}},
					Containers: []corev1.Container{{
						Name:            "settle",
						Image:           cell.Spec.Image,
						Command:         []string{runtimeapi.RuntimeBin, "settle"},
						SecurityContext: containerSecurity(),
						Env: []corev1.EnvVar{
							{Name: runtimeapi.EnvSessionID, Value: id},
							{Name: runtimeapi.EnvBaseBranch, Value: cell.Spec.Repo.Branch},
						},
						EnvFrom:      envFrom,
						VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}},
					}},
					Volumes: []corev1.Volume{{
						Name: "workspace",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: ids.WorkspacePVC},
						},
					}},
				},
			},
		}
		return nil
	})
	return err
}

// observeSettle waits for the settle job and harvests its result from the
// settle pod's termination message.
func (r *SessionReconciler) observeSettle(ctx context.Context, sess *acv1.Session, cell *acv1.Cell, ns, id string) (ctrl.Result, error) {
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.SettleJobName(id)}, &job); err != nil {
		if apierrors.IsNotFound(err) {
			return r.startSettle(ctx, sess, cell, ns, id, sess.Status.Message)
		}
		return ctrl.Result{}, err
	}
	if job.Status.Succeeded == 0 && job.Status.Failed <= *job.Spec.BackoffLimit {
		return ctrl.Result{RequeueAfter: pollInterval}, nil
	}

	result := settleResult{Message: "settle job failed"}
	ok := job.Status.Succeeded > 0
	if ok {
		if msg, err := r.settleMessage(ctx, ns, id); err == nil && msg != "" {
			_ = json.Unmarshal([]byte(msg), &result)
		}
	}

	// Reclaim: session pod, per-session credential, preview follow.
	_ = r.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.SessionName(id)}})
	_ = r.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.SessionSecretName(id)}})
	if cell.Spec.Preview.FollowSession == id {
		cell.Spec.Preview.FollowSession = ""
		if err := r.Update(ctx, cell); err != nil {
			return ctrl.Result{}, err
		}
	}

	switch {
	case !ok:
		sess.Status.Phase = acv1.SessionError
		sess.Status.Message = result.Message + " — worktree kept on the PVC for manual recovery"
	case result.Produced:
		sess.Status.Phase = acv1.SessionSettled
		sess.Status.Branch = result.Branch
		sess.Status.Produced = true
		sess.Status.Message = result.Message
	default:
		sess.Status.Phase = acv1.SessionDiscarded
		sess.Status.Message = result.Message
	}
	if err := r.Status().Update(ctx, sess); err != nil {
		return ctrl.Result{}, err
	}
	r.releaseSlot(ctx, sess.Namespace, sess.Spec.Cell, id)
	return ctrl.Result{}, nil
}

// settleMessage reads the terminated settle container's message.
func (r *SessionReconciler) settleMessage(ctx context.Context, ns, id string) (string, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{"job-name": ids.SettleJobName(id)}); err != nil {
		return "", err
	}
	for i := range pods.Items {
		for _, cs := range pods.Items[i].Status.ContainerStatuses {
			if cs.State.Terminated != nil && cs.State.Terminated.Message != "" {
				return cs.State.Terminated.Message, nil
			}
		}
	}
	return "", nil
}

// finalize guarantees settle-before-delete.
func (r *SessionReconciler) finalize(ctx context.Context, sess *acv1.Session, cell *acv1.Cell, ns, id string) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(sess, settleFinalizer) {
		return ctrl.Result{}, nil
	}
	if !isTerminal(sess.Status.Phase) {
		// Stop the agent, then settle whatever is on disk.
		err := r.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.SessionName(id)}})
		if err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		if sess.Status.Phase != acv1.SessionSettling {
			return r.startSettle(ctx, sess, cell, ns, id, "session deleted by user")
		}
		return r.observeSettle(ctx, sess, cell, ns, id)
	}
	controllerutil.RemoveFinalizer(sess, settleFinalizer)
	return ctrl.Result{}, r.Update(ctx, sess)
}

func (r *SessionReconciler) fail(ctx context.Context, sess *acv1.Session, err error) (ctrl.Result, error) {
	sess.Status.Phase = acv1.SessionError
	sess.Status.Message = err.Error()
	if serr := r.Status().Update(ctx, sess); serr != nil {
		return ctrl.Result{}, serr
	}
	// Error is terminal: whatever lease this session held must come back.
	if sess.Status.SessionID != "" {
		r.releaseSlot(ctx, sess.Namespace, sess.Spec.Cell, sess.Status.SessionID)
	}
	return ctrl.Result{}, nil // recorded on status; do not hot-loop
}

func (r *SessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&acv1.Session{}).
		Named("session").
		Complete(r)
}
