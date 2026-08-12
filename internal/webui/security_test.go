package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	h := &Handler{Registry: nil, Auth: NewAuthenticator("t")}

	previewMux := h.PreviewRoutes()
	for _, p := range []string{"/api/cells", "/api/meta", "/reviews", "/cells"} {
		rec := httptest.NewRecorder()
		previewMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("preview origin exposes console path %s (200)", p)
		}
	}

	// The console mux must not proxy untrusted content any more.
	consoleMux := (&Handler{Auth: NewAuthenticator("t")}).Routes()
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
	h := &Handler{PreviewPort: "8081", Auth: NewAuthenticator("t")}
	req := httptest.NewRequest(http.MethodGet, "/api/meta", nil)
	req.Host = "console.example:8080"
	if got := h.previewOriginFor(req); got != "http://console.example:8081" {
		t.Errorf("derived preview origin = %q", got)
	}

	h = &Handler{PreviewOrigin: "https://preview.example.com/", Auth: NewAuthenticator("t")}
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

	h := &Handler{Auth: NewAuthenticator("t")}
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

// The preview origin must reject the console credential, bind every ticket
// to one Cell AND zone AND host, and accept each ticket only once.
func TestPreviewOriginAuthorization(t *testing.T) {
	auth := NewAuthenticator("tok")
	served := false
	h := auth.PreviewMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	}))
	const host = "shop-dev.preview.example.com"

	req := func(path, reqHost string, mutate func(*http.Request)) *httptest.ResponseRecorder {
		served = false
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Host = reqHost
		if mutate != nil {
			mutate(r)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	t.Run("console cookie and bearer are both rejected", func(t *testing.T) {
		rec := req("/preview/shop/", host, func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "tok"})
		})
		if rec.Code != http.StatusUnauthorized || served {
			t.Errorf("console cookie granted access: %d", rec.Code)
		}
		rec = req("/preview/shop/", host, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer tok")
		})
		if rec.Code != http.StatusUnauthorized || served {
			t.Errorf("bearer granted access: %d", rec.Code)
		}
	})

	t.Run("ticket exchanges once for a zone-scoped session", func(t *testing.T) {
		tk := auth.MintPreviewTicket("shop", ZoneDev, host)
		rec := req("/preview/shop/?"+previewTicketQS+"="+tk, host, nil)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("exchange = %d, want 303", rec.Code)
		}
		var sc *http.Cookie
		for _, c := range rec.Result().Cookies() {
			if c.Name == previewCookieName("shop", ZoneDev) {
				sc = c
			}
		}
		if sc == nil || sc.Path != "/preview/shop/" || !sc.HttpOnly {
			t.Fatalf("session cookie wrong: %+v", sc)
		}
		// The cookie must outlive the short URL ticket, or the tab 401s.
		if sc.MaxAge < int(previewSessionTTL.Seconds()) {
			t.Errorf("cookie MaxAge %d shorter than the session it carries", sc.MaxAge)
		}
		if rec := req("/preview/shop/", host, func(r *http.Request) { r.AddCookie(sc) }); rec.Code != 200 {
			t.Errorf("session cookie did not authorize its own zone: %d", rec.Code)
		}
		// Replay of the same single-use ticket must fail.
		if rec := req("/preview/shop/?"+previewTicketQS+"="+tk, host, nil); rec.Code != http.StatusForbidden {
			t.Errorf("ticket replay accepted: %d", rec.Code)
		}
		// A dev session must not open prod.
		if rec := req("/app/shop/", "shop-prod.preview.example.com", func(r *http.Request) { r.AddCookie(sc) }); rec.Code == 200 {
			t.Error("dev session opened the production zone")
		}
	})

	t.Run("tickets are bound to cell, zone and host", func(t *testing.T) {
		devTicket := auth.MintPreviewTicket("shop", ZoneDev, host)
		// wrong zone
		if rec := req("/app/shop/?"+previewTicketQS+"="+devTicket, "shop-prod.preview.example.com", nil); rec.Code != http.StatusForbidden {
			t.Errorf("dev ticket opened prod: %d", rec.Code)
		}
		// wrong cell
		if rec := req("/preview/other/?"+previewTicketQS+"="+devTicket, host, nil); rec.Code != http.StatusForbidden {
			t.Errorf("cross-cell ticket accepted: %d", rec.Code)
		}
		// wrong host (captured ticket replayed elsewhere)
		if rec := req("/preview/shop/?"+previewTicketQS+"="+devTicket, "evil.example.com", nil); rec.Code != http.StatusForbidden {
			t.Errorf("ticket accepted on a foreign host: %d", rec.Code)
		}
	})

	t.Run("expired and foreign-signed tickets are refused", func(t *testing.T) {
		expired := auth.sign(ticket{
			kind: "t", cell: "shop", zone: ZoneDev, host: host,
			exp: time.Now().Add(-time.Second).Unix(), nonce: newNonce(),
		})
		if rec := req("/preview/shop/?"+previewTicketQS+"="+expired, host, nil); rec.Code != http.StatusForbidden {
			t.Errorf("validly-signed but expired ticket accepted: %d", rec.Code)
		}
		other := NewAuthenticator("different")
		foreign := other.MintPreviewTicket("shop", ZoneDev, host)
		if rec := req("/preview/shop/?"+previewTicketQS+"="+foreign, host, nil); rec.Code != http.StatusForbidden {
			t.Errorf("foreign-signed ticket accepted: %d", rec.Code)
		}
	})
}

// Dev and prod must not share an origin: the agent's unreviewed work would
// otherwise read, or install a service worker over, the released build.
func TestDevAndProdHostsDiffer(t *testing.T) {
	h := &Handler{PreviewDomain: "preview.example.com", Auth: NewAuthenticator("t")}
	r := httptest.NewRequest(http.MethodGet, "/api/cells", nil)
	r.Host = "console.example.com"
	dev := h.previewBaseFor(r, "shop", ZoneDev)
	prod := h.previewBaseFor(r, "shop", ZoneProd)
	if dev == prod {
		t.Fatalf("dev and prod share an origin: %s", dev)
	}
	if !strings.HasPrefix(dev, "http://shop-dev.") || !strings.HasPrefix(prod, "http://shop-prod.") {
		t.Errorf("unexpected zone hosts: %s / %s", dev, prod)
	}
	if h.previewBaseFor(r, "other", ZoneDev) == dev {
		t.Error("two cells share a dev origin")
	}
}

// The proxy must never hand platform credentials to repo-controlled code.
func TestProxyStripsPlatformCredentials(t *testing.T) {
	var got *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	req := httptest.NewRequest(http.MethodGet, "/preview/shop/index.html", nil)
	req.Header.Set("Authorization", "Bearer console-token")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "console-cookie"})
	req.AddCookie(&http.Cookie{Name: previewCookieName("shop", ZoneDev), Value: "preview-ticket"})
	req.AddCookie(&http.Cookie{Name: "casdoor_session", Value: "sso"})
	req.AddCookie(&http.Cookie{Name: "app_session", Value: "keep-me"})

	(&Handler{Auth: NewAuthenticator("t")}).proxyToURL(httptest.NewRecorder(), req, upstream.URL, "/preview/shop")
	if got == nil {
		t.Fatal("upstream never saw the request")
	}
	if got.Header.Get("Authorization") != "" {
		t.Error("Authorization forwarded to untrusted upstream")
	}
	for _, forbidden := range []string{sessionCookie, previewCookieName("shop", ZoneDev), "casdoor_session"} {
		if c, err := got.Cookie(forbidden); err == nil {
			t.Errorf("platform cookie %s leaked to upstream (value %q)", forbidden, c.Value)
		}
	}
	// The previewed application keeps its own cookies.
	if c, err := got.Cookie("app_session"); err != nil || c.Value != "keep-me" {
		t.Error("the app's own cookie must be preserved")
	}
}

// A client-supplied X-Forwarded-* must not be able to redefine our own
// origin — otherwise the same-origin check can simply be told to agree.
func TestForwardedHeadersAreNotTrustedByDefault(t *testing.T) {
	auth := NewAuthenticator("tok")
	reached := false
	h := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	// The attacker claims we are evil.example so their Origin matches.
	r := httptest.NewRequest(http.MethodPost, "http://celld.example/api/x", nil)
	r.Host = "celld.example"
	r.Header.Set("X-Forwarded-Host", "evil.example")
	r.Header.Set("Origin", "http://evil.example")
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "tok"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden || reached {
		t.Errorf("spoofed X-Forwarded-Host defeated the origin check: %d", rec.Code)
	}

	// Behind a gateway that overwrites them, they are honoured.
	trusting := NewAuthenticator("tok")
	trusting.TrustForwardedHeaders = true
	h2 := trusting.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r2 := httptest.NewRequest(http.MethodPost, "http://internal:8080/api/x", nil)
	r2.Host = "internal:8080"
	r2.Header.Set("X-Forwarded-Proto", "https")
	r2.Header.Set("X-Forwarded-Host", "console.example.com")
	r2.Header.Set("Origin", "https://console.example.com")
	r2.AddCookie(&http.Cookie{Name: sessionCookie, Value: "tok"})
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, r2)
	if rec2.Code != http.StatusOK {
		t.Errorf("trusted proxy origin rejected: %d", rec2.Code)
	}
}

// Over TLS the console cookie carries the __Host- prefix, which the browser
// enforces as host-only + Secure + Path=/ — the defence against a sibling
// subdomain tossing a same-named cookie up to the console.
func TestConsoleCookieUsesHostPrefixOverTLS(t *testing.T) {
	auth := NewAuthenticator("tok")
	auth.TrustForwardedHeaders = true
	mux := http.NewServeMux()
	auth.LoginRoutes(mux)

	login := func(proto string) *http.Cookie {
		r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("token=tok"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Host = "console.example.com"
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		cookies := rec.Result().Cookies()
		if len(cookies) == 0 {
			t.Fatalf("login set no cookie (status %d)", rec.Code)
		}
		return cookies[0]
	}

	c := login("https")
	if !strings.HasPrefix(c.Name, "__Host-") {
		t.Errorf("TLS cookie name = %q, want __Host- prefix", c.Name)
	}
	if !c.Secure || c.Path != "/" || c.Domain != "" {
		t.Errorf("__Host- requires Secure, Path=/ and no Domain: %+v", c)
	}
	// Plain HTTP (dev port-forward) cannot use the prefix.
	if c := login(""); strings.HasPrefix(c.Name, "__Host-") {
		t.Error("__Host- used without TLS; the browser would reject it")
	}
	// Either name is accepted on the way in.
	r := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-" + sessionCookie, Value: "tok"})
	if tok, viaCookie := credential(r); tok != "tok" || !viaCookie {
		t.Error("__Host- cookie not recognised on input")
	}
}
