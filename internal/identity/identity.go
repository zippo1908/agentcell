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
	// KindUser is a person with an account on this deployment: invited by
	// somebody, holding their own password, their own credentials and their
	// own forge identity. This is what makes "your key" and "your GitLab
	// account" mean anything — under a shared token they were one drawer.
	KindUser Kind = "user"
)

// Principal is the authenticated caller.
type Principal struct {
	// Admin may invite people and act on anyone's behalf in the places the
	// product says an administrator can. Carried here rather than looked up
	// per check, so a handler cannot forget to ask.
	Admin bool
	// Subject is stable and unique across issuers. Human-debuggable; not
	// used directly as a Kubernetes field value (see ID).
	Subject string
	Name    string
	Email   string
	Kind    Kind
	// id is the ALLOCATED principal id, resolved from the identity-binding
	// table when authentication happens. Unexported and set only through
	// WithID, so it cannot be filled in by accident somewhere that has not
	// actually resolved anything.
	//
	// Empty means "not resolved", and ID() then derives the value the way
	// it always did. That fallback is what keeps deployments without an
	// account store working, and what makes this change safe to roll out:
	// for every existing person the resolved id and the derived id are the
	// same value, because the resolved one was adopted from it.
	id string
}

// WithID returns a copy carrying an allocated principal id.
func (p Principal) WithID(id string) Principal {
	p.id = id
	return p
}

// HasAllocatedID reports whether this principal's id came from the binding
// table rather than from hashing its subject.
func (p Principal) HasAllocatedID() bool { return p.id != "" }

// StaticToken is the principal every static bearer token resolves to.
var StaticToken = Principal{Subject: "token:static", Name: "static token", Kind: KindToken}

// UserSubject identifies a person by the address they log in with. The
// address, not a row id: it stays stable if the store is ever rebuilt, and
// it is what an operator reads in an audit line.
func UserSubject(email string) string { return "user:" + strings.ToLower(strings.TrimSpace(email)) }

// OIDCSubject builds a subject that stays unique when two issuers happen to
// use the same `sub` — which they do, since `sub` is only required to be
// unique per issuer.
func OIDCSubject(issuer, sub string) string {
	h := sha256.Sum256([]byte(issuer))
	return "oidc:" + hex.EncodeToString(h[:4]) + ":" + sub
}

// ID is the form written to Kubernetes objects — a short opaque token, so it
// is a valid label value and a valid CR field regardless of what an IdP puts
// in `sub` (which may contain '@', '|', spaces, or exceed 63 characters).
// It also means an object's owner cannot be read back as an email address by
// anyone who can list CRs.
//
// Where the value comes from is the thing that changed. It is now the id
// ALLOCATED to this principal and stored against its logins; hashing the
// subject is the fallback for principals nothing has resolved — a deployment
// with no account store, or the static token.
//
// The distinction matters because this value is written into Cell member
// lists, Secret owner labels, Session owners and Unix uids. While it was
// derived, the way a person authenticated decided who they were, so
// connecting an IdP would have made everybody a stranger and changing
// somebody's email was impossible by construction. Allocated, a login is
// just one of a principal's identifiers and can be added or removed
// freely. See internal/store/principals.go.
func (p Principal) ID() string {
	if p.id != "" {
		return p.id
	}
	if p.Subject == "" {
		return ""
	}
	h := sha256.Sum256([]byte(p.Subject))
	return "u-" + hex.EncodeToString(h[:8])
}

// Provider names the authentication method a binding is keyed by.
//
// The Kind is enough: subjects are already globally unique by construction
// (UserSubject carries the address, OIDCSubject carries a hash of the
// issuer), so the issuer distinction lives inside the subject rather than
// being split across two columns. Splitting it later is a migration of this
// table alone, which is exactly the property the redesign was for.
func (p Principal) Provider() string { return string(p.Kind) }

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
