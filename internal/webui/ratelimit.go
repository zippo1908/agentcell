package webui

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Rate limiting the login.
//
// Verifying a password here costs an argon2id derivation — 64 MB and a few
// milliseconds of CPU, deliberately, because that is what makes a stolen
// hash expensive to attack. It also makes the login form the cheapest way to
// take the console down: an unauthenticated client can spend the server's
// memory and CPU as fast as it can open connections, and it does not need a
// valid address to do it — the miss path hashes too, so that a wrong address
// cannot be told from a wrong password by timing.
//
// Two counters, because the two attacks are different. Per ADDRESS bounds
// guessing one person's password from anywhere; per CLIENT bounds one source
// grinding through many addresses. Either one tripping is enough to refuse.
//
// Deliberately in memory: this deployment runs one celld (the accounts
// database allows exactly one writer), so a shared store would be
// infrastructure bought for a property the deployment already has. If that
// ever changes, this is the seam to move.

const (
	// loginWindow and loginBurst: enough for somebody mistyping a password
	// a few times in a row, far below what a script needs to be useful.
	loginWindow = time.Minute
	loginBurst  = 8
)

type rateLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newRateLimiter() *rateLimiter { return &rateLimiter{hits: map[string][]time.Time{}} }

// allow records an attempt and reports whether it may proceed.
func (l *rateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-loginWindow)
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	// Keep the map from growing without bound on a long-running process:
	// a key with nothing left in the window is a key nobody is using.
	if len(kept) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = kept
	}
	if len(kept) >= loginBurst {
		return false
	}
	l.hits[key] = append(l.hits[key], now)
	return true
}

// clientKey identifies the source of a request for rate-limiting purposes.
//
// X-Forwarded-For is honoured only where the operator said proxy headers may
// be trusted. Taking it otherwise would let anybody set their own bucket and
// walk straight past the limit — a rate limiter keyed on a value the client
// chooses is decoration.
func (a *Authenticator) clientKey(r *http.Request) string {
	if h := a.forwarded(r, "X-Forwarded-For"); h != "" {
		if first, _, ok := strings.Cut(h, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(h)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// tooManyLogins reports whether this attempt should be refused outright,
// before any password work is done.
func (a *Authenticator) tooManyLogins(r *http.Request, email string) bool {
	if a.logins == nil {
		return false
	}
	now := time.Now()
	// Both are recorded even when one already refuses, so a client cannot
	// keep an address's counter clean by exhausting its own.
	byClient := a.logins.allow("ip:"+a.clientKey(r), now)
	byEmail := a.logins.allow("acct:"+strings.ToLower(strings.TrimSpace(email)), now)
	return !byClient || !byEmail
}
