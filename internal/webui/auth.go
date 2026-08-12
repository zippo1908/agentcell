package webui

import (
	"crypto/subtle"
	"net/http"
	"net/url"
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

// Enabled reports whether any token is configured.
func (a *Authenticator) Enabled() bool { return len(a.tokens) > 0 }

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
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value, true
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
func sameOriginRequest(r *http.Request) bool {
	self := requestOrigin(r)
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
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
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
		if viaCookie && unsafeMethod(r.Method) && !sameOriginRequest(r) {
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
		http.SetCookie(w, &http.Cookie{
			Name: sessionCookie, Value: tok, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteLaxMode,
			Secure: strings.HasPrefix(requestOrigin(r), "https://"),
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
