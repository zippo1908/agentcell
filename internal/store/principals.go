package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// A Principal is the entity; a login is one of its identifiers.
//
// Identity used to BE the primary key: a principal's id was
// `hash(subject)`, and the subject was whatever the person happened to
// authenticate with. That value is denormalized into Kubernetes objects,
// Secret labels, Unix uids and audit records — four places that cannot be
// updated in one transaction — so the way somebody logged in decided, once
// and permanently, who the platform thought they were.
//
// Two consequences, one obvious and one that was already costing us:
//
//   - Connecting an enterprise IdP would give every existing person a brand
//     new identity. Not an error — the platform would correctly conclude it
//     had never seen them before, and their projects, credentials and
//     worktree would simply not be theirs.
//   - **There is no way to change somebody's email**, which is why no such
//     endpoint exists. Changing it changes `user:<email>`, which changes the
//     hash, which changes who they are. A person who marries, or whose
//     company changes domain, cannot be accommodated.
//
// The fix inverts the relationship. A principal id is allocated once and
// never derived from anything:
//
//	principals            id (permanent, opaque)
//	identity_bindings     (provider, subject) -> principal_id
//
// A Casdoor login, an Entra login and a local password can all be bindings
// of one principal. Adding or removing a binding does not touch the id, so
// none of the four denormalized copies has to change.
//
// The migration is the part that makes this safe to do now: every existing
// principal id is ADOPTED as the allocated id rather than replaced (see
// backfillPrincipals). Nothing in Kubernetes or on disk moves. The id stops
// being derived and starts being stored — same value, different provenance
// — and from that moment adding a binding is free. Done after an enterprise
// IdP is connected, this same change is a migration across four systems.

// ErrBindingTaken is returned when a (provider, subject) already points at a
// different principal. It is never resolved by overwriting: an identity that
// already belongs to somebody is the one case where guessing hands one
// person another's account.
var ErrBindingTaken = errors.New("this login is already bound to another principal")

// NewPrincipalID allocates an opaque, permanent id.
//
// Same shape as the ids already written to Kubernetes objects (`u-` and 16
// hex characters) on purpose: that value is a label value, a CR field and
// the seed for a Unix uid, and introducing a second shape would mean every
// consumer has to accept both. New ids are indistinguishable from adopted
// ones, which is the point — an id carries no information.
func NewPrincipalID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "u-" + hex.EncodeToString(b[:]), nil
}

// PrincipalFor resolves a login to its principal, or ErrNotFound.
func (db *DB) PrincipalFor(ctx context.Context, provider, subject string) (string, error) {
	var id string
	err := db.sql.QueryRowContext(ctx,
		`SELECT principal_id FROM identity_bindings WHERE provider = ? AND subject = ?`,
		provider, subject).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

// BindIdentity attaches a login to an existing principal.
//
// Binding the same login to the same principal twice is not an error — a
// person may confirm the same link from two devices — but binding it to a
// different one is refused.
func (db *DB) BindIdentity(ctx context.Context, provider, subject, principalID, boundBy string) error {
	existing, err := db.PrincipalFor(ctx, provider, subject)
	switch {
	case err == nil && existing == principalID:
		return nil
	case err == nil:
		return fmt.Errorf("%w (%s)", ErrBindingTaken, existing)
	case !errors.Is(err, ErrNotFound):
		return err
	}
	_, err = db.sql.ExecContext(ctx,
		`INSERT INTO identity_bindings (provider, subject, principal_id, bound_by, created_at)
		 VALUES (?,?,?,?,?)`,
		provider, subject, principalID, boundBy, time.Now().Unix())
	return err
}

// ResolveOrCreatePrincipal is what authentication calls.
//
// A login nobody has seen before belongs to a new principal — that is what
// "first time somebody signs in" means. It deliberately does NOT try to
// match on email: an IdP administrator can set anybody's email claim, so
// merging two identities on that basis alone would let whoever controls the
// IdP take over any account here. Linking an additional login to an existing
// principal is a separate, deliberate act (see BindIdentity, and the
// self-service flow that requires the password first).
func (db *DB) ResolveOrCreatePrincipal(ctx context.Context, provider, subject string) (string, error) {
	id, err := db.PrincipalFor(ctx, provider, subject)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return "", err
	}
	id, err = NewPrincipalID()
	if err != nil {
		return "", err
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO principals (id, created_at) VALUES (?,?)`, id, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO identity_bindings (provider, subject, principal_id, created_at)
		 VALUES (?,?,?,?)`, provider, subject, id, now); err != nil {
		// Somebody else bound this login between our read and our write.
		// Their row is as good as ours; take theirs rather than failing.
		if existing, e := db.PrincipalFor(ctx, provider, subject); e == nil {
			return existing, nil
		}
		return "", err
	}
	return id, tx.Commit()
}

// BindingsOf lists the logins that resolve to one principal — what a person
// sees on their own settings page, and what an operator needs to answer
// "how does this human get in".
func (db *DB) BindingsOf(ctx context.Context, principalID string) ([]Binding, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT provider, subject, created_at FROM identity_bindings
		 WHERE principal_id = ? ORDER BY created_at`, principalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Binding{}
	for rows.Next() {
		var b Binding
		if err := rows.Scan(&b.Provider, &b.Subject, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Binding is one way a principal can authenticate.
type Binding struct {
	Provider  string
	Subject   string
	CreatedAt int64
}

// backfillPrincipals adopts every existing identity, changing no ids.
//
// This is the whole reason the change is cheap today. Each account's current
// id — already `hash("user:" + email)`, already written into Cell member
// lists, Secret owner labels and Session owners — becomes the ALLOCATED id
// of a principal, and the local login becomes its first binding. Not one
// value moves; only where the value comes from changes.
//
// Idempotent, and runs on every start alongside the other migrations: an
// account created by an older build that has not yet been adopted is picked
// up the next time celld starts, so the two paths cannot drift.
func (db *DB) backfillPrincipals() error {
	if _, err := db.sql.Exec(
		`INSERT OR IGNORE INTO principals (id, created_at)
		 SELECT id, created_at FROM users`); err != nil {
		return err
	}
	// 'user:' || email reproduces identity.UserSubject exactly: emails are
	// stored through NormalizeEmail, which is the same lower+trim that
	// UserSubject applies. If that ever stops being true, adopted rows stop
	// matching what authentication looks up, and people get new identities
	// on their next login — so the two must be changed together.
	_, err := db.sql.Exec(
		`INSERT OR IGNORE INTO identity_bindings (provider, subject, principal_id, created_at)
		 SELECT 'user', 'user:' || email, id, created_at FROM users`)
	return err
}
