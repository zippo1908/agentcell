package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/access"
	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

// One tmux server per user, not per session.
//
// The agent CLIs manage conversations themselves — Claude Code by an id the
// caller chooses, Codex by its own bookkeeping — so a process per
// conversation buys nothing and costs a pod each. What the platform owes them
// is a private $HOME to keep that state in (ADR-0009) and a terminal that
// outlives any single agent run (ADR-0010).
//
// A user's runtime therefore holds every session they have open in a Cell,
// as windows. It holds no credential of its own: model keys arrive per
// window, over the exec channel, when a session starts.

// ExecFunc runs a command inside a pod. Injected rather than built here so
// the reconciler stays testable without a cluster.
type ExecFunc func(ctx context.Context, ns, pod string, argv []string, stdin io.Reader) (string, error)

// ensureUserRuntime creates (and waits for) the runtime pod that hosts this
// user's sessions in this Cell.
func (r *SessionReconciler) ensureUserRuntime(ctx context.Context, cell *acv1.Cell, ns string, uid int64) (bool, error) {
	name := ids.UserRuntimePod(uid)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, pod, func() error {
		if !pod.CreationTimestamp.IsZero() {
			return nil // pod spec is immutable; observe only
		}
		pod.Labels = map[string]string{
			ids.CellLabelKey:        cell.Name,
			ids.UserRuntimeLabelKey: fmt.Sprint(uid),
		}
		pod.Spec = corev1.PodSpec{
			RestartPolicy:   corev1.RestartPolicyAlways,
			SecurityContext: podSecurityAs(uid),
			// Like a session pod, and for the same reason: it runs the user's
			// agents, which run repository and model code (ADR-0005).
			AutomountServiceAccountToken: ptrFalse(),
			Affinity:                     anchorAffinity(),
			Containers: []corev1.Container{{
				Name:            runtimeapi.UserRuntimeContainer,
				Image:           cell.Spec.Image,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{runtimeapi.RuntimeBin, "runtime"},
				SecurityContext: containerSecurity(),
				// Sized for what it actually holds. A runtime is not a
				// bookkeeping pod: every one of this user's sessions runs its
				// agent inside it, so asking for a tmux server's worth of
				// memory would have the scheduler place several GB of CLI on
				// a node that never agreed to it, and the first OOM would
				// take all of that user's sessions at once.
				//
				// The honest number is the Cell's per-session budget times
				// the slots a user can occupy. It is also the cost of sharing
				// a runtime: Kubernetes now bounds the user, not the session.
				Resources:    runtimeResources(cell),
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
	if err != nil {
		// Two sessions starting together both read "absent" from the cache
		// and both create. Losing that race is the runtime existing, which is
		// the goal — not a failure. Same family as the read-after-create miss
		// below: this client is cache-backed, so absence is never proof.
		if !apierrors.IsAlreadyExists(err) {
			return false, err
		}
	}
	var live corev1.Pod
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &live); err != nil {
		if apierrors.IsNotFound(err) {
			// Just created: the client reads through an informer cache, which
			// has not seen it yet. Not an error — not ready yet.
			return false, nil
		}
		return false, err
	}
	for _, c := range live.Status.ContainerStatuses {
		if c.Ready {
			return true, nil
		}
	}
	return false, nil
}

// openWindow starts a session inside the user's runtime.
//
// The model credential is written to the command's stdin, never passed in
// argv, and reaches the window through a 0600 file it sources and unlinks.
//
// Be precise about what that buys: it keeps the key out of process listings,
// out of shell history and off disk. It does NOT make the key private to one
// session. Every window in a runtime runs as the same uid, and under the
// default /proc model a process can read a sibling's environment. The real
// boundary here is the USER, not the session; per-session secrecy would need
// per-session uids or pods, which is exactly what sharing a runtime trades
// away (ADR-0010).
func (r *SessionReconciler) openWindow(ctx context.Context, sess *acv1.Session, cell *acv1.Cell, ns, id string, uid int64, binding access.Binding, argv []string) error {
	return r.openWindowMode(ctx, sess, cell, ns, id, uid, binding, argv, false)
}

// restoreWindow rebuilds the terminal for an existing session without
// starting the agent again.
func (r *SessionReconciler) restoreWindow(ctx context.Context, sess *acv1.Session, cell *acv1.Cell, ns, id string, uid int64, binding access.Binding) error {
	return r.openWindowMode(ctx, sess, cell, ns, id, uid, binding, nil, true)
}

func (r *SessionReconciler) openWindowMode(ctx context.Context, sess *acv1.Session, cell *acv1.Cell, ns, id string, uid int64, binding access.Binding, argv []string, restore bool) error {
	if r.Exec == nil {
		return fmt.Errorf("resident sessions need exec access to the runtime pod")
	}
	var sec corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: sess.Namespace, Name: sess.Spec.CredentialSecret}, &sec); err != nil {
		return err
	}
	key := string(sec.Data["key"])
	if key == "" {
		return fmt.Errorf("secret %q has no %q entry", sess.Spec.CredentialSecret, "key")
	}
	// One JSON object, not KEY=VALUE lines.
	//
	// Line-oriented framing forced a choice between mangling a task that
	// contains a newline and refusing it — and refusing it was wrong, because
	// the console offers a multi-line box. A briefing is prose; the transport
	// has to carry prose.
	vars := map[string]string{runtimeapi.EnvAPIKey: key}
	for k, v := range r.Registry.SessionEnv(binding, key) {
		vars[k] = v
	}
	// The briefing values the worktree needs, carried as window environment
	// so the applet reads them the same way the one-shot pod does.
	// A CLI that resumes by recency needs its own state directory, or two of
	// this user's sessions — same runtime, same $HOME — would resume into
	// each other's conversation.
	stateDir := ids.SessionStateDir(uid, id)
	if v := access.SessionHomeVar(sess.Spec.Runner); v != "" {
		vars[v] = stateDir
	}
	// Some CLIs read the endpoint from a file rather than the environment,
	// so the window has to carry the file too — otherwise a resident session
	// would quietly use the CLI's own default provider while a one-shot
	// session of the same runner used the one that was asked for.
	if path, content, ok := access.SessionConfig(sess.Spec.Runner, binding); ok {
		cfgJSON, err := json.Marshal(runtimeapi.AgentConfig{
			Path: filepath.Join(stateDir, path), Content: content,
		})
		if err != nil {
			return err
		}
		vars[runtimeapi.EnvAgentConfig] = string(cfgJSON)
	}
	vars[runtimeapi.EnvSessionID] = id
	vars[runtimeapi.EnvBaseBranch] = cell.Spec.Repo.Branch
	vars[runtimeapi.EnvTask] = sess.Spec.Task
	vars[runtimeapi.EnvDescription] = cell.Spec.Description
	env, err := json.Marshal(vars)
	if err != nil {
		return err
	}

	cmd := append([]string{runtimeapi.RuntimeBin, "window-open", id}, argv...)
	if restore {
		cmd = []string{runtimeapi.RuntimeBin, "window-open", "-restore", id}
	}
	out, err := r.Exec(ctx, ns, ids.UserRuntimePod(uid), cmd, bytes.NewReader(env))
	if err != nil {
		return fmt.Errorf("open window: %v: %s", err, out)
	}
	return nil
}

// closeWindow ends a session's window before settle takes the worktree.
// Best effort: a window that is already gone is the state we wanted, and a
// runtime pod that has died takes its windows with it.
func (r *SessionReconciler) closeWindow(ctx context.Context, ns, id string, uid int64) {
	if r.Exec == nil {
		return
	}
	_, _ = r.Exec(ctx, ns, ids.UserRuntimePod(uid),
		[]string{runtimeapi.RuntimeBin, "window-close", id}, nil)
}

// reapUserRuntime removes a runtime with no sessions left to hold.
//
// The slot is reclaimed when the user stops using it — the original shape of
// this product ("a tmux takes one slot; log off and it is reclaimed") — not
// when any one agent finishes.
func (r *SessionReconciler) reapUserRuntime(ctx context.Context, ns string, uid int64, cell string) error {
	var list acv1.SessionList
	if err := r.List(ctx, &list); err != nil {
		return err
	}
	for i := range list.Items {
		s := &list.Items[i]
		if s.Spec.Cell != cell || !s.Spec.Resident || s.DeletionTimestamp != nil {
			continue
		}
		// In use is everything that is not finished — including a session that
		// has no phase yet, which is precisely the one being started right
		// now. Reaping on "not Running" killed a runtime out from under a
		// session that was still coming up.
		switch s.Status.Phase {
		case acv1.SessionSettled, acv1.SessionDiscarded, acv1.SessionError:
		default:
			return nil // still in use
		}
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.UserRuntimePod(uid)}}
	if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// maxRecoveries bounds how often a session's runtime is rebuilt under it.
// A node that keeps evicting must eventually settle the work rather than
// loop: the point of recovery is not losing a conversation, not surviving
// anything indefinitely.
const maxRecoveries = 3

// recoverResident rebuilds a resident session's terminal after its runtime
// went away.
//
// It deliberately does NOT re-run the agent. Whatever it had already done is
// committed to the worktree, so a second run would duplicate it; and the
// conversation itself is the CLI's, resumable by its own id when the owner
// says the next thing. Recovery restores the terminal — the user decides
// what happens in it.
func (r *SessionReconciler) recoverResident(ctx context.Context, sess *acv1.Session, cell *acv1.Cell, ns, id string) (ctrl.Result, error) {
	if sess.Status.Recoveries >= maxRecoveries {
		return r.startSettle(ctx, sess, cell, ns, id,
			fmt.Sprintf("runtime lost %d times; settling rather than rebuilding again", sess.Status.Recoveries))
	}
	uid, err := r.ownerUID(ctx, sess)
	if err != nil {
		return ctrl.Result{}, err
	}
	ready, err := r.ensureUserRuntime(ctx, cell, ns, uid)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	binding, err := r.Registry.Resolve(sess.Spec.Runner, sess.Spec.Provider, sess.Spec.Model)
	if err != nil {
		return r.fail(ctx, sess, err)
	}
	if err := r.restoreWindow(ctx, sess, cell, ns, id, uid, binding); err != nil {
		return ctrl.Result{}, err
	}
	sess.Status.Recoveries++
	sess.Status.PodName = ids.UserRuntimePod(uid)
	sess.Status.RuntimeInstance = r.currentInstance(ctx, ns, uid)
	sess.Status.Message = "runtime rebuilt; the conversation is where you left it"
	if err := r.Status().Update(ctx, sess); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: pollInterval}, nil
}

// windowAlive asks the runtime whether this session's window still exists.
//
// Errors are NOT treated as death: an exec can fail because the API server
// is busy or the pod is mid-restart, and settling a session on a transient
// failure would destroy exactly what resident sessions exist to keep.
func (r *SessionReconciler) windowState(ctx context.Context, sess *acv1.Session, ns, id string) (alive, working bool, err error) {
	if r.Exec == nil || sess.Status.PodName == "" {
		return true, false, nil
	}
	out, execErr := r.Exec(ctx, ns, sess.Status.PodName,
		[]string{runtimeapi.RuntimeBin, "window-status", id}, nil)
	if execErr != nil {
		if strings.Contains(out, "alive=false") {
			// The applet ran and reported honestly; the non-zero exit is its
			// answer, not a failure to ask.
			return false, false, nil
		}
		return true, false, execErr
	}
	alive = strings.Contains(out, "alive=true")
	// exit=- means the agent has not finished, i.e. it is still working.
	working = alive && strings.Contains(out, "exit=-")
	return alive, working, nil
}

// runtimeResources sizes a user's runtime for the sessions it can hold.
//
// Requests stay modest — an idle runtime is a tmux server — but the LIMIT is
// the full budget, because the agents run in here. Splitting them that way
// keeps scheduling honest without reserving a Cell's worth of memory for a
// user who has one window open.
func runtimeResources(cell *acv1.Cell) corev1.ResourceRequirements {
	slots := cell.Spec.MaxSessions
	if slots <= 0 {
		slots = 2
	}
	cpu, cpuErr := resource.ParseQuantity(cell.Spec.SessionResources.CPU)
	mem, memErr := resource.ParseQuantity(cell.Spec.SessionResources.Memory)
	if cpuErr != nil || memErr != nil {
		// The Session controller validates these and fails loudly; here a bad
		// value must not block the runtime, so fall back to the documented
		// defaults rather than to something arbitrarily small.
		cpu, mem = resource.MustParse("1"), resource.MustParse("2Gi")
	}
	limitCPU := cpu.DeepCopy()
	limitMem := mem.DeepCopy()
	for range slots - 1 {
		limitCPU.Add(cpu)
		limitMem.Add(mem)
	}
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    limitCPU,
			corev1.ResourceMemory: limitMem,
		},
	}
}

// runtimeInstance identifies the container a runtime pod is currently
// running, so a restart is distinguishable from a window someone closed.
//
// The container ID changes on every restart, which is exactly the property
// needed; RestartCount would work too but resets if the pod is replaced,
// and the ID does not have to be interpreted to be compared.
func runtimeInstance(pod *corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == runtimeapi.UserRuntimeContainer {
			if cs.ContainerID != "" {
				return cs.ContainerID
			}
			// Before the ID is published, the start time still changes on a
			// restart, which is enough to notice one.
			if cs.State.Running != nil {
				return cs.State.Running.StartedAt.String()
			}
		}
	}
	return ""
}

// currentInstance reads the runtime's container identity, best effort: a
// blank value simply means the next reconcile records it instead.
func (r *SessionReconciler) currentInstance(ctx context.Context, ns string, uid int64) string {
	var pod corev1.Pod
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.UserRuntimePod(uid)}, &pod); err != nil {
		return ""
	}
	return runtimeInstance(&pod)
}
