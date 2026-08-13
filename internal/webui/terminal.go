package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/identity"
	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

// The terminal, because a progress bar is not the same as watching.
//
// A session runs in tmux inside its owner's runtime — that was already true,
// for the CLIs' sake. What was missing is the other end of it: without a
// terminal, "is it working or is it stuck" is unanswerable from outside, and
// a headless agent prints nothing until it is finished. Eight minutes of
// blank log is not a slow run, it is a blind one.
//
// The rule that matters: a terminal is attached to a UID's tmux socket, and
// that socket lives in that user's private tree. It IS the authority — not a
// name to be checked against one. So this endpoint refuses anyone but the
// session's owner, membership in the Cell notwithstanding. A maintainer can
// see a project; nobody gets somebody else's keyboard.

var upgrader = websocket.Upgrader{
	// Same-origin only. The console is served from this origin; a terminal is
	// the most valuable thing here to hijack, so an unchecked Origin would be
	// handing any page on the internet a shell in the user's runtime.
	CheckOrigin: func(r *http.Request) bool {
		o := r.Header.Get("Origin")
		if o == "" {
			return false
		}
		return sameOrigin(o, r)
	},
}

// termMessage is what the browser sends up: either keystrokes or a resize.
// Output travels down as raw binary frames, unwrapped, because that is the
// hot path and every byte of framing is paid per keystroke echo.
type termMessage struct {
	Data string `json:"d,omitempty"`
	Cols uint16 `json:"c,omitempty"`
	Rows uint16 `json:"r,omitempty"`
}

func (h *Handler) sessionTerminal(w http.ResponseWriter, r *http.Request) {
	var sess acv1.Session
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: r.PathValue("session")}, &sess); err != nil {
		writeErr(w, 404, errNotFound)
		return
	}
	p := identity.FromContext(r.Context())
	// Ownership, not Cell membership. See the note above: this hands over a
	// keyboard in somebody's private runtime.
	if !p.Owns(sess.Spec.OwnerUserID) {
		writeErr(w, 404, errNotFound)
		return
	}
	if !sess.IsResident() {
		writeErr(w, 409, fmt.Errorf("this session is not resident, so it has no terminal"))
		return
	}
	if woke, err := h.wakeIfDormant(r, &sess); err != nil {
		writeErr(w, 500, err)
		return
	} else if woke {
		writeErr(w, 409, fmt.Errorf("这个会话在休眠,正在唤醒——终端马上回来"))
		return
	}
	if sess.Status.PodName == "" {
		writeErr(w, 409, fmt.Errorf("no runtime yet — the session is still starting"))
		return
	}
	if h.RESTConfig == nil || h.Kube == nil {
		writeErr(w, 501, fmt.Errorf("terminals need cluster exec access, which this celld was not given"))
		return
	}

	ns := ids.WorkloadNamespace(sess.Spec.Cell)
	id := sess.Status.SessionID
	if id == "" {
		writeErr(w, 409, fmt.Errorf("session has no id yet"))
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the response.
	}
	defer conn.Close()

	// Tear the viewer down when the socket goes, ALWAYS.
	//
	// A tmux client does not die with the exec stream that carried it: a
	// closed browser tab left `tmux attach` running in the pod for good.
	// That leak is not cosmetic — "is anybody watching" decides whether a
	// session is idle, so one abandoned tab would pin a slot awake forever
	// and reclamation would never run. Detach on a fresh context because the
	// request's is already cancelled by the time we get here.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = execIn(ctx, h.RESTConfig, h.Kube, ns, sess.Status.PodName,
			[]string{runtimeapi.RuntimeBin, "detach", id}, nil)
	}()

	// Attach rather than exec a shell: the window already exists and holds
	// the agent's own terminal. `attach` also means several viewers see the
	// same screen, which is what makes "come look at this" work.
	argv := []string{runtimeapi.RuntimeBin, "attach", id}
	if err := h.attach(r.Context(), conn, ns, sess.Status.PodName, argv); err != nil &&
		!websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		// Report into the terminal itself: a websocket that simply drops
		// leaves a blank rectangle and no way to tell a finished session from
		// a broken one.
		_ = conn.WriteMessage(websocket.BinaryMessage,
			[]byte("\r\n\x1b[31m"+err.Error()+"\x1b[0m\r\n"))
	}
}

// attach wires a websocket to a TTY inside a pod.
func (h *Handler) attach(ctx context.Context, conn *websocket.Conn, ns, pod string, argv []string) error {
	container, err := firstContainer(ctx, h.Kube, ns, pod)
	if err != nil {
		return err
	}
	req := h.Kube.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(ns).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   argv,
			Stdin:     true, Stdout: true, Stderr: false, TTY: true,
		}, scheme.ParameterCodec)
	exe, err := remotecommand.NewSPDYExecutor(h.RESTConfig, http.MethodPost, req.URL())
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	t := &wsTerminal{conn: conn, sizes: make(chan remotecommand.TerminalSize, 4), cancel: cancel}
	go t.readLoop(ctx)

	return exe.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin: t, Stdout: t, Tty: true, TerminalSizeQueue: t,
	})
}

// wsTerminal adapts a websocket to the io.Reader/io.Writer and size-queue
// that remotecommand expects.
type wsTerminal struct {
	conn   *websocket.Conn
	sizes  chan remotecommand.TerminalSize
	cancel context.CancelFunc

	// in carries keystrokes from readLoop to Read. A channel rather than a
	// buffer because the reader and the websocket live on different
	// goroutines and gorilla permits exactly one reader.
	in      chan []byte
	pending []byte
	once    sync.Once

	// writeMu: gorilla allows one concurrent writer, and both the output
	// pump and the keepalive ping write.
	writeMu sync.Mutex
}

func (t *wsTerminal) readLoop(ctx context.Context) {
	t.once.Do(func() { t.in = make(chan []byte, 32) })
	defer close(t.in)
	// A terminal is idle for long stretches while an agent thinks. Without a
	// ping, an intermediary times the connection out and the window goes
	// dead mid-run with nothing to say why.
	go t.keepalive(ctx)
	t.conn.SetReadLimit(1 << 20)
	for {
		typ, data, err := t.conn.ReadMessage()
		if err != nil {
			t.cancel()
			return
		}
		if typ != websocket.TextMessage {
			continue
		}
		var m termMessage
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		if m.Cols > 0 && m.Rows > 0 {
			select {
			case t.sizes <- remotecommand.TerminalSize{Width: m.Cols, Height: m.Rows}:
			default: // a stale resize is worth less than blocking the reader
			}
		}
		if m.Data != "" {
			select {
			case t.in <- []byte(m.Data):
			case <-ctx.Done():
				return
			}
		}
	}
}

func (t *wsTerminal) keepalive(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.writeMu.Lock()
			err := t.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			t.writeMu.Unlock()
			if err != nil {
				t.cancel()
				return
			}
		}
	}
}

func (t *wsTerminal) Read(p []byte) (int, error) {
	t.once.Do(func() { t.in = make(chan []byte, 32) })
	if len(t.pending) == 0 {
		b, ok := <-t.in
		if !ok {
			return 0, io.EOF
		}
		t.pending = b
	}
	n := copy(p, t.pending)
	t.pending = t.pending[n:]
	return n, nil
}

func (t *wsTerminal) Write(p []byte) (int, error) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if err := t.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (t *wsTerminal) Next() *remotecommand.TerminalSize {
	s, ok := <-t.sizes
	if !ok {
		return nil
	}
	return &s
}

// sameOrigin compares a websocket Origin header against the request's own
// host, taking the forwarded headers into account the same way the CSRF
// check does — celld normally sits behind a TLS-terminating proxy, so its
// own idea of scheme and host is not the browser's.
func sameOrigin(origin string, r *http.Request) bool {
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return strings.EqualFold(origin, scheme+"://"+host)
}
