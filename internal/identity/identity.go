// Package identity models who is making a request.
//
// Everything in AgentCell that is not the shared project layer belongs to
// exactly one principal (ADR-0008). Authorization is then a single rule —
// you see what you own — with no "is multi-user enabled" branch, because a
// deployment with no identity provider simply has one principal that owns
// everything it created.
package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Kind distinguishes how a principal proved who it is.
type Kind string

const (
	// KindOIDC is a human authenticated by the identity provider.
	KindOIDC Kind = "oidc"
	// KindToken is a static bearer token: break-glass and CI. It is a
	// single shared identity by nature, which is exactly why it is not the
	// default once an IdP is configured.
	KindToken Kind = "token"
)

// Principal is the authenticated caller.
type Principal struct {
	// Subject is stable and unique across issuers. Human-debuggable; not
	// used directly as a Kubernetes field value (see ID).
	Subject string
	Name    string
	Email   string
	Kind    Kind
}

// StaticToken is the principal every static bearer token resolves to.
var StaticToken = Principal{Subject: "token:static", Name: "static token", Kind: KindToken}

// OIDCSubject builds a subject that stays unique when two issuers happen to
// use the same `sub` — which they do, since `sub` is only required to be
// unique per issuer.
func OIDCSubject(issuer, sub string) string {
	h := sha256.Sum256([]byte(issuer))
	return "oidc:" + hex.EncodeToString(h[:4]) + ":" + sub
}

// ID is the form written to Kubernetes objects: a hash, so it is a valid
// label value and a valid CR field regardless of what the IdP puts in `sub`
// (which may contain '@', '|', spaces, or be longer than 63 characters).
//
// Hashing also means an object's owner cannot be read back as an email
// address by anyone who can list CRs.
func (p Principal) ID() string {
	if p.Subject == "" {
		return ""
	}
	h := sha256.Sum256([]byte(p.Subject))
	return "u-" + hex.EncodeToString(h[:8])
}

// Display is a human label for the UI and audit lines, never for authorization.
func (p Principal) Display() string {
	switch {
	case p.Name != "":
		return p.Name
	case p.Email != "":
		return p.Email
	default:
		return p.Subject
	}
}

// IsZero reports an unauthenticated request.
func (p Principal) IsZero() bool { return p.Subject == "" }

// Owns reports whether an object's recorded owner is this principal.
//
// An empty owner means the object predates ownership (ADR-0008): it is
// visible to the static-token principal only. Guessing an owner that was
// never recorded would hand one user another's work.
func (p Principal) Owns(ownerID string) bool {
	if ownerID == "" {
		return p.Kind == KindToken
	}
	return ownerID == p.ID()
}

type ctxKey struct{}

// NewContext carries the principal through the handler chain.
func NewContext(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the principal established by the auth middleware. The
// zero Principal means the request was not authenticated.
func FromContext(ctx context.Context) Principal {
	p, _ := ctx.Value(ctxKey{}).(Principal)
	return p
}

// LooksLikeJWT reports whether a presented credential is worth handing to
// the OIDC verifier. Static tokens are opaque random strings, so this keeps
// one credential form from producing confusing errors about the other.
func LooksLikeJWT(s string) bool {
	return strings.Count(s, ".") == 2 && !strings.HasPrefix(s, ".")
}
