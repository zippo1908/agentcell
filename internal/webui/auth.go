package webui

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"html/template"
	"net/http"
	"net/url"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sort"
	"strings"

	"github.com/zippo1908/agentcell/internal/identity"
)

// Authenticator gates the HTTP surface with static bearer tokens. Tokens
// are loaded from a mounted Secret (one per line / whitespace-separated),
// so multiple values are accepted at once to allow rotation without
// downtime. This is deliberately the simplest thing that removes the
// "anyone who reaches celld can dispatch/release" hole; OIDC/user accounts
// are a later layer that can wrap or replace this.
type Authenticator struct {
	tokens map[string]struct{}
	// OIDC, when configured, is the real identity layer; static tokens are
	// demoted to break-glass and CI (ADR-0008).
	OIDC *identity.OIDC
	// keyMaterial seeds the preview ticket key alongside the tokens. It
	// exists because the tokens alone are not a safe seed once a deployment
	// can authenticate with OIDC and configure no tokens at all: the digest
	// of an empty token set is a publicly derivable constant, and preview
	// tickets signed with it would be forgeable by anyone.
	keyMaterial []byte
	// TrustForwardedHeaders enables X-Forwarded-Proto/Host. Off by default:
	// if celld is reachable without the proxy — or the proxy forwards
	// client-supplied values instead of overwriting them — an attacker
	// could otherwise dictate what we believe our own origin is and walk
	// straight through the same-origin check. Turn it on only behind a
	// gateway that OVERWRITES these headers (e.g. APISIX must set, not
	// append, X-Forwarded-*).
	TrustForwardedHeaders bool
	// Accounts, when set, is what makes this deployment multi-person:
	// email logins, invitations, and a principal per human. Without it the
	// static token remains the only way in and everyone is one principal.
	Accounts *Accounts
	// tickets is the single-use guard. Shared through the API server when a
	// client is wired, because "used once" has to hold across replicas.
	tickets sharedTickets
}

// UseSharedTicketStore makes single-use tickets single-use across every
// celld replica rather than once per process.
func (a *Authenticator) UseSharedTicketStore(c client.Client, namespace string) {
	a.tickets.client, a.tickets.namespace = c, namespace
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
	return &Authenticator{tokens: tokens, keyMaterial: freshKeyMaterial()}
}

// freshKeyMaterial backs the preview ticket key when nothing else does. It
// is regenerated per process, so preview sessions do not survive a celld
// restart — a deliberate trade: a forgeable ticket is a security bug, a
// re-issued ticket is a redirect.
func freshKeyMaterial() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("webui: crypto/rand unavailable: " + err.Error())
	}
	return b
}

// SetKeyMaterial pins the preview signing seed to operator-provided bytes so
// tickets stay valid across restarts and across replicas.
// SessionKey derives the key that signs account session cookies.
//
// Separate from the preview key by domain string, so the same material can
// seed both without a ticket ever being usable as a session or the reverse
// — two credentials that mean very different things must not be
// interchangeable just because they share a secret.
func (a *Authenticator) SessionKey() []byte {
	h := sha256.New()
	_, _ = h.Write(a.keyMaterial)
	_, _ = h.Write([]byte{0})
	for _, t := range a.sortedTokens() {
		_, _ = h.Write([]byte(t))
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write([]byte("agentcell-session-v1"))
	return h.Sum(nil)
}

func (a *Authenticator) SetKeyMaterial(b []byte) {
	if len(b) > 0 {
		a.keyMaterial = b
	}
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

// Enabled reports whether the HTTP surface is gated at all — by static
// tokens, by an identity provider, or both.
func (a *Authenticator) Enabled() bool {
	return len(a.tokens) > 0 || a.OIDC.Configured()
}

// resolve turns a presented credential into a principal. An OIDC token is
// only ever accepted after full verification; a static token resolves to the
// single shared break-glass identity.
func (a *Authenticator) resolve(r *http.Request, presented string) (identity.Principal, bool) {
	if a.OIDC.Configured() && identity.LooksLikeJWT(presented) {
		p, err := a.OIDC.Verify(r.Context(), presented)
		if err != nil {
			return identity.Principal{}, false
		}
		return p, true
	}
	// An account session cookie, which is what a person logged in with an
	// email carries. Checked before the static token because the static
	// token is the break-glass path, not the normal one.
	if a.Accounts != nil {
		if p, ok := a.Accounts.FromCookie(r.Context(), presented); ok {
			return p, true
		}
	}
	if a.valid(presented) {
		// The bootstrap credential is an administrator by definition: it is
		// how an operator sets a deployment up before any person exists.
		p := identity.StaticToken
		p.Admin = true
		return p, true
	}
	return identity.Principal{}, false
}

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
		scheme = normalizeScheme(p)
	}
	host := r.Host
	if h := a.forwarded(r, "X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}

// normalizeScheme turns what a proxy reports into what a BROWSER would put
// in an Origin header.
//
// Traefik sets X-Forwarded-Proto to "ws"/"wss" on an upgrade request — a
// reasonable description of the connection, and the reason terminals worked
// on a port-forward and failed behind the ingress: we reconstructed our own
// origin as "ws://console…" and compared it against the browser's
// "http://console…", which can never match. A browser's Origin is always
// http or https, whatever protocol the connection later becomes.
//
// A comma-separated list is a chain of proxies; the first entry is what the
// browser actually spoke to.
func normalizeScheme(s string) string {
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "ws":
		return "http"
	case "wss":
		return "https"
	}
	return s
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
		if !a.Enabled() || r.URL.Path == "/healthz" ||
			strings.HasPrefix(r.URL.Path, "/login") || strings.HasPrefix(r.URL.Path, "/invite") {
			next.ServeHTTP(w, r)
			return
		}
		token, viaCookie := credential(r)
		principal, ok := a.resolve(r, token)
		if !ok {
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
		next.ServeHTTP(w, r.WithContext(identity.NewContext(r.Context(), principal)))
	})
}

func wantsHTML(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html")
}

// LoginRoutes serves a minimal token-entry form that sets the session
// cookie, so the browser UI works without a proxy injecting headers.
func (a *Authenticator) LoginRoutes(mux *http.ServeMux) {
	if a.OIDC.Configured() {
		mux.HandleFunc("GET /login", a.oidcStart)
		mux.HandleFunc("GET /auth/callback", a.oidcCallback)
		// The token form stays reachable at an explicit path so an operator
		// locked out by a broken IdP still has a way in.
		mux.HandleFunc("GET /login/token", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(loginHTML)
		})
		mux.HandleFunc("POST /login/token", a.tokenLogin)
		return
	}
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(loginPageHTML(""))
	})
	mux.HandleFunc("POST /login", a.accountLogin)
	mux.HandleFunc("POST /logout", a.logout)
	// The token form stays reachable so an operator whose account table is
	// empty — or whose own account is locked out — still has a way in.
	mux.HandleFunc("GET /login/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(loginHTML)
	})
	mux.HandleFunc("POST /login/token", a.tokenLogin)
}

// loginPageHTML renders the email form, with an optional message. Server
// rendered because it must work before any of the console's JavaScript —
// which lives behind auth — has been allowed to load.
func loginPageHTML(msg string) []byte {
	note := ""
	if msg != "" {
		note = `<p style="color:#e0776a;margin:0">` + template.HTMLEscapeString(msg) + `</p>`
	}
	return []byte(`<!doctype html><meta charset=utf-8>
<title>AgentCell — 登录</title>
<style>body{font:15px system-ui;display:grid;place-items:center;height:100vh;margin:0;background:#171a18;color:#e6e9e7}
form{display:flex;gap:8px;flex-direction:column;width:300px}
input,button{padding:9px;border-radius:6px;border:1px solid #333;font:inherit}
button{background:#58a17b;color:#10140f;border:0;cursor:pointer}
a{color:#8ab;font-size:13px}</style>
<form method=post action=/login>
<h2>AgentCell</h2>` + note + `
<input name=email type=email placeholder="邮箱" autofocus autocomplete=username>
<input name=password type=password placeholder="密码" autocomplete=current-password>
<button>登录</button>
<a href="/login/token">用访问令牌登录</a>
</form>`)
}

func (a *Authenticator) tokenLogin(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimSpace(r.FormValue("token"))
	if !a.valid(tok) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(loginHTML)
		return
	}
	a.setSessionCookie(w, r, tok, 0)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// setSessionCookie stores a credential for the browser UI, which cannot
// attach an Authorization header to navigations, iframes and asset loads.
func (a *Authenticator) setSessionCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	// Secure whenever the browser reached us over TLS (directly or via a
	// trusted proxy); a plain-HTTP port-forward still works for dev.
	secure := a.secureRequest(r)
	http.SetCookie(w, &http.Cookie{
		Name: consoleCookieName(secure), Value: value, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
		MaxAge: maxAge,
		// No Domain: host-only, so a subdomain cannot receive it.
	})
}

// oidcStateCookie holds the CSRF state and PKCE verifier between the
// redirect out and the callback back. Keeping it in a host-only, HttpOnly
// cookie rather than server memory means any celld replica can complete a
// flow another one started.
const oidcStateCookie = "agentcell_oidc_flow"

// oidcRedirectURL is where the provider sends the browser back. Derived from
// the console's own origin unless pinned, so a single value works for
// port-forward, ingress and gateway deployments.
func (a *Authenticator) oidcRedirectURL(r *http.Request) string {
	if a.OIDC.RedirectURL != "" {
		return a.OIDC.RedirectURL
	}
	return a.requestOrigin(r) + "/auth/callback"
}

func (a *Authenticator) oidcStart(w http.ResponseWriter, r *http.Request) {
	state, verifier := identity.RandomString(16), identity.RandomString(32)
	url, err := a.OIDC.AuthCodeURL(r.Context(), a.oidcRedirectURL(r), state, verifier)
	if err != nil {
		// A cold or unreachable IdP must not look like a rejected login.
		http.Error(w, "identity provider unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	secure := a.secureRequest(r)
	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookie, Value: state + "|" + verifier, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
		MaxAge: 600,
	})
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func (a *Authenticator) oidcCallback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(oidcStateCookie)
	if err != nil {
		http.Error(w, "login flow expired; start again", http.StatusBadRequest)
		return
	}
	state, verifier, ok := strings.Cut(c.Value, "|")
	// Constant-time compare: the state is the CSRF defence for the callback,
	// so a timing oracle on it is worth avoiding even though it is short-lived.
	if !ok || subtle.ConstantTimeCompare([]byte(state), []byte(r.URL.Query().Get("state"))) != 1 {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	// Burn the flow cookie whatever happens next, so a code cannot be
	// replayed against it.
	http.SetCookie(w, &http.Cookie{Name: oidcStateCookie, Value: "", Path: "/", MaxAge: -1})

	raw, err := a.OIDC.Exchange(r.Context(), a.oidcRedirectURL(r), r.URL.Query().Get("code"), verifier)
	if err != nil {
		http.Error(w, "login failed: "+err.Error(), http.StatusUnauthorized)
		return
	}
	// Verify before trusting: the token came from the provider's token
	// endpoint, but the session cookie is what every later request is judged
	// on, so it must never hold something we have not checked ourselves.
	if _, err := a.OIDC.Verify(r.Context(), raw); err != nil {
		http.Error(w, "provider returned an unverifiable token: "+err.Error(), http.StatusUnauthorized)
		return
	}
	a.setSessionCookie(w, r, raw, 0)
	http.Redirect(w, r, "/", http.StatusSeeOther)
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
