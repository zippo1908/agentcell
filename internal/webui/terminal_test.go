package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
