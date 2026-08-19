package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
)

// derivedID reproduces identity.Principal.ID()'s fallback.
//
// Duplicated rather than imported because internal/identity imports nothing
// from here and must keep it that way — and because this test exists
// precisely to pin the relationship between the two, so a copy that has to
// be updated deliberately is the point rather than a smell.
func derivedID(subject string) string {
	h := sha256.Sum256([]byte(subject))
	return "u-" + hex.EncodeToString(h[:8])
}

// The migration must not move a single id.
//
// Every existing principal id is already written into Cell member lists,
// Secret owner labels, Session owners and the Unix uid allocation — four
// places that cannot be updated in one transaction. So the backfill ADOPTS
// each derived id as the allocated one instead of issuing new ones. If this
// test fails, an upgrade silently turns every user into a stranger: their
// projects are not theirs, their credentials are not theirs, and their
// worktree belongs to nobody. Nothing would report an error, because the
// platform would correctly conclude it had never seen them before.
func TestBackfillAdoptsExistingIDsAndMovesNothing(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// An account created the way accounts.go creates them: the id IS the
	// derived one.
	const email = "Zhu.MingZe@us.tinci.com"
	want := derivedID("user:zhu.mingze@us.tinci.com")
	if err := db.CreateUser(ctx, want, email, "朱", "hash", true, true); err != nil {
		t.Fatal(err)
	}

	// Re-run the migration the way a restart would.
	if err := db.backfillPrincipals(); err != nil {
		t.Fatal(err)
	}

	got, err := db.PrincipalFor(ctx, "user", "user:zhu.mingze@us.tinci.com")
	if err != nil {
		t.Fatalf("the existing login was not adopted: %v", err)
	}
	if got != want {
		t.Fatalf("the id MOVED: %q -> %q. Every Cell member list, Secret label, "+
			"Session owner and worktree keyed to the old value is now orphaned.", want, got)
	}
}

// Running it twice is not two rows, and does not reissue an id.
func TestBackfillIsIdempotent(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := derivedID("user:a@b.c")
	if err := db.CreateUser(ctx, id, "a@b.c", "", "h", false, false); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := db.backfillPrincipals(); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.PrincipalFor(ctx, "user", "user:a@b.c")
	if err != nil || got != id {
		t.Fatalf("after three runs: %q %v, want %q", got, err, id)
	}
	bs, err := db.BindingsOf(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 1 {
		t.Fatalf("%d bindings, want 1 — the backfill duplicated a login", len(bs))
	}
}

// The point of the whole change: one person, two ways in, one identity.
//
// This is what makes connecting an enterprise IdP a configuration change
// rather than a migration. Before, the SSO login would have hashed to a
// different id and the person would have arrived as somebody new.
func TestTwoLoginsCanBeOnePrincipal(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := derivedID("user:wang@tinci.com")
	if err := db.CreateUser(ctx, id, "wang@tinci.com", "王", "h", false, true); err != nil {
		t.Fatal(err)
	}
	if err := db.backfillPrincipals(); err != nil {
		t.Fatal(err)
	}

	// He later signs in through Casdoor and links it.
	const ssoSubject = "oidc:958ce9f3:wang"
	if err := db.BindIdentity(ctx, "oidc", ssoSubject, id, "self"); err != nil {
		t.Fatal(err)
	}

	viaPassword, err := db.PrincipalFor(ctx, "user", "user:wang@tinci.com")
	if err != nil {
		t.Fatal(err)
	}
	viaSSO, err := db.PrincipalFor(ctx, "oidc", ssoSubject)
	if err != nil {
		t.Fatal(err)
	}
	if viaPassword != viaSSO {
		t.Fatalf("the same human resolved to two principals: %q vs %q", viaPassword, viaSSO)
	}
	if viaSSO == derivedID(ssoSubject) {
		t.Error("the SSO login resolved to its own hash — the binding was not consulted")
	}
}

// A login nobody has bound belongs to a NEW principal, never to whoever
// happens to share its email.
//
// An IdP administrator can set anybody's email claim. Merging identities on
// that basis alone would let whoever controls the IdP take over any account
// here, silently, by editing one field.
func TestAnUnknownLoginDoesNotInheritByEmail(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	existing := derivedID("user:victim@tinci.com")
	if err := db.CreateUser(ctx, existing, "victim@tinci.com", "", "h", true, true); err != nil {
		t.Fatal(err)
	}
	if err := db.backfillPrincipals(); err != nil {
		t.Fatal(err)
	}

	// An SSO identity turns up claiming the same address.
	got, err := db.ResolveOrCreatePrincipal(ctx, "oidc", "oidc:deadbeef:attacker")
	if err != nil {
		t.Fatal(err)
	}
	if got == existing {
		t.Fatal("an unbound SSO login was merged into an existing account — " +
			"whoever controls the IdP could take over any account by editing an email claim")
	}
}

// Resolving the same login twice is the same principal, not two.
func TestResolveIsStable(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := db.ResolveOrCreatePrincipal(ctx, "oidc", "oidc:aaaa:someone")
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.ResolveOrCreatePrincipal(ctx, "oidc", "oidc:aaaa:someone")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("the same login got two identities: %q then %q", first, second)
	}
}

// A login already bound to somebody is never silently re-pointed.
func TestABoundLoginCannotBeStolen(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	mine, err := db.ResolveOrCreatePrincipal(ctx, "oidc", "oidc:aaaa:me")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := NewPrincipalID()
	if err != nil {
		t.Fatal(err)
	}
	err = db.BindIdentity(ctx, "oidc", "oidc:aaaa:me", theirs, "someone-else")
	if !errors.Is(err, ErrBindingTaken) {
		t.Fatalf("re-binding somebody else's login returned %v, want ErrBindingTaken", err)
	}
	// And it still points where it did.
	got, err := db.PrincipalFor(ctx, "oidc", "oidc:aaaa:me")
	if err != nil || got != mine {
		t.Fatalf("the binding moved anyway: %q %v", got, err)
	}
}

// Binding the same login to the same principal again is fine — a person may
// confirm the same link twice, from two devices.
func TestRebindingToTheSamePrincipalIsNotAnError(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := db.ResolveOrCreatePrincipal(ctx, "oidc", "oidc:bbbb:x")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BindIdentity(ctx, "oidc", "oidc:bbbb:x", id, "self"); err != nil {
		t.Fatalf("re-confirming an existing link failed: %v", err)
	}
}

// Allocated ids must be indistinguishable from adopted ones.
//
// An id that announces when it was issued is an id that invites somebody to
// treat the two differently.
func TestAllocatedIDsLookLikeAdoptedOnes(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		id, err := NewPrincipalID()
		if err != nil {
			t.Fatal(err)
		}
		if len(id) != len(derivedID("anything")) || id[:2] != "u-" {
			t.Fatalf("%q does not have the same shape as an adopted id", id)
		}
		if seen[id] {
			t.Fatalf("allocated the same id twice: %q", id)
		}
		seen[id] = true
	}
}

// A brand-new account is bound at creation, not at first login.
//
// Otherwise the account answers to the id written into `users` while it is
// being created, and to a freshly allocated one from its first request
// onwards — because resolution would find no binding and issue a new id.
// Two ids for one account, with the changeover happening invisibly between
// the sign-up response and the next click.
func TestANewAccountIsBoundWhenItIsCreated(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := derivedID("user:new@tinci.com")
	if err := db.CreateUser(ctx, id, "New@tinci.com", "新人", "h", false, true); err != nil {
		t.Fatal(err)
	}

	// No backfill, no restart: resolution must already find it.
	got, err := db.PrincipalFor(ctx, "user", "user:new@tinci.com")
	if err != nil {
		t.Fatalf("a freshly created account has no binding: %v", err)
	}
	if got != id {
		t.Fatalf("the new account was bound to %q, want the id it was created with (%q)", got, id)
	}

	// And resolving does not mint a second one.
	again, err := db.ResolveOrCreatePrincipal(ctx, "user", "user:new@tinci.com")
	if err != nil || again != id {
		t.Fatalf("first login allocated a different id: %q %v", again, err)
	}
}

// A failed account creation leaves no principal behind.
func TestAFailedCreateLeavesNoOrphanPrincipal(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := derivedID("user:dup@tinci.com")
	if err := db.CreateUser(ctx, id, "dup@tinci.com", "", "h", false, false); err != nil {
		t.Fatal(err)
	}
	// Same address again: the unique index refuses it.
	other, _ := NewPrincipalID()
	if err := db.CreateUser(ctx, other, "dup@tinci.com", "", "h", false, false); err == nil {
		t.Fatal("a duplicate address was accepted")
	}
	var n int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM principals WHERE id = ?`, other).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("the failed create left a principal with no account behind")
	}
}
