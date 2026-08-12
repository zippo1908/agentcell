package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Preview and production content is untrusted and same-origin with the
// control plane, so SameSite cookies do not protect us: a cookie-
// authenticated write must prove it came from our own UI.
func TestCookieWritesRequireSameOrigin(t *testing.T) {
	auth := NewAuthenticator("tok")
	reached := false
	h := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	call := func(method, origin, referer, authz string, cookie bool) int {
		reached = false
		req := httptest.NewRequest(method, "http://celld.example/api/sessions/s/review", nil)
		req.Host = "celld.example"
		if cookie {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "tok"})
		}
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	t.Run("same-origin cookie POST succeeds", func(t *testing.T) {
		if got := call(http.MethodPost, "http://celld.example", "", "", true); got != 200 || !reached {
			t.Errorf("same-origin POST = %d (reached=%v), want 200", got, reached)
		}
	})
	t.Run("same-origin referer fallback succeeds", func(t *testing.T) {
		if got := call(http.MethodPost, "", "http://celld.example/reviews", "", true); got != 200 {
			t.Errorf("referer fallback = %d, want 200", got)
		}
	})
	t.Run("cross-origin cookie POST is refused", func(t *testing.T) {
		if got := call(http.MethodPost, "http://evil.example", "", "", true); got != 403 || reached {
			t.Errorf("cross-origin POST = %d (reached=%v), want 403", got, reached)
		}
	})
	t.Run("opaque origin (sandboxed preview) is refused", func(t *testing.T) {
		if got := call(http.MethodPost, "null", "", "", true); got != 403 || reached {
			t.Errorf("Origin: null POST = %d (reached=%v), want 403", got, reached)
		}
	})
	t.Run("missing origin and referer is refused", func(t *testing.T) {
		if got := call(http.MethodPost, "", "", "", true); got != 403 || reached {
			t.Errorf("no-provenance POST = %d (reached=%v), want 403", got, reached)
		}
	})
	t.Run("cookie GET is unaffected", func(t *testing.T) {
		if got := call(http.MethodGet, "", "", "", true); got != 200 {
			t.Errorf("cookie GET = %d, want 200", got)
		}
	})
	t.Run("bearer writes bypass the browser check", func(t *testing.T) {
		// A CLI/API caller cannot be impersonated by a browser, so no
		// Origin is required — this must not become collateral damage.
		if got := call(http.MethodPost, "", "", "Bearer tok", false); got != 200 {
			t.Errorf("bearer POST = %d, want 200", got)
		}
		if got := call(http.MethodDelete, "", "", "Bearer tok", false); got != 200 {
			t.Errorf("bearer DELETE = %d, want 200", got)
		}
	})
}

// Isolation comes from origin separation (ADR-0007), so the previewed app
// keeps same-origin powers over itself — what must stay denied is taking
// over the console page.
func TestUntrustedContentCSP(t *testing.T) {
	for _, forbidden := range []string{
		"allow-top-navigation",
		"allow-popups-to-escape-sandbox",
	} {
		if strings.Contains(untrustedContentCSP, forbidden) {
			t.Errorf("CSP must not grant %q: %s", forbidden, untrustedContentCSP)
		}
	}
	if !strings.HasPrefix(untrustedContentCSP, "sandbox ") {
		t.Errorf("CSP must be a sandbox directive, got %q", untrustedContentCSP)
	}
	// A previewed app must not be degraded: it needs its own origin powers.
	for _, required := range []string{"allow-scripts", "allow-same-origin", "allow-forms"} {
		if !strings.Contains(untrustedContentCSP, required) {
			t.Errorf("preview would degrade without %q: %s", required, untrustedContentCSP)
		}
	}
}

// The console origin must not serve untrusted content, and the preview
// origin must not expose the console API or SPA — that separation IS the
// security boundary.
func TestConsoleAndPreviewOriginsAreDisjoint(t *testing.T) {
	h := &Handler{Registry: nil}

	previewMux := h.PreviewRoutes()
	for _, p := range []string{"/api/cells", "/api/meta", "/reviews", "/cells"} {
		rec := httptest.NewRecorder()
		previewMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("preview origin exposes console path %s (200)", p)
		}
	}

	// The console mux must not proxy untrusted content any more.
	consoleMux := (&Handler{}).Routes()
	for _, p := range []string{"/preview/shop/", "/app/shop/"} {
		rec := httptest.NewRecorder()
		consoleMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("console origin still serves untrusted path %s (200)", p)
		}
	}
}

// The UI must be told an absolute, different origin for untrusted content.
func TestPreviewOriginIsAbsoluteAndDistinct(t *testing.T) {
	h := &Handler{PreviewPort: "8081"}
	req := httptest.NewRequest(http.MethodGet, "/api/meta", nil)
	req.Host = "console.example:8080"
	if got := h.previewOriginFor(req); got != "http://console.example:8081" {
		t.Errorf("derived preview origin = %q", got)
	}

	h = &Handler{PreviewOrigin: "https://preview.example.com/"}
	if got := h.previewOriginFor(req); got != "https://preview.example.com" {
		t.Errorf("configured preview origin = %q", got)
	}
}

// The policy must actually reach the client, and an upstream policy must
// not be able to displace ours (multiple CSP headers intersect).
func TestProxyStampsCSPOnUpstreamResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A hostile upstream trying to grant itself same-origin powers.
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-same-origin")
		_, _ = w.Write([]byte("<html>preview</html>"))
	}))
	defer upstream.Close()

	h := &Handler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/preview/shop/index.html", nil)
	h.proxyToURL(rec, req, upstream.URL, "/preview/shop")

	policies := rec.Result().Header.Values("Content-Security-Policy")
	found := false
	for _, p := range policies {
		if p == untrustedContentCSP {
			found = true
		}
	}
	if !found {
		t.Fatalf("our sandbox policy was not applied; got %v", policies)
	}
	if len(policies) < 2 {
		t.Error("upstream policy should be preserved alongside ours (intersection), not replaced")
	}
	if rec.Header().Get("Referrer-Policy") == "" {
		t.Error("expected a Referrer-Policy on untrusted content")
	}
}
