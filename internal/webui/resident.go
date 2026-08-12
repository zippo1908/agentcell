package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/access"
	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

// A resident session keeps its slot alive in a tmux server on the owner's
// private socket, so the owner can look at what the agent did and keep going
// in the same context. Two things are needed from outside the pod: telling
// whether the agent is still working, and sending it another instruction.
//
// Both are done by exec'ing into the pod rather than by giving the pod an
// API token. The session pod deliberately holds no ServiceAccount token
// (ADR-0005) because it runs untrusted repo and model code; handing it one
// so it could report its own status would trade a real security property for
// a status field.

// execInPod runs a command in a session's host pod.
//
// The container name is resolved from the pod itself rather than assumed: a
// one-shot session runs in its own pod, a resident one is a window inside its
// owner's runtime, and callers should not have to know which.
func (h *Handler) execInPod(ctx context.Context, ns, pod string, argv []string) (string, error) {
	if h.RESTConfig == nil || h.Kube == nil {
		return "", fmt.Errorf("exec is not configured")
	}
	return execIn(ctx, h.RESTConfig, h.Kube, ns, pod, argv, nil)
}

type residentState struct {
	Resident bool   `json:"resident"`
	Live     bool   `json:"live"`               // the tmux window still exists
	Working  bool   `json:"working"`            // the agent has not finished
	ExitCode string `json:"exitCode,omitempty"` // once it has
	Attach   string `json:"attach"`             // how a human attaches
}

// sessionState answers "is it still working, or waiting for me?".
func (h *Handler) sessionState(w http.ResponseWriter, r *http.Request) {
	sess, err := h.ownedSession(r, r.PathValue("session"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if !sess.Spec.Resident {
		writeJSON(w, 200, residentState{})
		return
	}
	ns, id := ids.WorkloadNamespace(sess.Spec.Cell), sess.Status.SessionID
	host := sess.Status.PodName
	if host == "" {
		host = ids.SessionName(id)
	}
	st := residentState{
		Resident: true,
		// cell-runtime derives the socket and window inside the pod, so the
		// command works as printed and reveals no private paths.
		Attach: fmt.Sprintf("kubectl -n %s exec -it %s -- %s attach %s", ns, host, runtimeapi.RuntimeBin, id),
	}
	// The marker file is written by the shell after the agent returns, so its
	// presence (and contents) is the exit status; its absence means working.
	// Ask about the WINDOW, not the pod. A runtime that answers exec may have
	// lost this window — the owner closed it, or the runtime container
	// restarted and took every window with it while the pod stayed. Reporting
	// "the pod is reachable" as live called all of that running.
	out, _ := h.execInPod(r.Context(), ns, host,
		[]string{runtimeapi.RuntimeBin, "window-status", id})
	switch {
	case strings.Contains(out, "alive=true"):
		st.Live = true
	case strings.Contains(out, "alive=false"):
		st.Live = false
	default:
		// Could not ask (pod gone, starting, exec refused): report nothing
		// live rather than guessing either way.
		writeJSON(w, 200, st)
		return
	}
	if i := strings.Index(out, "exit="); i >= 0 {
		if code := strings.TrimSpace(out[i+len("exit="):]); code != "" && code != "-" {
			st.ExitCode = code
		}
	}
	st.Working = st.Live && st.ExitCode == ""
	writeJSON(w, 200, st)
}

// continueSession types another instruction into the live session.
func (h *Handler) continueSession(w http.ResponseWriter, r *http.Request) {
	sess, err := h.ownedSession(r, r.PathValue("session"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeErr(w, 400, fmt.Errorf("text is empty"))
		return
	}
	if !sess.Spec.Resident {
		writeErr(w, 409, fmt.Errorf("session is not resident; dispatch with resident:true to keep a slot open"))
		return
	}
	if sess.Status.Phase != acv1.SessionRunning {
		writeErr(w, 409, fmt.Errorf("session is %s, not running", sess.Status.Phase))
		return
	}
	// Continue the CLI's OWN conversation where it can. Typing the text at a
	// bare shell would start a fresh agent run that has to rediscover
	// everything the last one learned — which is exactly the waste resident
	// sessions exist to remove.
	argv, err := access.ResumeArgvFor(sess.Spec.Runner, body.Text, sess.Status.RunnerSessionID)
	if err != nil {
		// A runner with no resume of its own still gets the text; it just
		// starts a new conversation in the same worktree, and saying so is
		// better than pretending continuity we do not have.
		argv, err = access.HeadlessArgv(sess.Spec.Runner, body.Text)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
	}
	ns, id := ids.WorkloadNamespace(sess.Spec.Cell), sess.Status.SessionID
	host := sess.Status.PodName
	if host == "" {
		host = ids.SessionName(id)
	}
	// Each element stays a separate argv element the whole way down, so a
	// semicolon in the text is a semicolon and not a second command.
	out, err := h.execInPod(r.Context(), ns, host,
		append([]string{runtimeapi.RuntimeBin, "tell", id}, argv...))
	if err != nil {
		writeErr(w, 502, fmt.Errorf("%v: %s", err, out))
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "sent"})
}
