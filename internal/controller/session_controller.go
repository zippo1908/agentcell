package controller

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
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
	// Library reads the project files people uploaded, so a session starts
	// with the specification in its worktree instead of the agent being
	// told about documents it cannot open. nil simply means no library.
	Library LibraryReader
}

// maxLibraryBytes bounds the uncompressed text shipped into one session.
// Generous for prose — roughly a small book — and far below the pod spec
// limit even before compression.
const maxLibraryBytes = 2 << 20

// LibraryReader is the slice of the store this controller needs: the
// readable layer of one project's files. An interface, not the store type,
// so the controller keeps no opinion about where those files live.
type LibraryReader interface {
	TextLayer(ctx context.Context, cell string) (map[string]string, error)
}

// libraryBlob packs a project's readable files for the runtime.
//
// A tar rather than a JSON map because it lands as a directory tree the
// agent walks with the same tools it uses for code; gzip because a
// specification is text and text compresses, and this value travels in a
// window environment.
func (r *SessionReconciler) libraryBlob(ctx context.Context, cell string) string {
	if r.Library == nil {
		return ""
	}
	files, err := r.Library.TextLayer(ctx, cell)
	if err != nil || len(files) == 0 {
		return ""
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	// Sorted, so the same library produces the same bytes and an unchanged
	// project does not look changed on every reconcile.
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	// Bounded, because for a one-shot session this value travels in the POD
	// SPEC — and a spec that exceeds what the API server accepts does not
	// truncate, it fails, so an oversized library would stop sessions from
	// starting at all rather than arriving incomplete.
	total := 0
	var omitted []string
	for _, n := range names {
		body := []byte(files[n])
		if total+len(body) > maxLibraryBytes {
			omitted = append(omitted, n)
			continue
		}
		total += len(body)
		if err := tw.WriteHeader(&tar.Header{
			Name: n, Mode: 0o644, Size: int64(len(body)),
		}); err != nil {
			return ""
		}
		if _, err := tw.Write(body); err != nil {
			return ""
		}
	}
	// Say what was left out, in the library itself. A silently partial
	// corpus is the worst possible shape: the agent answers confidently
	// from the half it received and nobody can tell which half that was.
	if len(omitted) > 0 {
		note := "这些文件太大,没有放进这次会话(在控制台的文件页里能看到):\n" +
			strings.Join(omitted, "\n") + "\n"
		if err := tw.WriteHeader(&tar.Header{
			Name: "_omitted.txt", Mode: 0o644, Size: int64(len(note)),
		}); err == nil {
			_, _ = tw.Write([]byte(note))
		}
	}
	if err := tw.Close(); err != nil {
		return ""
	}
	if err := zw.Close(); err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
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
	// Repo and Repos carry a project group's per-repository results. Absent
	// on a single-repo project, which reports exactly as it always did.
	Repo  string         `json:"repo,omitempty"`
	Repos []settleResult `json:"repos,omitempty"`
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
	// A one-shot session with nothing to do is a mistake, not a terminal:
	// unlike a resident session there is no window to type into, so it
	// would spend a run on an empty prompt and settle. Refuse where it can
	// still be read as an error.
	if !sess.IsResident() && strings.TrimSpace(sess.Spec.Task) == "" {
		return r.fail(ctx, sess, fmt.Errorf("这条会话没有任务,也没有终端可以输入"))
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
	// No key is a legitimate state now: somebody who connected an account
	// has a credential, it is simply not one they pasted. Looking one up
	// anyway asked the API server for Secret "" and reported "Secret \"\"
	// not found" — which reads like the platform lost something rather than
	// like nothing was ever meant to be there.
	if sess.Spec.CredentialSecret != "" {
		if err := r.Get(ctx, types.NamespacedName{
			Namespace: sess.Namespace, Name: sess.Spec.CredentialSecret}, &src); err != nil {
			return err
		}
		if _, ok := src.Data["key"]; !ok {
			return fmt.Errorf("secret %q has no %q entry", sess.Spec.CredentialSecret, "key")
		}
	} else if r.accountCredential(ctx, sess) == "" {
		return fmt.Errorf("这条会话既没有模型 key,也没有连好的账号")
	}
	dst := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.SessionSecretName(id)}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, dst, func() error {
		dst.Data = map[string][]byte{"key": src.Data["key"]}
		// A stored account login travels the same way the model key does —
		// by Secret reference, never in the pod spec — so a person who
		// connected their Kimi account does not also have to paste a key.
		if cred := r.accountCredential(ctx, sess); cred != "" {
			dst.Data["account"] = []byte(cred)
		}
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
		{Name: runtimeapi.EnvLibrary, Value: r.libraryBlob(ctx, sess.Spec.Cell)},
		{Name: runtimeapi.EnvAccount, ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: ids.SessionSecretName(id)},
				Key:                  "account",
				// Optional: most people use a key rather than an account, and
				// a missing entry must not stop a session from starting.
				Optional: ptrTrue(),
			},
		}},
		{Name: runtimeapi.EnvSessionID, Value: id},
		{Name: runtimeapi.EnvTask, Value: sess.Spec.Task},
		{Name: runtimeapi.EnvRunner, Value: sess.Spec.Runner},
		{Name: runtimeapi.EnvBaseBranch, Value: cell.Spec.Repo.Branch},
		{Name: runtimeapi.EnvDescription, Value: cell.Spec.Description},
		{Name: runtimeapi.EnvRepos, Value: reposJSON(cell)},
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
	if path, content, ok := access.SessionConfig(sess.Spec.Runner, binding,
		r.accountCredential(ctx, sess) != ""); ok {
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
	// Hoisted: whether the agent is mid-run is observed in one place and
	// needed in two — the idle clock, and the refusal to sleep a session
	// whose agent is still going.
	var working, alive bool
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
		var attached bool
		var wErr error
		alive, working, attached, wErr = r.windowState(ctx, sess, ns, id)
		err = wErr
		// While this session is alive it is the one holding the account's
		// live credential, because the CLI refreshes on its own schedule
		// and the provider rotates the refresh token when it does. Read it
		// back each time round so the stored copy stays the current one —
		// otherwise the NEXT session starts with a token that was already
		// rotated away and the person is told to log in again.
		if alive {
			if uid, uerr := r.ownerUID(ctx, sess); uerr == nil {
				r.syncAccountCredential(ctx, sess, ns, id, uid)
			}
		}
		// A deadline somebody SET is still a deadline. Only an explicit
		// TTLSeconds ends a resident session: the default here is an idle
		// value, and treating it as an absolute lifetime would put a
		// stopwatch on every project. Checked before the window, because
		// rebuilding the window of something whose deadline has passed
		// would resurrect exactly what the deadline was set to end.
		if sess.Spec.TTLSeconds > 0 && sess.Status.StartTime != nil &&
			time.Since(sess.Status.StartTime.Time) > time.Duration(sess.Spec.TTLSeconds)*time.Second {
			return r.startSettle(ctx, sess, cell, ns, id, "TTL exceeded")
		}
		if err == nil && !alive {
			// Rebuild it. The window is where the agent lives, not what the
			// work IS: the worktree is on the volume and the CLI's own
			// conversation is in the private home, so a lost window is a
			// lost terminal and nothing else. Settling here was the single
			// biggest reason a project seemed to end by itself — a tmux
			// window can go away because a CLI exited, because somebody
			// typed exit, or because the runtime was replaced, and none of
			// those is a person saying "I am done with this project".
			return r.recoverResident(ctx, sess, cell, ns, id)
		}
		// A follow-up written while the session was asleep is delivered now
		// that its terminal is back. Same path whether it was awake or not,
		// so the two cannot drift into behaving differently.
		if sess.Spec.PendingTask != "" && alive {
			if err := r.deliverPending(ctx, sess, ns, id); err != nil {
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			now := metav1.Now()
			sess.Status.LastActivity = &now
			sess.Status.BoardNotified = false
			if err := r.Status().Update(ctx, sess); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: pollInterval}, nil
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
			// Not while the agent is mid-run. Sleeping releases the slot and
			// makes the runtime reapable, but the agent keeps running inside
			// the shared runtime — so the Cell would report capacity it does
			// not have, and the work would be killed later by a reap it
			// never expected. The request is honoured the moment it finishes.
			if working {
				sess.Status.Message = "收到,agent 跑完这一段就休眠"
				if err := r.Status().Update(ctx, sess); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: pollInterval}, nil
			}
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
		// Stay parked. A dormant session holds no compute — only a worktree
		// on a volume — so there is nothing to reclaim by ending it, and
		// "you did not open this project for a week" is not a decision to
		// deliver its work. Ending a project is something a person does.
		_ = ttl
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
		// And every repository of a project group, for the same reason: the
		// push target is decided here, from the spec, never from whatever a
		// worktree's own config happens to say.
		{Name: runtimeapi.EnvRepos, Value: reposJSON(cell)},
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
		// A project group reports per repository, and each of those is its own
		// reviewable thing: separate remotes, separate reviewers, separate
		// verdicts. Only repositories that actually produced something are
		// listed — a repository the task never touched is not a decision
		// anybody has to make.
		for _, rr := range result.Repos {
			if !rr.Produced {
				continue
			}
			sess.Status.Outputs = append(sess.Status.Outputs, acv1.RepoOutput{
				Repo: rr.Repo, Branch: rr.Branch, Produced: true,
				Message: rr.Message, Review: acv1.ReviewPending,
			})
		}
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

// deliverPending types a queued follow-up into the session's terminal and
// clears it. Clearing FIRST would risk losing the instruction if the exec
// failed; clearing after means a retry can repeat it, which is the milder of
// the two failures — a duplicated sentence rather than a lost one.
func (r *SessionReconciler) deliverPending(ctx context.Context, sess *acv1.Session, ns, id string) error {
	if r.Exec == nil {
		return fmt.Errorf("no exec channel")
	}
	// With an interactive agent in the window, a follow-up is typed at it —
	// the same thing the person would do. Starting another one-shot process
	// would either be read as text by the agent already there, or launch a
	// second agent inside the first.
	var cmd []string
	if len(access.InteractiveArgvFor(sess.Spec.Runner)) > 0 {
		cmd = []string{runtimeapi.RuntimeBin, "tell", id, "-say", sess.Spec.PendingTask}
	} else {
		argv, err := access.ResumeArgvFor(sess.Spec.Runner, sess.Spec.PendingTask, sess.Status.RunnerSessionID)
		if err != nil {
			// A runner that cannot resume starts fresh in the same worktree,
			// which is honest and visible — the alternative is silently
			// dropping what somebody asked for.
			argv, err = access.HeadlessArgv(sess.Spec.Runner, sess.Spec.PendingTask)
			if err != nil {
				return err
			}
		}
		cmd = append([]string{runtimeapi.RuntimeBin, "tell", id}, argv...)
	}
	if _, err := r.Exec(ctx, ns, sess.Status.PodName, cmd, nil); err != nil {
		return err
	}
	sess.Spec.PendingTask = ""
	return r.Update(ctx, sess)
}

// accountCredential is the session owner's stored CLI login, if they have
// connected an account for this runner.
//
// Looked up by owner rather than named on the Session: an account is a
// property of the person, and asking them to name it on every dispatch would
// be asking them to remember something the platform already knows.
func (r *SessionReconciler) accountCredential(ctx context.Context, sess *acv1.Session) string {
	if sess.Spec.Runner != "kimi" || sess.Spec.OwnerUserID == "" {
		return ""
	}
	var sec corev1.Secret
	name := strings.TrimPrefix(sess.Spec.OwnerUserID, "u-") + "-kimi"
	if err := r.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: name}, &sec); err != nil {
		return ""
	}
	return string(sec.Data["kimi-credentials"])
}
