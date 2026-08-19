package webui

import (
	"fmt"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/pkg/ids"
)

// Stopping and restarting your own session, from the console.
//
// The controller could already do both — dormancy has an "asked to stop"
// path and a runtime pod is rebuilt whenever it goes missing — but neither
// had a way in that did not involve kubectl. That gap has a specific cost:
// when a terminal is wedged, or an agent is looping on something useless,
// the only remedies the console offered were "wait" and "结束并清算", and
// the second one throws away the worktree and the conversation. Somebody
// who just wants the thing to stop should not have to end the work.
//
// Neither of these is a delete. Ending a session is settleSession, it is
// spelled differently, and it asks first.

// sleepSession parks a session: the compute goes back, everything else stays.
func (h *Handler) sleepSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.mySession(w, r)
	if !ok {
		return
	}
	if sess.Spec.DesiredState == acv1.SessionDesiredDormant {
		writeJSON(w, 200, map[string]string{"ok": "already", "message": "这条会话已经在休眠"})
		return
	}
	// State the wish; the controller reconciles it. Same path the idle timer
	// uses, so a hand-stopped session and a timed-out one cannot drift apart
	// — including the part where a slot is only released once the agent has
	// actually stopped.
	sess.Spec.DesiredState = acv1.SessionDesiredDormant
	if err := h.Client.Update(r.Context(), sess); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{
		"ok":      "sleeping",
		"message": "正在停下——worktree 和对话都留着,下次说话或打开终端就醒",
	})
}

// restartRuntime throws away the runtime this session is living in.
//
// The escape hatch for a wedged terminal or a CLI that has tied itself in a
// knot. The pod is cattle: the worktree, the git objects and the CLI's own
// conversation state all live on the volume, so the controller rebuilds it
// and `--continue` still finds the thread. What does NOT survive is whatever
// the agent was doing at that instant — which is the point, and why the UI
// asks first.
func (h *Handler) restartRuntime(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.mySession(w, r)
	if !ok {
		return
	}
	if h.Kube == nil {
		writeErr(w, 501, fmt.Errorf("这个 celld 没有集群权限,重启不了运行时"))
		return
	}
	// A session that has ENDED cannot be restarted, and saying so is the
	// whole point: Error is terminal, so the controller stops reconciling it
	// and the annotation this handler writes is never read. Accepting the
	// request would leave somebody pressing a button that does nothing, on
	// exactly the session that most needs help.
	if isTerminalPhase(sess.Status.Phase) {
		writeErr(w, 409, fmt.Errorf(
			"这条会话已经结束(%s),重启不了。回项目里再开一条新的——工作树和之前的产出都还在。",
			sess.Status.Phase))
		return
	}
	pod := sess.Status.PodName
	if pod == "" {
		writeErr(w, 409, fmt.Errorf("这条会话还没有运行时可重启"))
		return
	}
	// Say WHY the runtime is about to vanish before removing it. The
	// controller budgets recoveries so a runtime that keeps dying eventually
	// settles instead of flapping; a restart a person asked for must not
	// draw on that budget, or the third press of this button ends their
	// work. Marking first also means a crash between these two steps costs
	// nothing worse than one unspent free recovery.
	if sess.Annotations == nil {
		sess.Annotations = map[string]string{}
	}
	sess.Annotations[acv1.RestartRequestedAnnotation] = "1"
	if err := h.Client.Update(r.Context(), sess); err != nil {
		writeErr(w, 500, err)
		return
	}
	ns := ids.WorkloadNamespace(sess.Spec.Cell)
	if err := h.Kube.CoreV1().Pods(ns).Delete(r.Context(), pod, metav1.DeleteOptions{}); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{
		"ok":      "restarting",
		"message": "运行时正在重建——刚才那一步被打断了,worktree 和对话还在",
	})
}

// mySession loads the session named in the path and refuses anyone who is
// not entitled to drive it. Same rule the terminal uses, and refusals are
// 404 for the same reason (ADR-0008): a 403 would confirm it exists.
func (h *Handler) mySession(w http.ResponseWriter, r *http.Request) (*acv1.Session, bool) {
	var sess acv1.Session
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: r.PathValue("session")}, &sess); err != nil {
		writeErr(w, 404, errNotFound)
		return nil, false
	}
	if !h.maySession(r, &sess) {
		writeErr(w, 404, errNotFound)
		return nil, false
	}
	return &sess, true
}

// isTerminalPhase mirrors the controller's own rule: these phases are the
// end of a session's life, and nothing reconciles them afterwards.
func isTerminalPhase(p acv1.SessionPhase) bool {
	switch p {
	case acv1.SessionSettled, acv1.SessionDiscarded, acv1.SessionError:
		return true
	}
	return false
}
