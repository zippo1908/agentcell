package store

import (
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Two spellings of one address must not become two people.
//
// Email case is not significant in the part that identifies a mailbox, so
// somebody who signs up as Zhu@Tinci.com and later types zhu@tinci.com is
// the same person — and if the database disagrees, they get a second empty
// account and none of their own work.
func TestEmailCaseIsNotIdentity(t *testing.T) {
	db := open(t)
	if err := db.CreateUser(t.Context(), "u1", "Zhu@Tinci.com", "Zhu", "hash", false, false); err != nil {
		t.Fatal(err)
	}
	u, _, err := db.UserByEmail(t.Context(), "  zhu@tinci.com ")
	if err != nil {
		t.Fatalf("the same address did not find the same person: %v", err)
	}
	if u.ID != "u1" {
		t.Errorf("id = %q, want u1", u.ID)
	}
	if err := db.CreateUser(t.Context(), "u2", "ZHU@TINCI.COM", "", "h", false, false); err == nil {
		t.Error("a second account was created for the same address")
	}
}

// An expired invitation is not an invitation. It is a bearer credential
// that turns its holder into a user, so a link left in a chat log must
// stop working on its own.
func TestExpiredInviteIsRefused(t *testing.T) {
	db := open(t)
	past := time.Now().Add(-time.Minute).Unix()
	if err := db.CreateInvite(t.Context(), "hash-old", Invite{Email: "a@b.c", Expires: past}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Invite(t.Context(), "hash-old"); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound for an expired invite", err)
	}
	if got, _ := db.PendingInvites(t.Context()); len(got) != 0 {
		t.Errorf("an expired invite is still listed as pending: %v", got)
	}
}

// Lending quota to a team must reach every member, and lending to one
// person must not.
func TestGrantsReachTeamsAndOnlyTheRightPeople(t *testing.T) {
	db := open(t)
	ctx := t.Context()
	if err := db.CreateGrant(ctx, Grant{"boss", GranteeTeam, "platform", "kimi"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateGrant(ctx, Grant{"boss", GranteeUser, "li", "openai"}); err != nil {
		t.Fatal(err)
	}

	// A member of the team gets the team grant.
	got, err := db.GrantsTo(ctx, "wang", []string{"platform"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Credential != "kimi" {
		t.Errorf("team member got %v, want the team's kimi grant", got)
	}

	// Somebody in no team gets nothing, even though grants exist.
	if got, _ = db.GrantsTo(ctx, "wang", nil); len(got) != 0 {
		t.Errorf("a non-member received %v", got)
	}

	// The personal grant reaches its target and nobody else.
	if got, _ = db.GrantsTo(ctx, "li", nil); len(got) != 1 || got[0].Credential != "openai" {
		t.Errorf("li got %v, want the personal openai grant", got)
	}

	// Granting twice is not two grants; taking it back is complete.
	if err := db.CreateGrant(ctx, Grant{"boss", GranteeUser, "li", "openai"}); err != nil {
		t.Fatal(err)
	}
	if by, _ := db.GrantsBy(ctx, "boss"); len(by) != 2 {
		t.Errorf("granting the same thing twice made %d grants", len(by))
	}
	if err := db.DeleteGrant(ctx, Grant{"boss", GranteeUser, "li", "openai"}); err != nil {
		t.Fatal(err)
	}
	if got, _ = db.GrantsTo(ctx, "li", nil); len(got) != 0 {
		t.Errorf("li still holds %v after it was taken back", got)
	}
}

// A forge identity is per person and replaceable, and listing them must
// never hand back the tokens.
func TestGitIdentityIsPerPersonAndNeverListedWithItsToken(t *testing.T) {
	db := open(t)
	ctx := t.Context()
	if err := db.SetGitIdentity(ctx, "li", GitIdentity{"gitlab", "li", "glpat-first"}); err != nil {
		t.Fatal(err)
	}
	// Re-linking replaces rather than duplicating: rotating a token is the
	// normal case, not an error.
	if err := db.SetGitIdentity(ctx, "li", GitIdentity{"gitlab", "li", "glpat-second"}); err != nil {
		t.Fatal(err)
	}
	g, err := db.GitIdentity(ctx, "li", "gitlab")
	if err != nil {
		t.Fatal(err)
	}
	if g.Token != "glpat-second" {
		t.Errorf("token = %q, want the rotated one", g.Token)
	}
	// Somebody else's forge identity is not readable by asking for it.
	if _, err := db.GitIdentity(ctx, "wang", "gitlab"); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound for another person's identity", err)
	}
	list, err := db.GitProviders(ctx, "li")
	if err != nil {
		t.Fatal(err)
	}
	if list["gitlab"] != "li" {
		t.Errorf("providers = %v, want the linked username", list)
	}
	for _, v := range list {
		if v == "glpat-second" {
			t.Error("the listing returned the token")
		}
	}
}

// Leaving is a state, not a deletion: removing the row would orphan every
// session, worktree and piece of authorship that points at this person.
func TestDisablingKeepsTheAccount(t *testing.T) {
	db := open(t)
	if err := db.CreateUser(t.Context(), "u1", "a@b.c", "A", "h", false, false); err != nil {
		t.Fatal(err)
	}
	if err := db.SetDisabled(t.Context(), "a@b.c", true); err != nil {
		t.Fatal(err)
	}
	u, _, err := db.UserByEmail(t.Context(), "a@b.c")
	if err != nil {
		t.Fatalf("a disabled account disappeared: %v", err)
	}
	if !u.Disabled {
		t.Error("the account is not reported as disabled")
	}
}
