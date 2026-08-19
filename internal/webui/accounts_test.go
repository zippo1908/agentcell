package webui

import (
	"path/filepath"
	"testing"

	"github.com/zippo1908/agentcell/internal/store"
)

func accountsFixture(t *testing.T) *Accounts {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Accounts{DB: db, Key: []byte("test-key-material")}
}

// A password change must end every session that exists.
//
// This is the property that makes "I think somebody has my password" an
// action a person can take alone, at the moment they are worried, without
// an administrator and without a list of devices. It works because the
// cookie signature is taken over the password hash — nothing has to
// remember that those sessions were ever issued.
func TestChangingThePasswordInvalidatesEveryCookie(t *testing.T) {
	a := accountsFixture(t)
	ctx := t.Context()
	if err := a.Bootstrap(ctx, "boss@tinci.com", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	cookie, err := a.Mint(ctx, "boss@tinci.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.FromCookie(ctx, cookie); !ok {
		t.Fatal("a freshly minted cookie was refused")
	}

	hash, err := hashPassword("a-completely-different-one")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DB.SetPassword(ctx, "boss@tinci.com", hash); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.FromCookie(ctx, cookie); ok {
		t.Error("a session issued before the password change is still valid")
	}
}

// A tampered cookie is not a session, however plausible it looks.
func TestCookieCannotBeEditedIntoSomebodyElse(t *testing.T) {
	a := accountsFixture(t)
	ctx := t.Context()
	if err := a.Bootstrap(ctx, "boss@tinci.com", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	hash, _ := hashPassword("another-long-password")
	if err := a.DB.CreateUser(ctx, "u2", "li@tinci.com", "Li", hash, false, false); err != nil {
		t.Fatal(err)
	}
	cookie, err := a.Mint(ctx, "li@tinci.com")
	if err != nil {
		t.Fatal(err)
	}
	// Assert the honest cookie works FIRST. Without this the forgery check
	// below passes just as well when everything is refused, which is how a
	// broken parser looks exactly like a secure one.
	if p, ok := a.FromCookie(ctx, cookie); !ok || p.Email != "li@tinci.com" {
		t.Fatalf("the genuine cookie was refused (ok=%v, as %q)", ok, p.Email)
	}
	// Swap the identity, keep the signature: the classic forgery.
	forged := "boss@tinci.com" + cookie[len("li@tinci.com"):]
	if p, ok := a.FromCookie(ctx, forged); ok {
		t.Errorf("a rewritten cookie authenticated as %s", p.Email)
	}
}

// An invitation is a bearer credential: single use, and gone once used.
func TestInviteCannotBeRedeemedTwice(t *testing.T) {
	a := accountsFixture(t)
	ctx := t.Context()
	tok, err := a.Invite(ctx, "new@tinci.com", "New", "boss@tinci.com", false, false)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Redeem(ctx, tok, "", "long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	if p.Email != "new@tinci.com" || p.Admin {
		t.Errorf("redeemed as %+v, want a non-admin new@tinci.com", p)
	}
	if _, err := a.Redeem(ctx, tok, "", "long-enough-password"); err == nil {
		t.Error("the same invitation was redeemed a second time")
	}
}

// The admin flag comes from the INVITATION, not from what the person types
// when they redeem it. Otherwise being invited would be enough to make
// yourself an administrator.
func TestRedeemerCannotMakeThemselvesAdmin(t *testing.T) {
	a := accountsFixture(t)
	ctx := t.Context()
	tok, err := a.Invite(ctx, "new@tinci.com", "", "boss@tinci.com", false, false)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Redeem(ctx, tok, "Someone", "long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	if p.Admin {
		t.Error("redeeming an ordinary invitation produced an administrator")
	}
}

// A short password is refused at the point it is set, not reported later.
func TestShortPasswordIsRefused(t *testing.T) {
	a := accountsFixture(t)
	tok, err := a.Invite(t.Context(), "new@tinci.com", "", "boss@tinci.com", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Redeem(t.Context(), tok, "", "short"); err == nil {
		t.Error("a five-character password was accepted")
	}
	// And the invitation survives, so the person can try again rather than
	// needing a new link for a typo.
	if _, err := a.Redeem(t.Context(), tok, "", "long-enough-password"); err != nil {
		t.Errorf("the invitation was consumed by a failed attempt: %v", err)
	}
}

// Bootstrap exists to create the FIRST administrator and must never be a
// way to add a second one to a running deployment.
func TestBootstrapOnlyEverRunsOnce(t *testing.T) {
	a := accountsFixture(t)
	ctx := t.Context()
	if err := a.Bootstrap(ctx, "boss@tinci.com", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	if err := a.Bootstrap(ctx, "intruder@example.com", "another-long-one"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.DB.UserByEmail(ctx, "intruder@example.com"); err == nil {
		t.Error("bootstrap created a second account on a populated deployment")
	}
}

// A disabled account cannot log in and its existing cookies stop working.
func TestDisabledAccountIsLockedOut(t *testing.T) {
	a := accountsFixture(t)
	ctx := t.Context()
	if err := a.Bootstrap(ctx, "boss@tinci.com", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	cookie, _ := a.Mint(ctx, "boss@tinci.com")
	if err := a.DB.SetDisabled(ctx, "boss@tinci.com", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Verify(ctx, "boss@tinci.com", "correct-horse-battery"); ok {
		t.Error("a disabled account logged in")
	}
	if _, ok := a.FromCookie(ctx, cookie); ok {
		t.Error("a disabled account kept its live session")
	}
}
