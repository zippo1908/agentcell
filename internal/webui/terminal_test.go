package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/tools/remotecommand"
)

// A terminal must survive the one header a websocket proxy actually sends.
//
// Found on the cluster, not here: terminals worked on a port-forward and
// failed behind the ingress with a bare 403, for every session, forever.
// Traefik sets X-Forwarded-Proto to "ws" on an upgrade — an accurate
// description of the connection — so the console reconstructed its own
// origin as "ws://host" and compared it against the browser's
// "http://host". A browser's Origin is never ws://, so the check could not
// have passed no matter what anybody clicked.
func TestUpgradeAcceptsBrowserOriginBehindWebsocketProxy(t *testing.T) {
	h := &Handler{Auth: &Authenticator{TrustForwardedHeaders: true}}
	check := h.upgrader().CheckOrigin

	r := httptest.NewRequest(http.MethodGet, "/api/sessions/s1/terminal", nil)
	r.Host = "console.example.com"
	r.Header.Set("Origin", "http://console.example.com")
	r.Header.Set("X-Forwarded-Host", "console.example.com")
	r.Header.Set("X-Forwarded-Proto", "ws")
	if !check(r) {
		t.Error("a same-origin terminal was refused because the proxy said ws://")
	}

	r.Header.Set("X-Forwarded-Proto", "wss")
	r.Header.Set("Origin", "https://console.example.com")
	if !check(r) {
		t.Error("wss:// was not normalized to https://")
	}

	// The check must still do its job: another origin stays out.
	r.Header.Set("Origin", "https://evil.example.com")
	if check(r) {
		t.Error("a cross-origin terminal was allowed")
	}
}

// An operator who said "do not trust proxy headers" must be obeyed here
// too. This endpoint hands over a keyboard; it is the last place that
// should quietly take a stranger's word for what host it is serving.
func TestUpgradeIgnoresForwardedHeadersWhenUntrusted(t *testing.T) {
	h := &Handler{Auth: &Authenticator{}} // TrustForwardedHeaders: false
	r := httptest.NewRequest(http.MethodGet, "/api/sessions/s1/terminal", nil)
	r.Host = "console.example.com"
	r.Header.Set("Origin", "http://attacker.example.com")
	r.Header.Set("X-Forwarded-Host", "attacker.example.com")
	if h.upgrader().CheckOrigin(r) {
		t.Error("a spoofed X-Forwarded-Host was honoured")
	}
}

// The browser must be told when the attach actually succeeded.
//
// The websocket is accepted before the attach is attempted, so onopen says
// nothing about whether there is a tty on the other end. Without an
// explicit signal a client resets its backoff on every failed attempt and
// reconnects forever — which is exactly what "会话在休眠,正在唤醒" showing
// for minutes on end was.
func TestFirstOutputAnnouncesAttached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		term := &wsTerminal{conn: c, sizes: make(chan remotecommand.TerminalSize, 1)}
		if _, err := term.Write([]byte("hello")); err != nil {
			t.Error(err)
		}
		if _, err := term.Write([]byte(" again")); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()

	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	typ, data, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.TextMessage || !strings.Contains(string(data), `"ready"`) {
		t.Fatalf("first frame = %d %q, want a text ready frame", typ, data)
	}

	// Screen output stays binary, so a control message can never be
	// mistaken for something the agent printed.
	typ, data, err = c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.BinaryMessage || string(data) != "hello" {
		t.Fatalf("second frame = %d %q, want binary output", typ, data)
	}

	// And it is announced exactly once, not before every write.
	typ, data, err = c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.BinaryMessage || string(data) != " again" {
		t.Fatalf("third frame = %d %q, want the next chunk of output", typ, data)
	}
}
