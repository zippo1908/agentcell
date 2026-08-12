package webui

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Untrusted content must not be reachable with the console's credential,
// and one Cell's content must not be able to reach another's — nor may a
// Cell's DEV zone reach its own PROD zone (ADR-0007). Dev is where the
// agent's unreviewed work runs; prod serves a released, reviewed build.
// Same-origin between them would let dev read prod, share its storage, or
// install a service worker over it.
//
// So each (cell, zone) pair gets its own host and its own capability:
//
//	<cell>-dev.<preview-domain>   → /preview/<cell>/
//	<cell>-prod.<preview-domain>  → /app/<cell>/
//
// The console mints a short single-use ticket bound to cell+zone+host; the
// preview listener exchanges it for a session cookie scoped to that zone's
// path. The console cookie and bearer tokens are never accepted here.
const (
	previewCookie     = "agentcell_preview"
	previewTicketQS   = "__ac"
	previewTicketTTL  = 2 * time.Minute // URL ticket: single use, short-lived
	previewSessionTTL = 8 * time.Hour   // exchanged cookie: the working session
)

// Zone distinguishes a Cell's untrusted zones.
type Zone string

const (
	ZoneDev  Zone = "dev"
	ZoneProd Zone = "prod"
)

// usedTickets records single-use ticket nonces until they expire, so a
// ticket captured from a URL (history, logs, a referrer) cannot be replayed.
type usedTickets struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func (u *usedTickets) consume(nonce string, exp time.Time) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.seen == nil {
		u.seen = map[string]time.Time{}
	}
	now := time.Now()
	for k, t := range u.seen { // bounded by the ticket TTL
		if now.After(t) {
			delete(u.seen, k)
		}
	}
	if _, replay := u.seen[nonce]; replay {
		return false
	}
	u.seen[nonce] = exp
	return true
}

// previewKey derives the signing key from the configured access tokens plus
// dedicated key material: stable across restarts with the same config,
// invalidated when tokens rotate.
//
// The key material is not optional. With OIDC there may be no static tokens
// at all, and the digest of an empty token list is a constant anyone can
// compute — every preview ticket would be forgeable.
func (a *Authenticator) previewKey() []byte {
	h := sha256.New()
	_, _ = h.Write(a.keyMaterial)
	_, _ = h.Write([]byte{0})
	for _, t := range a.sortedTokens() {
		_, _ = h.Write([]byte(t))
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write([]byte("agentcell-preview-v2"))
	return h.Sum(nil)
}

// ticket fields: kind|cell|zone|host|exp|nonce|sig
type ticket struct {
	kind  string // "t" = single-use URL ticket, "s" = exchanged session
	cell  string
	zone  Zone
	host  string
	exp   int64
	nonce string
}

func (a *Authenticator) sign(t ticket) string {
	payload := strings.Join([]string{
		t.kind, t.cell, string(t.zone), t.host, strconv.FormatInt(t.exp, 10), t.nonce,
	}, "|")
	mac := hmac.New(sha256.New, a.previewKey())
	_, _ = mac.Write([]byte(payload))
	return payload + "|" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *Authenticator) verify(raw string) (ticket, error) {
	i := strings.LastIndex(raw, "|")
	if i < 0 {
		return ticket{}, fmt.Errorf("malformed")
	}
	payload, sig := raw[:i], raw[i+1:]
	mac := hmac.New(sha256.New, a.previewKey())
	_, _ = mac.Write([]byte(payload))
	if !hmac.Equal([]byte(sig), []byte(base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))) {
		return ticket{}, fmt.Errorf("bad signature")
	}
	f := strings.Split(payload, "|")
	if len(f) != 6 {
		return ticket{}, fmt.Errorf("malformed payload")
	}
	exp, err := strconv.ParseInt(f[4], 10, 64)
	if err != nil {
		return ticket{}, fmt.Errorf("malformed expiry")
	}
	if time.Now().Unix() > exp {
		return ticket{}, fmt.Errorf("expired")
	}
	return ticket{kind: f[0], cell: f[1], zone: Zone(f[2]), host: f[3], exp: exp, nonce: f[5]}, nil
}

func newNonce() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// MintPreviewTicket returns a single-use capability for one Cell zone on a
// specific host.
func (a *Authenticator) MintPreviewTicket(cell string, zone Zone, host string) string {
	return a.sign(ticket{
		kind: "t", cell: cell, zone: zone, host: host,
		exp: time.Now().Add(previewTicketTTL).Unix(), nonce: newNonce(),
	})
}

// zonePath is the only path prefix a zone's session may be used for.
func zonePath(cell string, zone Zone) string {
	if zone == ZoneProd {
		return "/app/" + cell + "/"
	}
	return "/preview/" + cell + "/"
}

// ZoneOfPreviewRequest reports which zone a request targets.
func ZoneOfPreviewRequest(r *http.Request) Zone {
	if strings.HasPrefix(r.URL.Path, "/app/") {
		return ZoneProd
	}
	return ZoneDev
}

// PreviewMiddleware authorizes the untrusted-content origin.
func (a *Authenticator) PreviewMiddleware(next http.Handler) http.Handler {
	used := &usedTickets{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Enabled() || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		cell := CellFromPreviewRequest(r)
		if cell == "" {
			http.Error(w, "no cell in request", http.StatusBadRequest)
			return
		}
		zone := ZoneOfPreviewRequest(r)
		host := a.hostOnly(r)
		cookieName := previewCookieName(cell, zone)

		if raw := r.URL.Query().Get(previewTicketQS); raw != "" {
			t, err := a.verify(raw)
			// Every binding is checked: a dev ticket cannot open prod, one
			// Cell's ticket cannot open another's, and a ticket minted for a
			// different host cannot be replayed here.
			if err != nil || t.kind != "t" || t.cell != cell || t.zone != zone || t.host != host {
				http.Error(w, "invalid preview ticket", http.StatusForbidden)
				return
			}
			if !used.consume(t.nonce, time.Unix(t.exp, 0)) {
				http.Error(w, "preview ticket already used", http.StatusForbidden)
				return
			}
			// Exchange for a session whose lifetime matches the cookie's, so
			// the tab does not start 401-ing when the short ticket expires.
			session := a.sign(ticket{
				kind: "s", cell: cell, zone: zone, host: host,
				exp: time.Now().Add(previewSessionTTL).Unix(), nonce: newNonce(),
			})
			http.SetCookie(w, &http.Cookie{
				Name: cookieName, Value: session, Path: zonePath(cell, zone),
				HttpOnly: true, SameSite: http.SameSiteLaxMode,
				Secure: a.secureRequest(r),
				MaxAge: int(previewSessionTTL.Seconds()),
			})
			q := r.URL.Query()
			q.Del(previewTicketQS)
			target := r.URL.Path
			if enc := q.Encode(); enc != "" {
				target += "?" + enc
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}

		c, err := r.Cookie(cookieName)
		if err != nil {
			http.Error(w, "preview session required — open this from the AgentCell console", http.StatusUnauthorized)
			return
		}
		t, err := a.verify(c.Value)
		if err != nil || t.kind != "s" || t.cell != cell || t.zone != zone || t.host != host {
			http.Error(w, "preview session expired — reopen from the console", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// previewCookieName keeps a Cell's zones (and different Cells) from
// colliding if a deployment has not given each its own host.
func previewCookieName(cell string, zone Zone) string {
	return previewCookie + "_" + cell + "_" + string(zone)
}

// hostOnly is the request's host without a port, which is what the ticket
// binds to (the port cannot be trusted to distinguish origins for cookies).
func (a *Authenticator) hostOnly(r *http.Request) string {
	h := r.Host
	if fh := a.forwarded(r, "X-Forwarded-Host"); fh != "" {
		h = fh
	}
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return h
}
