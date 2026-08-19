package webui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zippo1908/agentcell/pkg/ids"
)

// Prewarming the board's conversation, and saying early what would fail.
//
// Two pains share one endpoint. The first: an @机器人 ask against a sleeping
// session waits out a pod rebuild behind a waiting bubble — so opening the
// board wakes the session, and the rebuild happens while the person reads.
// The second is worse: a dead credential used to surface only AFTER the ask,
// as "kimi 没有跑完" — the person did everything right and the thing still
// would not work. So once the session is warm, a one-prompt probe answers
// "would an ask have worked right now?", and the board can say what to fix
// before anybody asks.
//
// The probe is a real (tiny) model call because nothing cheaper tells the
// truth: `kimi doctor` validates config syntax, `provider list` reads the
// same files, and neither touches the grant. A cached result keeps the cost
// at one ping per TTL per session.

const (
	// prewarmProbeTTL is how long a probe result is trusted. The probe costs
	// a model call; anything shorter charges real money for page refreshes.
	prewarmProbeTTL = 10 * time.Minute
	// prewarmProbeTimeout caps the probe itself. A probe slower than this is
	// indistinguishable from a hung CLI, and "unknown" is its honest answer.
	prewarmProbeTimeout = 45 * time.Second
)

type prewarmView struct {
	// Session: "none" (never asked), "warming" (waking or coming up),
	// "ready" (an ask could start now).
	Session string `json:"session"`
	// Credential: "ok", "invalid", "missing", "unknown" — only meaningful
	// when Session is ready.
	Credential string `json:"credential,omitempty"`
	Message    string `json:"message,omitempty"`
}

// probeCache remembers one probe verdict per session. In memory on purpose:
// same single-replica seam as the ask registry.
type probeCache struct {
	mu sync.Mutex
	m  map[string]probeVerdict
}

type probeVerdict struct {
	result string
	at     time.Time
}

func (c *probeCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	if !ok || time.Since(v.at) > prewarmProbeTTL {
		return "", false
	}
	return v.result, true
}

func (c *probeCache) put(key, result string) {
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]probeVerdict{}
	}
	c.m[key] = probeVerdict{result: result, at: time.Now()}
	c.mu.Unlock()
}

// boardPrewarm wakes the project's board session if it sleeps, and reports
// how far the warm-up got — ending, once warm, in a credential probe.
func (h *Handler) boardPrewarm(w http.ResponseWriter, r *http.Request) {
	t, _, ok := h.boardFor(w, r)
	if !ok {
		return
	}
	sess, err := h.liveBoardSession(r.Context(), t.Name)
	if err != nil || sess == nil {
		writeJSON(w, 200, prewarmView{Session: "none",
			Message: "这个项目还没有共享会话——@机器人 问一句就会建,第一次会慢一些"})
		return
	}
	if woke, err := h.wakeIfDormant(r, sess); err != nil {
		writeErr(w, 500, err)
		return
	} else if woke {
		writeJSON(w, 200, prewarmView{Session: "warming"})
		return
	}

	ns := ids.WorkloadNamespace(t.Name)
	uid, uerr := runtimeUID(sess.Status.PodName)
	if sess.Status.PodName == "" || uerr != nil || !h.podReady(r.Context(), ns, sess.Status.PodName) {
		writeJSON(w, 200, prewarmView{Session: "warming"})
		return
	}
	state := ids.SessionStateDir(uid, sess.Status.SessionID)
	if _, err := h.execInPod(r.Context(), ns, sess.Status.PodName,
		[]string{"sh", "-c", "test -f " + state + "/config.toml"}); err != nil {
		writeJSON(w, 200, prewarmView{Session: "warming"})
		return
	}

	if sess.Spec.Runner != "" && sess.Spec.Runner != "kimi" {
		writeJSON(w, 200, prewarmView{Session: "ready", Credential: "unknown",
			Message: fmt.Sprintf("这个 runner(%s)的凭据探不了,直接问一句试试", sess.Spec.Runner)})
		return
	}

	key := t.Name + "|" + sess.Status.SessionID
	verdict, cached := h.probes.get(key)
	if !cached {
		verdict = h.probeKimi(r.Context(), ns, sess.Status.PodName, ids.UserHome(uid)+"/home", state)
		h.probes.put(key, verdict)
	}
	v := prewarmView{Session: "ready", Credential: verdict}
	switch verdict {
	case "invalid":
		v.Message = "Kimi 授权已失效——到「凭据」页断开再重连一次,@机器人 才能回答"
	case "missing":
		v.Message = "还没有连接 Kimi 账号——到「凭据」页连一次,或者加一把模型 key"
	case "unknown":
		v.Message = "凭据状态没探明白,可以先问一句试试"
	}
	writeJSON(w, 200, v)
}

// probeKimi spends one tiny prompt to learn whether an ask would authenticate.
// Classified by what the CLI says, not by exit status alone: a nonzero exit
// with no recognisable reason is "unknown", never a false alarm.
func (h *Handler) probeKimi(ctx context.Context, ns, pod, home, state string) string {
	pctx, cancel := context.WithTimeout(ctx, prewarmProbeTimeout)
	defer cancel()
	out, err := h.execInPod(pctx, ns, pod, []string{"sh", "-c",
		"export HOME=" + home + " KIMI_CODE_HOME=" + state + "\n" +
			`[ -s "$KIMI_CODE_HOME/credentials/kimi-code.json" ] || echo AGENTCELL_NOFILE` + "\n" +
			"cd " + home + "; kimi -p ping --output-format stream-json 2>&1 | head -c 2048"})
	blob := out + " " + fmt.Sprint(err)
	return classifyProbe(blob)
}

// classifyProbe reads the probe's combined output. Verdicts come only from
// sentences the CLI is known to say — a nonzero exit with no recognisable
// reason is "unknown", never a false alarm.
func classifyProbe(blob string) string {
	nofile := strings.Contains(blob, "AGENTCELL_NOFILE")
	switch {
	case strings.Contains(blob, "authorization grant is invalid"),
		strings.Contains(blob, "Invalid Authentication"):
		return "invalid"
	case strings.Contains(blob, "no credential configured"):
		// The oauth file never existed (nobody connected) versus a connected
		// account whose grant died — the fix is the same page, but the words
		// are not, and "失效" to someone who never connected is confusing.
		if nofile {
			return "missing"
		}
		return "invalid"
	case strings.Contains(blob, `"role":"meta"`):
		return "ok"
	default:
		return "unknown"
	}
}
