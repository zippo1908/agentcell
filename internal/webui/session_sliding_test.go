package webui

import (
	"fmt"
	"testing"
	"time"

	"github.com/zippo1908/agentcell/internal/store"
)

// mintAt forges a cookie that expires at a chosen moment, which is how a
// week-long window gets tested without waiting a week.
func mintAt(t *testing.T, a *Accounts, email string, exp time.Time) string {
	t.Helper()
	_, hash, err := a.DB.UserByEmail(t.Context(), email)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("%s|%d", store.NormalizeEmail(email), exp.Unix())
	return body + "." + a.sign(body, hash)
}

func fixtureWithUser(t *testing.T, email string) *Accounts {
	t.Helper()
	a := accountsFixture(t)
	if err := a.Bootstrap(t.Context(), email, "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	return a
}

// A session in active use is never asked for a password again.
//
// The old window was twelve hours ABSOLUTE, so everybody was logged out on a
// fixed clock — in the middle of an afternoon, with a terminal open. Sliding
// is what makes "remembered" true.
func TestAFreshSessionIsNotWorthRenewing(t *testing.T) {
	a := fixtureWithUser(t, "boss@tinci.com")

	cookie, err := a.Mint(t.Context(), "boss@tinci.com")
	if err != nil {
		t.Fatal(err)
	}
	_, ok, stale := a.fromCookie(t.Context(), cookie)
	if !ok {
		t.Fatal("a freshly minted cookie must be accepted")
	}
	if stale {
		t.Error("a cookie issued a moment ago should not be re-issued on the next request")
	}
}

func TestAHalfSpentSessionIsRenewed(t *testing.T) {
	a := fixtureWithUser(t, "boss@tinci.com")
	// Past halfway, still valid.
	cookie := mintAt(t, a, "boss@tinci.com", time.Now().Add(renewWithin-time.Hour))

	_, ok, stale := a.fromCookie(t.Context(), cookie)

	if !ok {
		t.Fatal("a cookie past halfway is still valid")
	}
	if !stale {
		t.Error("a cookie past halfway must be re-issued, or an active session eventually expires anyway")
	}
}

// The stated goal of the old comment, now actually achieved: a session
// nobody has touched lapses on its own.
func TestAnUntouchedSessionStillExpires(t *testing.T) {
	a := fixtureWithUser(t, "boss@tinci.com")
	cookie := mintAt(t, a, "boss@tinci.com", time.Now().Add(-time.Minute))

	if _, ok, _ := a.fromCookie(t.Context(), cookie); ok {
		t.Fatal("an expired cookie must be refused however long the window is")
	}
}

// Sliding must not weaken the one revocation the design relies on.
func TestRenewalDoesNotSurviveAPasswordChange(t *testing.T) {
	a := fixtureWithUser(t, "boss@tinci.com")
	cookie := mintAt(t, a, "boss@tinci.com", time.Now().Add(renewWithin-time.Hour))
	if _, ok, stale := a.fromCookie(t.Context(), cookie); !ok || !stale {
		t.Fatal("precondition: a renewable cookie")
	}

	hash, err := hashPassword("a-completely-different-one")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DB.SetPassword(t.Context(), "boss@tinci.com", hash); err != nil {
		t.Fatal(err)
	}

	if _, ok, _ := a.fromCookie(t.Context(), cookie); ok {
		t.Fatal("a renewable cookie survived a password change; revocation is broken")
	}
}

// A disabled account cannot renew its way back in.
func TestADisabledAccountCannotRenew(t *testing.T) {
	a := fixtureWithUser(t, "boss@tinci.com")
	cookie := mintAt(t, a, "boss@tinci.com", time.Now().Add(renewWithin-time.Hour))
	if err := a.DB.SetDisabled(t.Context(), "boss@tinci.com", true); err != nil {
		t.Fatal(err)
	}

	if _, ok, _ := a.fromCookie(t.Context(), cookie); ok {
		t.Fatal("a disabled account kept a live session")
	}
}
