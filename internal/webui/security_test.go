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

// The preview origin must reject the console credential outright and only
// honour a short-lived ticket for the exact Cell being viewed — otherwise
// one Cell's untrusted content could browse another's (they may share a
// host) or ride the operator's platform-wide cookie.
func TestPreviewOriginRejectsConsoleCredential(t *testing.T) {
	auth := NewAuthenticator("tok")
	served := false
	h := auth.PreviewMiddleware(CellFromPreviewRequest,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			served = true
			w.WriteHeader(http.StatusOK)
		}))

	req := func(path string, mutate func(*http.Request)) *httptest.ResponseRecorder {
		served = false
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if mutate != nil {
			mutate(r)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	t.Run("console cookie is not accepted", func(t *testing.T) {
		rec := req("/preview/shop/", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "tok"})
		})
		if rec.Code != http.StatusUnauthorized || served {
			t.Errorf("console cookie granted preview access: %d (served=%v)", rec.Code, served)
		}
	})

	t.Run("bearer token is not accepted", func(t *testing.T) {
		rec := req("/preview/shop/", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer tok")
		})
		if rec.Code != http.StatusUnauthorized || served {
			t.Errorf("bearer granted preview access: %d", rec.Code)
		}
	})

	t.Run("valid ticket is exchanged for a scoped cookie", func(t *testing.T) {
		ticket := auth.MintPreviewTicket("shop")
		rec := req("/preview/shop/?"+previewTicketQS+"="+ticket, nil)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("ticket exchange = %d, want 303", rec.Code)
		}
		var scoped *http.Cookie
		for _, c := range rec.Result().Cookies() {
			if c.Name == previewCookieName("shop") && c.Path == "/preview/shop/" {
				scoped = c
			}
		}
		if scoped == nil {
			t.Fatal("no cookie scoped to this cell's path")
		}
		if !scoped.HttpOnly {
			t.Error("preview cookie must be HttpOnly")
		}
		// And it then authorizes the same cell.
		if rec := req("/preview/shop/", func(r *http.Request) { r.AddCookie(scoped) }); rec.Code != 200 {
			t.Errorf("scoped cookie did not authorize its own cell: %d", rec.Code)
		}
	})

	t.Run("a ticket for one cell does not open another", func(t *testing.T) {
		ticket := auth.MintPreviewTicket("shop")
		rec := req("/preview/other/?"+previewTicketQS+"="+ticket, nil)
		if rec.Code != http.StatusForbidden || served {
			t.Errorf("cross-cell ticket accepted: %d", rec.Code)
		}
		// Same for a cookie minted for another cell.
		rec = req("/preview/other/", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: previewCookieName("other"), Value: ticket})
		})
		if rec.Code != http.StatusUnauthorized || served {
			t.Errorf("cross-cell cookie accepted: %d", rec.Code)
		}
	})

	t.Run("forged and expired tickets are refused", func(t *testing.T) {
		if rec := req("/preview/shop/?"+previewTicketQS+"=shop:9999999999:forged", nil); rec.Code != http.StatusForbidden {
			t.Errorf("forged ticket = %d, want 403", rec.Code)
		}
		other := NewAuthenticator("different-token")
		if rec := req("/preview/shop/?"+previewTicketQS+"="+other.MintPreviewTicket("shop"), nil); rec.Code != http.StatusForbidden {
			t.Errorf("ticket signed with another key = %d, want 403", rec.Code)
		}
	})
}

// Each Cell gets its own host when a preview domain is configured — the
// only browser-level isolation between Cells.
func TestPerCellPreviewHost(t *testing.T) {
	h := &Handler{PreviewDomain: "preview.example.com", Auth: NewAuthenticator("t")}
	r := httptest.NewRequest(http.MethodGet, "/api/cells", nil)
	r.Host = "console.example.com"
	if got := h.previewBaseFor(r, "shop"); got != "http://shop.preview.example.com" {
		t.Errorf("per-cell host = %q", got)
	}
	if got := h.previewBaseFor(r, "other"); got == h.previewBaseFor(r, "shop") {
		t.Error("two cells share an origin despite a preview domain")
	}
	url := h.previewURL(r, "shop", "/preview/shop/")
	if !strings.Contains(url, "shop.preview.example.com") || !strings.Contains(url, previewTicketQS+"=") {
		t.Errorf("preview URL missing host or ticket: %s", url)
	}
}
