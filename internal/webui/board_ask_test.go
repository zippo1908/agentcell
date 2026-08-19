package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
)

func TestAskRegistryExpiry(t *testing.T) {
	var r askRegistry
	id := r.put(askEntry{Cell: "shop", Task: "把卡片改成两列", Asker: "u-a"})
	if id == "" {
		t.Fatal("put returned no id")
	}
	if _, ok := r.get(id); !ok {
		t.Fatal("a fresh ask was not found")
	}
	// Age the entry past the TTL; the next lookup must both refuse it and
	// sweep it.
	r.mu.Lock()
	e := r.m[id]
	e.Created = time.Now().Add(-2 * askTTL)
	r.m[id] = e
	r.mu.Unlock()
	if _, ok := r.get(id); ok {
		t.Fatal("an expired ask was still answered")
	}
	r.mu.Lock()
	n := len(r.m)
	r.mu.Unlock()
	if n != 0 {
		t.Fatalf("expired entry was not swept, %d left", n)
	}
	// Two asks never share an id.
	if r.put(askEntry{Cell: "shop"}) == r.put(askEntry{Cell: "shop"}) {
		t.Fatal("ask ids collided")
	}
}

func TestExtractStreamText(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
		ok   bool
	}{
		// The known first line of a real stream: meta, not answer text.
		{"meta line", `{"role":"meta","type":"system.version","version":"1.0"}`, "", false},
		{"assistant delta", `{"type":"delta","delta":{"text":"你好"}}`, "你好", true},
		{"bare delta text", `{"type":"text_delta","delta":"世界"}`, "世界", true},
		{"flat text", `{"type":"assistant","text":"一整段"}`, "一整段", true},
		{"message content blocks", `{"type":"message","message":{"content":[{"type":"text","text":"把卡片"},{"type":"text","text":"改成两列"}]}}`, "把卡片改成两列", true},
		{"bare content string", `{"type":"assistant","content":"直接一段"}`, "直接一段", true},
		{"tool call", `{"type":"tool_use","name":"Bash","input":{"command":"ls"}}`, "", false},
		{"thinking", `{"type":"thinking","thinking":"让我想想"}`, "", false},
		{"no type, has text", `{"text":"没类型也有文本"}`, "没类型也有文本", true},
		{"not json", `this is not json`, "", false},
		{"empty line", ``, "", false},
		{"json without text", `{"role":"assistant"}`, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := extractStreamText(c.line)
			if ok != c.ok || got != c.want {
				t.Errorf("extractStreamText(%q) = %q, %v; want %q, %v", c.line, got, ok, c.want, c.ok)
			}
		})
	}
}

// A board session on a non-kimi runner must refuse the stream immediately,
// as an SSE error event — not by hanging until the readiness deadline.
func TestBoardAskNonKimiRunner(t *testing.T) {
	cell := &acv1.Cell{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "shop"}}
	sess := &acv1.Session{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "sess-board"}}
	sess.Spec.Cell = "shop"
	sess.Spec.Board = "shop"
	sess.Spec.Runner = "claude"
	sess.Status.Phase = acv1.SessionRunning
	h := &Handler{
		Client:    fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(cell, sess).Build(),
		Namespace: ns,
	}
	askID := h.asks.put(askEntry{Cell: "shop", Task: "把卡片改成两列", Asker: "u-a"})

	req := httptest.NewRequest(http.MethodGet, "/api/cells/shop/board/ask/"+askID, nil)
	req.SetPathValue("cell", "shop")
	req.SetPathValue("ask", askID)
	rec := httptest.NewRecorder()
	h.boardAsk(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (SSE refuses inside the stream)", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"t":"error"`) || !strings.Contains(body, "还不支持流式回答") {
		t.Fatalf("expected an error event about the runner, got:\n%s", body)
	}
	if strings.Contains(body, `"t":"done"`) {
		t.Fatalf("a refused ask must not end with done, got:\n%s", body)
	}
}

// An ask id that was never registered is a 404, indistinguishable from one
// that expired.
func TestBoardAskUnknownIs404(t *testing.T) {
	cell := &acv1.Cell{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "shop"}}
	h := &Handler{
		Client:    fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(cell).Build(),
		Namespace: ns,
	}
	req := httptest.NewRequest(http.MethodGet, "/api/cells/shop/board/ask/nope", nil)
	req.SetPathValue("cell", "shop")
	req.SetPathValue("ask", "nope")
	rec := httptest.NewRecorder()
	h.boardAsk(rec, req)
	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
