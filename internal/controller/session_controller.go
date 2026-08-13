package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/access"
	"github.com/zippo1908/agentcell/internal/useruid"
	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

const (
	settleFinalizer = "agentcell.io/settle"
	defaultTTL      = int64(3600)
	// A resident session is a slot someone is using, so the clock that
	// matters is IDLE time, not total age: killing one at the two-hour mark
	// while its agent is mid-run would be the platform interrupting work it
	// was built to host. Two hours of nothing happening is the default, and
	// spec.ttlSeconds overrides it.
	defaultResidentIdle = int64(7200)
	// defaultIdle is how long a resident session sits unused before it stops
	// holding compute. Short, because going dormant costs nothing: the
	// worktree and the CLI's conversation stay on the volume and the
	// terminal comes back where it was.
	defaultIdle = int64(900)
	// defaultDormantTTL bounds how long a session nobody returns to keeps a
	// worktree. It publishes rather than deletes — a week of not looking at
	// something is not consent to throw it away.
	defaultDormantTTL = int64(7 * 24 * 3600)
	pollInterval      = 10 * time.Second
)

// SessionReconciler drives dispatch → work → settle → reclaim. Settle is
// mandatory: it runs as a Job before the finalizer is released, so no
// session — finished, killed or deleted — leaves the worktree behind or
// loses produced commits.
type SessionReconciler struct {
	client.Client
	Registry *access.Registry
	// GitBrokerURL, when set, routes the settle push through the broker so
	// the settle job holds no forge credential (ADR-0005).
	GitBrokerURL string
	// Forge opens/tracks PRs through the broker after review approval
	// (ADR-0006). nil/disabled leaves review purely informational.
	Forge ForgeClient
	// Exec reaches into a user's runtime pod to open and close session
	// windows. nil means resident sessions are unavailable.
	Exec ExecFunc
	// UIDs resolves the Unix identity a Session's pods run as (ADR-0009).
	// nil keeps every workload on the shared project identity, which is the
	// pre-identity behaviour.
	UIDs *useruid.Allocator
}

// ownerUID resolves the Unix identity this Session's pods run as. Without an
// allocator — or without a recorded owner — that is the shared project
// identity, so single-principal deployments are untouched.
func (r *SessionReconciler) ownerUID(ctx context.Context, sess *acv1.Session) (int64, error) {
	if r.UIDs == nil {
		return useruid.ProjectUID, nil
	}
	return r.UIDs.Ensure(ctx, sess.Spec.OwnerUserID)
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

	// Terminal phases: the CR lives on as a record. Release is re-run here
	// idempotently — if the controller crashed between writing the terminal
	// status and releasing the lease, this is where the slot comes back.
	// The review/PR lifecycle (ADR-0006) continues after the work phase.
	if sess.DeletionTimestamp.IsZero() && isTerminal(sess.Status.Phase) {
		if sess.Status.SessionID != "" {
			r.releaseSlot(ctx, sess.Namespace, sess.Spec.Cell, sess.Status.SessionID)
		}
		return r.reviewReconcile(ctx, &sess)
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
	case acv1.SessionDormant:
		return r.observeDormant(ctx, &sess, &cell, ns, id)
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
	// Name the CLI-side conversation once, at first dispatch, so a later
	// "keep going" addresses this one rather than opening a fresh context.
	if sess.Status.RunnerSessionID == "" {
		if sid := access.NewRunnerSession(sess.Spec.Runner); sid != "" {
			sess.Status.RunnerSessionID = sid
			if err := r.Status().Update(ctx, sess); err != nil {
				return ctrl.Result{}, err
			}
		}
	}
	argv, err := access.StartArgvFor(sess.Spec.Runner, sess.Spec.Task, sess.Status.RunnerSessionID)
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

	if sess.IsResident() {
		// A resident session is a window in its owner's runtime, not a pod of
		// its own: the CLIs manage conversations, so one tmux server per user
		// is what the platform actually needs to provide.
		uid, err := r.ownerUID(ctx, sess)
		if err != nil {
			r.releaseSlot(ctx, sess.Namespace, sess.Spec.Cell, id)
			return r.fail(ctx, sess, err)
		}
		ready, err := r.ensureUserRuntime(ctx, cell, ns, uid)
		if err != nil {
			r.releaseSlot(ctx, sess.Namespace, sess.Spec.Cell, id)
			return r.fail(ctx, sess, err)
		}
		if !ready {
			// Keep the slot: it is this session's, and the runtime is coming
			// up for it.
			return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
		}
		if err := r.openWindow(ctx, sess, cell, ns, id, uid, binding, argv); err != nil {
			r.releaseSlot(ctx, sess.Namespace, sess.Spec.Cell, id)
			return r.fail(ctx, sess, err)
		}
		sess.Status.PodName = ids.UserRuntimePod(uid)
		// Remember which container the window lives in, so a later restart is
		// recognisable as one, and start the idle clock from now.
		sess.Status.RuntimeInstance = r.currentInstance(ctx, ns, uid)
		opened := metav1.Now()
		sess.Status.LastActivity = &opened
	} else {
		if err := r.copyCredential(ctx, sess, ns, id); err != nil {
			r.releaseSlot(ctx, sess.Namespace, sess.Spec.Cell, id)
			return r.fail(ctx, sess, fmt.Errorf("credential secret: %w", err))
		}
		if err := r.ensureSessionPod(ctx, sess, cell, ns, id, binding, argv); err != nil {
			r.releaseSlot(ctx, sess.Namespace, sess.Spec.Cell, id)
			return r.fail(ctx, sess, err)
		}
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
	// Only a one-shot session has a pod of its own; a resident one already
	// recorded the runtime that hosts its window, and overwriting that sent
	// every later lookup — status, follow-ups, attach — to a pod that does
	// not exist.
	if !sess.IsResident() {
		sess.Status.PodName = ids.SessionName(id)
	}
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
	uid, err := r.ownerUID(ctx, sess)
	if err != nil {
		return err
	}
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
	if sess.IsResident() {
		env = append(env, corev1.EnvVar{Name: runtimeapi.EnvResident, Value: "1"})
	}
	for k, v := range r.Registry.SessionEnv(binding, "$("+runtimeapi.EnvAPIKey+")") {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}
	// A CLI with its own state directory gets one here too, not only in a
	// resident runtime: a user's one-shot pods share a $HOME on the same
	// volume, so without this two concurrent sessions would write one
	// another's config and conversation state.
	stateDir := ids.SessionStateDir(uid, id)
	if v := access.SessionHomeVar(sess.Spec.Runner); v != "" {
		env = append(env, corev1.EnvVar{Name: v, Value: stateDir})
	}
	if path, content, ok := access.SessionConfig(sess.Spec.Runner, binding); ok {
		cfgJSON, err := json.Marshal(runtimeapi.AgentConfig{
			Path: filepath.Join(stateDir, path), Content: content,
		})
		if err != nil {
			return err
		}
		env = append(env, corev1.EnvVar{Name: runtimeapi.EnvAgentConfig, Value: string(cfgJSON)})
	}
	argvJSON, err := json.Marshal(argv)
	if err != nil {
		return err
	}
	env = append(env, corev1.EnvVar{Name: "AGENTCELL_AGENT_ARGV", Value: string(argvJSON)})
	// A followed session serves its own preview: the worktree is private to
	// its owner, so no shared process can read it (ADR-0009).
	ports := []corev1.ContainerPort{}
	if sess.Spec.FollowPreview && len(cell.Spec.Preview.Command) > 0 {
		previewJSON, err := json.Marshal(cell.Spec.Preview.Command)
		if err != nil {
			return err
		}
		env = append(env,
			corev1.EnvVar{Name: runtimeapi.EnvPreviewCmd, Value: string(previewJSON)},
			corev1.EnvVar{Name: runtimeapi.EnvPreviewTarget, Value: ids.WorktreePath(uid, id)},
		)
		ports = append(ports, corev1.ContainerPort{Name: "preview", ContainerPort: previewPort(cell)})
	}

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
			SecurityContext: podSecurityAs(uid),
			// The session pod runs the agent (untrusted repo + model code)
			// and never talks to git or the broker — the settle job does.
			// Give it no ServiceAccount token at all (ADR-0005 hardening).
			AutomountServiceAccountToken: ptrFalse(),
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
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{runtimeapi.RuntimeBin, "session"},
				Env:             env,
				Ports:           ports,
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
		if sess.IsResident() {
			ttl = defaultResidentIdle
		}
	}
	host := ids.SessionName(id)
	if sess.IsResident() {
		host = sess.Status.PodName
	}
	var pod corev1.Pod
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: host}, &pod)
	switch {
	case apierrors.IsNotFound(err):
		if sess.IsResident() {
			// A runtime that disappears takes its windows, not the work: the
			// worktree is on the volume and the CLI's conversation is in the
			// private $HOME. Rebuild the terminal and hand the session back
			// rather than settling it out from under its owner.
			return r.recoverResident(ctx, sess, cell, ns, id)
		}
		// Pod vanished (evicted, force-deleted): settle what's on disk.
		return r.startSettle(ctx, sess, cell, ns, id, "session pod disappeared")
	case err != nil:
		return ctrl.Result{}, err
	}

	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		// A resident slot ends the same way: its PID 1 returns when the owner
		// kills the tmux window, and the pod finishing is still the signal to
		// settle. What differs is that the agent exiting no longer ends it.
		return r.startSettle(ctx, sess, cell, ns, id, "agent finished ("+string(pod.Status.Phase)+")")
	}
	// The window is the session, not the pod — but a missing window has two
	// very different causes, and settling the wrong one destroys work.
	//
	// A tmux server dies with its container, so a RESTART takes every window
	// with it while the pod stays. That is the platform failing, not the user
	// finishing, and the session should be handed back. Only when the
	// container is the same one the window was opened in does "no window"
	// mean somebody closed it.
	if sess.IsResident() {
		if inst := runtimeInstance(&pod); inst != "" && sess.Status.RuntimeInstance != "" && inst != sess.Status.RuntimeInstance {
			return r.recoverResident(ctx, sess, cell, ns, id)
		}
		alive, working, attached, err := r.windowState(ctx, sess, ns, id)
		if err == nil && !alive {
			return r.startSettle(ctx, sess, cell, ns, id, "session window closed")
		}
		// The board asked for this, so the board hears when it is done —
		// at the moment the AGENT finishes, not when the session settles.
		//
		// Those are far apart now: a resident session stays open for
		// follow-ups and settles only much later, so waiting for settle
		// would leave the asker watching a stream that never answers.
		if !working && !sess.Status.BoardNotified && sess.Spec.Board != "" {
			r.postDoneToBoard(ctx, sess)
			sess.Status.BoardNotified = true
			if err := r.Status().Update(ctx, sess); err != nil {
				return ctrl.Result{}, err
			}
		}
		// An agent that is still running IS activity, and so is a person
		// watching: the idle clock must not tick through a long build, nor
		// through somebody reading the screen it produced.
		if working || attached {
			now := metav1.Now()
			sess.Status.LastActivity = &now
			if err := r.Status().Update(ctx, sess); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// A resident session that nobody is using stops holding compute — it
	// does not get its work published out from under it.
	//
	// Force-settling on idle was the wrong shape: five minutes of work cost
	// a slot for hours and then ENDED, so "keep it around in case I come
	// back" and "give the slot back" were the same decision. They are not.
	// Going dormant gives back the expensive thing (the runtime, the slot)
	// and keeps the cheap one (a worktree and a conversation on disk).
	if sess.IsResident() {
		if sess.Spec.DesiredState == acv1.SessionDesiredDormant {
			return r.goDormant(ctx, sess, id, "asked to stop")
		}
		idle := sess.Spec.IdleSeconds
		if idle == 0 {
			idle = defaultIdle
		}
		since := sess.Status.LastActivity
		if since == nil {
			since = sess.Status.StartTime
		}
		if since != nil && time.Since(since.Time) > time.Duration(idle)*time.Second {
			return r.goDormant(ctx, sess, id, "idle")
		}
		return ctrl.Result{RequeueAfter: pollInterval}, nil
	}

	// A one-shot session has no terminal to come back to, so its only clock
	// is total age.
	if since := sess.Status.StartTime; since != nil &&
		time.Since(since.Time) > time.Duration(ttl)*time.Second {
		if err := r.Delete(ctx, &pod); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		return r.startSettle(ctx, sess, cell, ns, id, "TTL exceeded")
	}

	return ctrl.Result{RequeueAfter: pollInterval}, nil
}

// goDormant releases the compute a session is holding and records that it is
// meant to be asleep.
//
// The window is deliberately NOT closed: closing it is what settle does, and
// it is irreversible. What happens instead is that the runtime pod becomes
// reapable — once every session a user has in this Cell is dormant or
// finished, the pod goes, and with it the tmux server. The worktree and the
// CLI's own state are on the volume, so waking rebuilds the terminal rather
// than starting over.
func (r *SessionReconciler) goDormant(ctx context.Context, sess *acv1.Session, id, why string) (ctrl.Result, error) {
	if sess.Spec.DesiredState != acv1.SessionDesiredDormant {
		// Say it in the spec, not only in the phase: "should this be awake"
		// is the question, and it has to be answerable — and overridable —
		// without reading a timer.
		sess.Spec.DesiredState = acv1.SessionDesiredDormant
		if err := r.Update(ctx, sess); err != nil {
			return ctrl.Result{}, err
		}
	}
	now := metav1.Now()
	sess.Status.Phase = acv1.SessionDormant
	sess.Status.DormantSince = &now
	sess.Status.Message = "休眠中(" + why + ")——打开终端或追问即可继续"
	if err := r.Status().Update(ctx, sess); err != nil {
		return ctrl.Result{}, err
	}
	// The slot goes back now. A dormant session consumes nothing, so holding
	// one would be charging a project for work that finished.
	r.releaseSlot(ctx, sess.Namespace, sess.Spec.Cell, id)
	// Reaping the runtime is the reclamation: it is shared, so it can only go
	// when none of this user's sessions still need it.
	if uid, err := r.ownerUID(ctx, sess); err == nil {
		_ = r.reapUserRuntime(ctx, ids.WorkloadNamespace(sess.Spec.Cell), uid, sess.Spec.Cell)
	}
	return ctrl.Result{}, nil
}

// observeDormant waits for somebody to come back, and eventually publishes.
func (r *SessionReconciler) observeDormant(ctx context.Context, sess *acv1.Session, cell *acv1.Cell, ns, id string) (ctrl.Result, error) {
	if sess.Spec.DesiredState == acv1.SessionDesiredRunning {
		return r.wake(ctx, sess, cell, ns, id)
	}
	ttl := sess.Spec.TTLSeconds
	if ttl == 0 {
		ttl = defaultDormantTTL
	}
	if since := sess.Status.DormantSince; since != nil &&
		time.Since(since.Time) > time.Duration(ttl)*time.Second {
		// Publish rather than delete. Nobody came back, but that is not
		// consent to throw the work away.
		return r.startSettle(ctx, sess, cell, ns, id, "dormant past its TTL")
	}
	// Nothing is running, so this is a cheap, rare check.
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

// wake rebuilds what dormancy gave up: a slot, a runtime, and the terminal.
func (r *SessionReconciler) wake(ctx context.Context, sess *acv1.Session, cell *acv1.Cell, ns, id string) (ctrl.Result, error) {
	claimed, err := r.claimSlot(ctx, sess, id)
	if err != nil {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if !claimed {
		// Waking queues like any other work: a project that is full is full,
		// and pretending otherwise would let dormancy be a way around the
		// gate.
		sess.Status.Message = fmt.Sprintf("等待槽位(%d 个都在用)", cell.Spec.MaxSessions)
		if err := r.Status().Update(ctx, sess); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	uid, err := r.ownerUID(ctx, sess)
	if err != nil {
		r.releaseSlot(ctx, sess.Namespace, sess.Spec.Cell, id)
		return r.fail(ctx, sess, err)
	}
	ready, err := r.ensureUserRuntime(ctx, cell, ns, uid)
	if err != nil {
		r.releaseSlot(ctx, sess.Namespace, sess.Spec.Cell, id)
		return r.fail(ctx, sess, err)
	}
	if !ready {
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}
	binding, err := r.Registry.Resolve(sess.Spec.Runner, sess.Spec.Provider, sess.Spec.Model)
	if err != nil {
		return r.fail(ctx, sess, err)
	}
	// -restore: rebuild the terminal WITHOUT running the agent again. The
	// task already ran; re-running it would redo whatever it had done.
	if err := r.restoreWindow(ctx, sess, cell, ns, id, uid, binding); err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	now := metav1.Now()
	sess.Status.Phase = acv1.SessionRunning
	sess.Status.PodName = ids.UserRuntimePod(uid)
	sess.Status.LastActivity = &now
	sess.Status.DormantSince = nil
	sess.Status.Message = "已唤醒,终端恢复到原处"
	if err := r.Status().Update(ctx, sess); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: pollInterval}, nil
}

func (r *SessionReconciler) startSettle(ctx context.Context, sess *acv1.Session, cell *acv1.Cell, ns, id, why string) (ctrl.Result, error) {
	if sess.IsResident() {
		// Close the window before settle takes the worktree, so nothing is
		// still typing into it while it is committed and reclaimed.
		if uid, err := r.ownerUID(ctx, sess); err == nil {
			r.closeWindow(ctx, ns, id, uid)
		}
	}
	if err := r.ensureSettleJob(ctx, sess, cell, ns, id); err != nil {
		return r.fail(ctx, sess, err)
	}
	sess.Status.Phase = acv1.SessionSettling
	sess.Status.Message = why
	if err := r.Status().Update(ctx, sess); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: pollInterval}, nil
}

func (r *SessionReconciler) ensureSettleJob(ctx context.Context, sess *acv1.Session, cell *acv1.Cell, ns, id string) error {
	// The settle job reads the owner's private worktree, so it must run as
	// the owner — nobody else can read it, by design.
	uid, err := r.ownerUID(ctx, sess)
	if err != nil {
		return err
	}
	settleEnv := []corev1.EnvVar{
		{Name: runtimeapi.EnvSessionID, Value: id},
		{Name: runtimeapi.EnvBaseBranch, Value: cell.Spec.Repo.Branch},
		// Push goes to this explicit URL, never to the worktree's remote
		// config — a malicious session editing .git/config cannot redirect
		// a credentialed push.
		{Name: runtimeapi.EnvRepoURL, Value: cell.Spec.Repo.URL},
	}
	if cell.Spec.Repo.SecretName != "" {
		settleEnv = append(settleEnv, gitWorkloadEnv(r.GitBrokerURL, cell.Name, ids.GitSecretName)...)
	}
	podLabels := map[string]string{ids.SessionLabelKey: id}
	settleMounts := []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}}
	volumes := []corev1.Volume{{
		Name: "workspace",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: ids.WorkspacePVC},
		},
	}}
	sa := ""
	if r.GitBrokerURL != "" {
		// settle is the only role permitted to push, and its pod name binds
		// the push to this session's branch at the broker.
		podLabels = withBrokerClientLabel(podLabels)
		sa = runtimeapi.SASettle
		settleMounts = append(settleMounts, brokerTokenMount())
		volumes = append(volumes, brokerTokenVolume())
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.SettleJobName(id)}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, job, func() error {
		if !job.CreationTimestamp.IsZero() {
			return nil
		}
		backoff := int32(2)
		job.Spec = batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: sa,
					SecurityContext:    podSecurityAs(uid),
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
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{runtimeapi.RuntimeBin, "settle"},
						SecurityContext: containerSecurity(),
						Resources:       settleResources(),
						Env:             settleEnv,
						VolumeMounts:    settleMounts,
					}},
					Volumes: volumes,
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
	// Answer where the question was asked. Work requested on a board that
	// finishes silently is work nobody knows about — the asker would have to
	// go looking, which is the habit the board exists to remove.
	r.postSettleToBoard(ctx, sess)
	r.releaseSlot(ctx, sess.Namespace, sess.Spec.Cell, id)
	if sess.IsResident() {
		// Reclaim the runtime once this user has nothing open in the Cell.
		// A tmux takes a slot; when you are done it goes away.
		if uid, err := r.ownerUID(ctx, sess); err == nil {
			if err := r.reapUserRuntime(ctx, ns, uid, sess.Spec.Cell); err != nil {
				return ctrl.Result{}, err
			}
		}
	}
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

// postSettleToBoard tells the board what came of the work it asked for.
//
// Only for sessions that came FROM a board: dispatching from the Cell page
// and being narrated on a team stream would be chatter nobody asked for.
func (r *SessionReconciler) postSettleToBoard(ctx context.Context, sess *acv1.Session) {
	if sess.Spec.Board == "" {
		return
	}
	var body string
	switch sess.Status.Phase {
	case acv1.SessionSettled:
		body = "做完了:" + sess.Status.Message + "(分支 " + sess.Status.Branch + ",待批阅)"
	case acv1.SessionDiscarded:
		body = "跑完了,但没有产出:" + sess.Status.Message
	case acv1.SessionError:
		body = "出错了:" + sess.Status.Message
	default:
		return
	}
	post := acv1.Post{
		Kind: acv1.PostAgent, Author: sess.Spec.Cell, Cell: sess.Spec.Cell,
		Session: sess.Name, Body: body, At: metav1.Now(),
	}
	r.appendBoardPost(ctx, sess, post)
}

// postDoneToBoard says the agent has finished, while the session is still
// open. The branch does not exist yet — that is what settling is for — so it
// says what is true now and points at where to look.
func (r *SessionReconciler) postDoneToBoard(ctx context.Context, sess *acv1.Session) {
	r.appendBoardPost(ctx, sess, acv1.Post{
		Kind: acv1.PostAgent, Author: sess.Spec.Cell, Cell: sess.Spec.Cell,
		Session: sess.Name,
		Body:    "跑完了。终端还开着——去看一眼,不满意就直接追问;满意就清算,产出会进批阅。",
	})
}

// appendBoardPost is the controller's half of the board: it writes where the
// work was asked for, and does nothing at all when it was not asked there.
func (r *SessionReconciler) appendBoardPost(ctx context.Context, sess *acv1.Session, post acv1.Post) {
	if sess.Spec.Board == "" {
		return
	}
	if post.At.IsZero() {
		post.At = metav1.Now()
	}
	if sess.Spec.OwnerUserID != "" && len(post.Mentions) == 0 {
		post.Mentions = []string{sess.Spec.OwnerUserID}
	}
	_ = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var b acv1.Board
		key := types.NamespacedName{Namespace: sess.Namespace, Name: "board-" + sess.Spec.Board}
		if err := r.Get(ctx, key, &b); err != nil {
			return nil
		}
		b.Append(post)
		return r.Update(ctx, &b)
	})
}
