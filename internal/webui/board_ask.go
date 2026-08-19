package webui

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/pkg/ids"
)

// Streaming the board's answer back to the person who asked.
//
// The ack post ("接了:…") says the work was accepted; it says nothing while
// the agent thinks. This endpoint is the gap between the two: the asker holds
// an SSE stream and watches the answer form, and when it completes the full
// text lands on the board as an ordinary agent post — so the stream is a
// convenience for the person watching, never the only copy. People who poll
// the board see exactly what the stream's holder saw.
//
// The stream is produced by exec'ing `kimi -p … --output-format stream-json`
// inside the board session's own runtime pod. The pod already holds the
// credential (the state directory's symlink) and the worktree; the exec
// carries only paths and the task text. Secrets never leave the pod — the
// same boundary the login flow is built around.

const (
	// askTTL is how long a registered ask remains answerable. Long enough for
	// a slow dispatch to come up; short enough that the map cannot accumulate
	// asks nobody ever opened a stream for.
	askTTL = 10 * time.Minute
	// askReadyDeadline bounds waiting for the runtime pod and the CLI's
	// config to exist. A session that is not up by then is not coming in any
	// timeframe a held-open HTTP connection should wait for.
	askReadyDeadline = 120 * time.Second
	// askStreamTimeout caps the whole exchange. A kimi run that has not
	// finished in five minutes has left "quick ask on the board" territory.
	askStreamTimeout = 5 * time.Minute
	// askHeartbeat keeps intermediaries from timing out an idle-looking
	// stream while the agent thinks.
	askHeartbeat = 15 * time.Second
)

// askEntry is one @机器人 question waiting for its answer to be streamed.
type askEntry struct {
	Cell    string
	Task    string // the post text with @mentions stripped, as dispatched
	Asker   string
	Created time.Time
}

// askRegistry maps ask ids to their entries.
//
// Deliberately in memory: this deployment runs one celld (the accounts
// database allows exactly one writer), so a shared store would be
// infrastructure bought for a property the deployment already has — the same
// seam as the login limiter, and both move together when there is a shared
// store to move them to.
type askRegistry struct {
	mu sync.Mutex
	m  map[string]askEntry
}

// put registers an ask and returns its id. Random rather than sequential so
// an id tells nobody how many asks came before it or what to guess next.
func (r *askRegistry) put(e askEntry) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	id := hex.EncodeToString(b)
	r.mu.Lock()
	if r.m == nil {
		r.m = map[string]askEntry{}
	}
	e.Created = time.Now()
	r.m[id] = e
	r.mu.Unlock()
	return id
}

// get looks an ask up, sweeping expired entries as it goes. Lazy expiry
// beats a janitor goroutine at this size: the map is only ever as large as
// the asks since the last lookup.
func (r *askRegistry) get(id string) (askEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range r.m {
		if time.Since(v.Created) > askTTL {
			delete(r.m, k)
		}
	}
	e, ok := r.m[id]
	return e, ok
}

// boardAsk streams one registered ask's answer as server-sent events.
func (h *Handler) boardAsk(w http.ResponseWriter, r *http.Request) {
	t, _, ok := h.boardFor(w, r)
	if !ok {
		return
	}
	e, ok := h.asks.get(r.PathValue("ask"))
	if !ok || e.Cell != t.Name {
		// Same refusal shape as everything else here: an unknown ask and
		// somebody else's ask are indistinguishable.
		writeErr(w, 404, errNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, fmt.Errorf("streaming is not supported by this server"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(200)

	// The request's own context is the cancellation: a closed tab stops the
	// exec. The timeout caps it from the other side.
	ctx, cancel := context.WithTimeout(r.Context(), askStreamTimeout)
	defer cancel()

	emit := func(v any) {
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	fail := func(err error) {
		emit(map[string]any{"t": "error", "message": err.Error()})
	}

	sess, err := h.waitBoardRuntime(ctx, t.Name)
	if err != nil {
		fail(err)
		return
	}
	if h.RESTConfig == nil {
		fail(fmt.Errorf("这个 celld 没有集群 exec 权限,流不了"))
		return
	}

	uid, err := runtimeUID(sess.Status.PodName)
	if err != nil {
		fail(err)
		return
	}
	id := sess.Status.SessionID
	home := ids.UserHome(uid) + "/home"
	state := ids.SessionStateDir(uid, id)
	worktree := ids.WorktreePath(uid, id)
	// The task travels as an argv element ($1), never interpolated into the
	// script: a quote in the question is a quote, not a second command. The
	// paths are platform-derived (digits and hex), so interpolating THEM is
	// safe — and they are the only thing interpolated.
	script := "export HOME=" + home + " KIMI_CODE_HOME=" + state +
		"; cd " + worktree + " 2>/dev/null || cd " + home +
		`; TASK="$1"; exec kimi -p "$TASK" --output-format stream-json -y`
	argv := []string{"sh", "-c", script, "sh", e.Task}

	ns := ids.WorkloadNamespace(t.Name)
	lines := make(chan string, 64)
	streamErr := make(chan error, 1)
	go func() {
		streamErr <- h.runKimiStream(ctx, ns, sess.Status.PodName, argv, lines)
	}()

	var full strings.Builder
	tick := time.NewTicker(askHeartbeat)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			// Client gone or the cap hit. Either way the other end is no
			// longer reading, and the exec dies with the context.
			return
		case <-tick.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case line, ok := <-lines:
			if !ok {
				err := <-streamErr
				if err != nil && full.Len() == 0 {
					fail(fmt.Errorf("kimi 没有跑完:%w", err))
					return
				}
				text := full.String()
				// Post BEFORE done: the board is the copy of record, and a
				// client that got "done" for an answer the board never shows
				// would have the only copy of it. A fresh context because the
				// request's may already be ending.
				post := acv1.Post{
					Kind: acv1.PostAgent, Author: t.Name, Cell: t.Name,
					Session: sess.Name, Body: text,
				}
				pctx, pcancel := context.WithTimeout(context.Background(), 15*time.Second)
				perr := h.appendPost(pctx, t.Name, &post)
				pcancel()
				if perr != nil {
					fail(fmt.Errorf("回答没能写到黑板:%w", perr))
					return
				}
				emit(map[string]any{"t": "done", "text": text})
				return
			}
			if frag, ok := extractStreamText(line); ok {
				full.WriteString(frag)
				emit(map[string]any{"t": "delta", "text": frag})
			}
		}
	}
}

// waitBoardRuntime waits until the project's board session has a live
// runtime pod whose CLI state directory is ready.
//
// The config.toml check is the real readiness signal: the window-open flow
// writes it (and the credentials symlink) when the session's terminal is set
// up, so its presence means the exec below needs nothing further injected.
func (h *Handler) waitBoardRuntime(ctx context.Context, cell string) (*acv1.Session, error) {
	deadline := time.Now().Add(askReadyDeadline)
	for {
		sess, err := h.liveBoardSession(ctx, cell)
		if err == nil && sess != nil {
			if sess.Spec.Runner != "" && sess.Spec.Runner != "kimi" {
				// Said as soon as it is knowable — waiting two minutes to
				// repeat it would not make it more true.
				return nil, fmt.Errorf("这个 runner(%s)还不支持流式回答", sess.Spec.Runner)
			}
			pod := sess.Status.PodName
			id := sess.Status.SessionID
			if pod != "" && id != "" {
				uid, uerr := runtimeUID(pod)
				if uerr == nil && h.podReady(ctx, ids.WorkloadNamespace(cell), pod) {
					state := ids.SessionStateDir(uid, id)
					if _, xerr := h.execInPod(ctx, ids.WorkloadNamespace(cell), pod,
						[]string{"sh", "-c", "test -f " + state + "/config.toml"}); xerr == nil {
						return sess, nil
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("等不到这个项目的会话就绪——它可能没派起来,去工作区看一眼")
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("等会话就绪时被取消了")
		case <-time.After(2 * time.Second):
		}
	}
}

// podReady reports whether the named pod is running and ready, using the
// direct clientset rather than the cached client: a pod that became ready a
// second ago is exactly the case being polled for, and a cache lag here is
// latency on every ask.
func (h *Handler) podReady(ctx context.Context, ns, name string) bool {
	if h.Kube == nil {
		return false
	}
	p, err := h.Kube.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil || p.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// runtimeUID recovers the owner's uid from their runtime pod's name. The pod
// name IS the encoding (ids.UserRuntimePod), and the uid is what every path
// under /workspace/users is derived from.
func runtimeUID(pod string) (int64, error) {
	s, ok := strings.CutPrefix(pod, "runtime-")
	if !ok {
		return 0, fmt.Errorf("会话的运行时 %q 不是用户 runtime,流不了", pod)
	}
	return strconv.ParseInt(s, 10, 64)
}

// runKimiStream execs the CLI and feeds its stdout to lines, one channel
// send per line, closing lines when the stream ends. Stderr is collected and
// attached to the returned error — it is where a failed CLI says why.
func (h *Handler) runKimiStream(ctx context.Context, ns, pod string, argv []string, lines chan<- string) error {
	defer close(lines)
	container, err := firstContainer(ctx, h.Kube, ns, pod)
	if err != nil {
		return err
	}
	req := h.Kube.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(ns).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   argv,
			Stdout:    true, Stderr: true,
		}, scheme.ParameterCodec)
	exe, err := remotecommand.NewSPDYExecutor(h.RESTConfig, http.MethodPost, req.URL())
	if err != nil {
		return err
	}
	pr, pw := io.Pipe()
	var errBuf bytes.Buffer
	go func() {
		err := exe.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: pw, Stderr: &errBuf})
		// Closing the pipe with the exec's error delivers it to the scanner
		// below — one channel for both data and failure, in order.
		_ = pw.CloseWithError(err)
	}()
	sc := bufio.NewScanner(pr)
	// stream-json lines carry whole content blocks; the default 64KB token
	// limit would silently truncate a long one.
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		select {
		case lines <- sc.Text():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	err = sc.Err()
	if err == nil && errBuf.Len() > 0 {
		err = fmt.Errorf("%s", strings.TrimSpace(errBuf.String()))
	}
	return err
}

// extractStreamText pulls one piece of assistant text out of a stream-json
// line.
//
// The CLI's stream-json schema is not documented, so this is deliberately
// lenient: it recognises the shapes assistant text has been seen in and
// skips everything else. Two rules keep the leniency honest: a line with a
// type that is clearly not assistant text (the meta line, tool calls,
// thinking) is dropped rather than leaked into the answer, and a line no
// text could be found in is skipped, never emitted raw.
func extractStreamText(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return "", false
	}
	var m map[string]any
	if json.Unmarshal([]byte(line), &m) != nil {
		return "", false
	}
	if typ, _ := m["type"].(string); typ != "" {
		switch typ {
		case "assistant", "message", "text", "delta", "text_delta", "content_block_delta":
		default:
			return "", false
		}
	}
	// .message.content[].text — the message-wrapped shape.
	if msg, ok := m["message"].(map[string]any); ok {
		if s, ok := textFromContent(msg["content"]); ok {
			return s, true
		}
	}
	// .content, either a bare string or a block array.
	if s, ok := textFromContent(m["content"]); ok {
		return s, true
	}
	// .text — the flat shape.
	if s, ok := m["text"].(string); ok && s != "" {
		return s, true
	}
	// .delta.text, or .delta as a bare string.
	switch d := m["delta"].(type) {
	case map[string]any:
		if s, ok := d["text"].(string); ok && s != "" {
			return s, true
		}
	case string:
		if d != "" {
			return d, true
		}
	}
	return "", false
}

// textFromContent reads the text out of a content value that is either a
// string or an array of {type, text} blocks.
func textFromContent(v any) (string, bool) {
	switch c := v.(type) {
	case string:
		if c != "" {
			return c, true
		}
	case []any:
		var sb strings.Builder
		found := false
		for _, b := range c {
			if bm, ok := b.(map[string]any); ok {
				if s, ok := bm["text"].(string); ok {
					sb.WriteString(s)
					found = true
				}
			}
		}
		if found {
			return sb.String(), true
		}
	}
	return "", false
}
