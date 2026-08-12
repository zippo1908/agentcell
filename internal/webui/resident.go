package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/pkg/ids"
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

// execInPod runs a command in a session pod and returns its combined output.
func (h *Handler) execInPod(ctx context.Context, ns, pod string, argv []string) (string, error) {
	if h.RESTConfig == nil {
		return "", fmt.Errorf("exec is not configured")
	}
	req := h.Kube.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(ns).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "session",
			Command:   argv,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
	exe, err := remotecommand.NewSPDYExecutor(h.RESTConfig, http.MethodPost, req.URL())
	if err != nil {
		return "", err
	}
	var out, errOut bytes.Buffer
	if err := exe.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &out, Stderr: &errOut}); err != nil {
		return out.String() + errOut.String(), err
	}
	return out.String() + errOut.String(), nil
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
	st := residentState{
		Resident: true,
		Attach: fmt.Sprintf("kubectl -n %s exec -it %s -- tmux -S %s attach -t %s",
			ns, ids.SessionName(id), "$AGENTCELL_TMUX_SOCKET", ids.TmuxWindow(id)),
	}
	// The marker file is written by the shell after the agent returns, so its
	// presence (and contents) is the exit status; its absence means working.
	out, err := h.execInPod(r.Context(), ns, ids.SessionName(id),
		[]string{"sh", "-c", "cat .agentcell/agent.done 2>/dev/null || true"})
	if err != nil {
		// The pod may be gone or starting; that is not an error for a status
		// question, it just means there is nothing live to report.
		writeJSON(w, 200, st)
		return
	}
	st.Live = true
	if code := strings.TrimSpace(out); code != "" {
		st.ExitCode = code
	} else {
		st.Working = true
	}
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
	ns, id := ids.WorkloadNamespace(sess.Spec.Cell), sess.Status.SessionID
	// Passed as a single argv element, never interpolated into a shell string:
	// the text is user input and tmux would otherwise happily run whatever a
	// semicolon introduces.
	out, err := h.execInPod(r.Context(), ns, ids.SessionName(id),
		[]string{"cell-runtime", "tell", body.Text})
	if err != nil {
		writeErr(w, 502, fmt.Errorf("%v: %s", err, out))
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "sent"})
}

var _ = rest.Config{}
