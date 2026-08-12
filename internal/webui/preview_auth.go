package webui

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Untrusted content must not be reachable with the console's credential
// (ADR-0007). Two reasons the console cookie cannot be reused here:
//
//   - cookies ignore port, so a same-host/different-port split would hand
//     the console cookie straight to untrusted content;
//   - it is a long-lived credential for the whole platform, while a preview
//     session only ever needs to view one Cell.
//
// So the console mints a short-lived, per-Cell ticket into the preview URL.
// The preview listener exchanges it for a cookie scoped to that Cell's path
// and host, and never honours the console cookie.
const (
	previewCookie    = "agentcell_preview"
	previewTicketQS  = "__ac"
	previewTicketTTL = 10 * time.Minute
	previewCookieTTL = 8 * time.Hour
)

// previewKey derives the ticket-signing key from the configured access
// tokens: stable across restarts with the same config, and rotated
// automatically when the tokens are.
func (a *Authenticator) previewKey() []byte {
	h := sha256.New()
	for _, t := range a.sortedTokens() {
		_, _ = h.Write([]byte(t))
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write([]byte("agentcell-preview-v1"))
	return h.Sum(nil)
}

// MintPreviewTicket returns a signed capability for one Cell.
func (a *Authenticator) MintPreviewTicket(cell string) string {
	exp := time.Now().Add(previewTicketTTL).Unix()
	payload := cell + ":" + strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, a.previewKey())
	_, _ = mac.Write([]byte(payload))
	return payload + ":" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyPreviewTicket checks a ticket and returns the Cell it grants.
func (a *Authenticator) verifyPreviewTicket(ticket string) (string, error) {
	i := strings.LastIndex(ticket, ":")
	if i < 0 {
		return "", fmt.Errorf("malformed ticket")
	}
	payload, sig := ticket[:i], ticket[i+1:]
	mac := hmac.New(sha256.New, a.previewKey())
	_, _ = mac.Write([]byte(payload))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return "", fmt.Errorf("bad signature")
	}
	j := strings.LastIndex(payload, ":")
	if j < 0 {
		return "", fmt.Errorf("malformed payload")
	}
	cell, expStr := payload[:j], payload[j+1:]
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", fmt.Errorf("expired ticket")
	}
	return cell, nil
}

// PreviewMiddleware authorizes the preview origin. It accepts a ticket in
// the query (exchanging it for a scoped cookie and redirecting to strip it)
// or a previously issued preview cookie for the SAME Cell. The console
// cookie and bearer tokens are deliberately not accepted: nothing that can
// act on the control plane should be usable from untrusted content.
func (a *Authenticator) PreviewMiddleware(cellOf func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Enabled() || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		cell := cellOf(r)
		if cell == "" {
			http.Error(w, "no cell in request", http.StatusBadRequest)
			return
		}

		if t := r.URL.Query().Get(previewTicketQS); t != "" {
			granted, err := a.verifyPreviewTicket(t)
			if err != nil || granted != cell {
				http.Error(w, "invalid preview ticket", http.StatusForbidden)
				return
			}
			// Scope the cookie to this Cell's path so the browser will not
			// attach it to another Cell's preview on a shared host.
			http.SetCookie(w, &http.Cookie{
				Name: previewCookieName(cell), Value: t, Path: "/preview/" + cell + "/",
				HttpOnly: true, SameSite: http.SameSiteLaxMode,
				Secure: strings.HasPrefix(requestOrigin(r), "https://"),
				MaxAge: int(previewCookieTTL.Seconds()),
			})
			http.SetCookie(w, &http.Cookie{
				Name: previewCookieName(cell), Value: t, Path: "/app/" + cell + "/",
				HttpOnly: true, SameSite: http.SameSiteLaxMode,
				Secure: strings.HasPrefix(requestOrigin(r), "https://"),
				MaxAge: int(previewCookieTTL.Seconds()),
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

		c, err := r.Cookie(previewCookieName(cell))
		if err != nil {
			http.Error(w, "preview session required — open this from the AgentCell console", http.StatusUnauthorized)
			return
		}
		granted, err := a.verifyPreviewTicket(c.Value)
		if err != nil || granted != cell {
			http.Error(w, "preview session expired — reopen from the console", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// previewCookieName keeps one Cell's session from colliding with another's
// when a deployment has not given each Cell its own hostname.
func previewCookieName(cell string) string { return previewCookie + "_" + cell }
