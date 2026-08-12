package webui

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Authenticator gates the HTTP surface with static bearer tokens. Tokens
// are loaded from a mounted Secret (one per line / whitespace-separated),
// so multiple values are accepted at once to allow rotation without
// downtime. This is deliberately the simplest thing that removes the
// "anyone who reaches celld can dispatch/release" hole; OIDC/user accounts
// are a later layer that can wrap or replace this.
type Authenticator struct {
	tokens map[string]struct{}
	// TrustForwardedHeaders enables X-Forwarded-Proto/Host. Off by default:
	// if celld is reachable without the proxy — or the proxy forwards
	// client-supplied values instead of overwriting them — an attacker
	// could otherwise dictate what we believe our own origin is and walk
	// straight through the same-origin check. Turn it on only behind a
	// gateway that OVERWRITES these headers (e.g. APISIX must set, not
	// append, X-Forwarded-*).
	TrustForwardedHeaders bool
}

// NewAuthenticator builds an authenticator from raw token material. An
// empty set means auth is disabled — the caller decides whether to allow
// that (dev) or refuse to start (prod).
func NewAuthenticator(raw string) *Authenticator {
	tokens := map[string]struct{}{}
	for _, t := range strings.Fields(raw) {
		if t != "" {
			tokens[t] = struct{}{}
		}
	}
	return &Authenticator{tokens: tokens}
}

// sortedTokens returns the configured tokens in a stable order so derived
// keys (e.g. the preview signing key) do not change between restarts.
func (a *Authenticator) sortedTokens() []string {
	out := make([]string, 0, len(a.tokens))
	for t := range a.tokens {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Enabled reports whether any token is configured.
func (a *Authenticator) Enabled() bool { return len(a.tokens) > 0 }

// forwarded returns the header value only if forwarded headers are trusted.
func (a *Authenticator) forwarded(r *http.Request, name string) string {
	if a == nil || !a.TrustForwardedHeaders {
		return ""
	}
	return r.Header.Get(name)
}

// valid does a constant-time comparison against every configured token so
// a match doesn't leak timing about which token (or how many) exist.
func (a *Authenticator) valid(presented string) bool {
	ok := false
	pb := []byte(presented)
	for t := range a.tokens {
		if subtle.ConstantTimeCompare(pb, []byte(t)) == 1 {
			ok = true
		}
	}
	return ok
}

// sessionCookie carries a token for the browser UI, which cannot attach an
// Authorization header to navigations, iframes and asset loads. The value
// is one of the same static tokens, so the cookie grants exactly the API
// bearer's rights and nothing more.
const sessionCookie = "agentcell_token"

// credential extracts the presented token and how it was presented. The
// distinction matters: a bearer header is only ever sent deliberately by an
// API/CLI caller, while a cookie rides along on any request the browser
// makes — including one initiated by untrusted preview content.
func credential(r *http.Request) (token string, viaCookie bool) {
	const p = "Bearer "
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, p) {
		return strings.TrimSpace(h[len(p):]), false
	}
	for _, name := range []string{"__Host-" + sessionCookie, sessionCookie} {
		if c, err := r.Cookie(name); err == nil {
			return c.Value, true
		}
	}
	return "", false
}

// unsafeMethod reports whether a request can change state.
func unsafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// sameOriginRequest verifies a state-changing, cookie-authenticated request
// actually originated from the AgentCell UI.
//
// This is load-bearing: preview and production content is untrusted (repo-
// and agent-authored) and is served from *this same origin* under /preview
// and /app. SameSite cookies therefore do not help at all — such a request
// is same-site by construction. Without this check, a malicious preview
// page could call dispatch/review/release with the operator's cookie.
//
// Bearer callers are exempt: a token in a header cannot be attached by a
// browser on someone else's behalf.
func (a *Authenticator) sameOriginRequest(r *http.Request) bool {
	self := a.requestOrigin(r)
	if o := r.Header.Get("Origin"); o != "" {
		// "null" is what a sandboxed (opaque-origin) document sends —
		// exactly the preview case we are defending against.
		return o != "null" && o == self
	}
	// No Origin: fall back to Referer, which browsers send on same-origin
	// form posts. Absent both, refuse — we cannot establish provenance.
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return false
		}
		return u.Scheme+"://"+u.Host == self
	}
	return false
}

// requestOrigin reconstructs this server's own origin as the browser sees
// it, honouring a trusted reverse proxy's forwarded headers.
func (a *Authenticator) requestOrigin(r *http.Request) string { //nolint:revive // nil-safe by design
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := a.forwarded(r, "X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	host := r.Host
	if h := a.forwarded(r, "X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}

// secureRequest reports whether the browser reached us over TLS.
func (a *Authenticator) secureRequest(r *http.Request) bool {
	return strings.HasPrefix(a.requestOrigin(r), "https://")
}

// consoleCookieName uses the __Host- prefix over TLS. The prefix is a
// browser-enforced guarantee that the cookie is host-only, Secure and
// Path=/ — which also means a sibling subdomain cannot "toss" a cookie of
// the same name up to us. Plain HTTP (dev port-forward) cannot use it.
func consoleCookieName(secure bool) string {
	if secure {
		return "__Host-" + sessionCookie
	}
	return sessionCookie
}

// Middleware wraps a handler, requiring a valid token on every request
// except health and the login endpoint. When no token is configured it is
// a pass-through — celld logs a warning at startup in that case.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Enabled() || r.URL.Path == "/healthz" || r.URL.Path == "/login" {
			next.ServeHTTP(w, r)
			return
		}
		token, viaCookie := credential(r)
		if !a.valid(token) {
			// A browser navigation gets redirected to the login form; an
			// API/CLI caller gets a clean 401.
			if wantsHTML(r) {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// CSRF: a valid cookie is not enough for a state-changing request —
		// it must also demonstrably come from our own UI.
		if viaCookie && unsafeMethod(r.Method) && !a.sameOriginRequest(r) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func wantsHTML(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html")
}

// LoginRoutes serves a minimal token-entry form that sets the session
// cookie, so the browser UI works without a proxy injecting headers.
func (a *Authenticator) LoginRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(loginHTML)
	})
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimSpace(r.FormValue("token"))
		if !a.valid(tok) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write(loginHTML)
			return
		}
		// Secure whenever the browser reached us over TLS (directly or via a
		// trusted proxy); a plain-HTTP port-forward still works for dev.
		secure := a.secureRequest(r)
		http.SetCookie(w, &http.Cookie{
			Name: consoleCookieName(secure), Value: tok, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
			// No Domain: host-only, so a subdomain cannot receive it.
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
}

var loginHTML = []byte(`<!doctype html><meta charset=utf-8>
<title>AgentCell — 登录</title>
<style>body{font:15px system-ui;display:grid;place-items:center;height:100vh;margin:0;background:#171a18;color:#e6e9e7}
form{display:flex;gap:8px;flex-direction:column;width:300px}
input,button{padding:9px;border-radius:6px;border:1px solid #333;font:inherit}
button{background:#58a17b;color:#10140f;border:0;cursor:pointer}</style>
<form method=post action=/login>
<h2>AgentCell</h2>
<input name=token type=password placeholder="访问令牌 (access token)" autofocus>
<button>登录</button>
</form>`)
