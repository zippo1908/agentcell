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

// Untrusted preview/production responses must carry a sandbox CSP with an
// opaque origin, whether they are framed or opened directly.
func TestUntrustedContentCSP(t *testing.T) {
	for _, forbidden := range []string{
		"allow-same-origin",
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
	if !strings.Contains(untrustedContentCSP, "allow-scripts") {
		t.Error("preview needs allow-scripts to be useful")
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
