package controller

import (
	"context"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("50m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
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
// argv: argv is readable from /proc by every other window this user has open.
// Same user, but a session is still the boundary a credential is scoped to.
func (r *SessionReconciler) openWindow(ctx context.Context, sess *acv1.Session, cell *acv1.Cell, ns, id string, uid int64, binding access.Binding, argv []string) error {
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
	var env strings.Builder
	fmt.Fprintf(&env, "%s=%s\n", runtimeapi.EnvAPIKey, key)
	for k, v := range r.Registry.SessionEnv(binding, key) {
		fmt.Fprintf(&env, "%s=%s\n", k, v)
	}
	// The briefing values the worktree needs, carried as window environment
	// so the applet reads them the same way the one-shot pod does.
	fmt.Fprintf(&env, "%s=%s\n", runtimeapi.EnvSessionID, id)
	fmt.Fprintf(&env, "%s=%s\n", runtimeapi.EnvBaseBranch, cell.Spec.Repo.Branch)
	// One line each, so a task containing a newline cannot inject another
	// variable. Rejected rather than mangled: a briefing is not worth a
	// credential-shaped hole.
	if strings.ContainsAny(sess.Spec.Task, "\n\r") || strings.ContainsAny(cell.Spec.Description, "\n\r") {
		return fmt.Errorf("task and description must be single-line to cross the exec channel")
	}
	fmt.Fprintf(&env, "%s=%s\n", runtimeapi.EnvTask, sess.Spec.Task)
	fmt.Fprintf(&env, "%s=%s\n", runtimeapi.EnvDescription, cell.Spec.Description)

	cmd := append([]string{runtimeapi.RuntimeBin, "window-open", id}, argv...)
	out, err := r.Exec(ctx, ns, ids.UserRuntimePod(uid), cmd, strings.NewReader(env.String()))
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
