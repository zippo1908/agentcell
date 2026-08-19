package webui

import "testing"

// The grant has to survive the trip from "I am inviting Li and Li should be
// able to start projects" to Li's account.
//
// Carrying it on the invitation is the whole point: the decision is made by
// the person doing the inviting, at the moment they invite, rather than
// being a second act somebody has to remember after Li has already logged in
// and found the button does nothing.
func TestAnInvitationCarriesTheRightToCreateProjects(t *testing.T) {
	a := accountsFixture(t)
	ctx := t.Context()

	tok, err := a.Invite(ctx, "li@tinci.com", "Li", "boss@tinci.com", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Redeem(ctx, tok, "Li", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}

	u, _, err := a.DB.UserByEmail(ctx, "li@tinci.com")
	if err != nil {
		t.Fatal(err)
	}
	if !u.CanCreate {
		t.Fatal("the invitation granted it and the account did not get it")
	}
	if u.Admin {
		t.Fatal("creating projects is not being an administrator")
	}
}

func TestAnInvitationWithoutTheGrantDoesNotConferIt(t *testing.T) {
	a := accountsFixture(t)
	ctx := t.Context()

	tok, err := a.Invite(ctx, "wang@tinci.com", "Wang", "boss@tinci.com", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Redeem(ctx, tok, "Wang", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}

	u, _, err := a.DB.UserByEmail(ctx, "wang@tinci.com")
	if err != nil {
		t.Fatal(err)
	}
	if u.CanCreate {
		t.Fatal("an invitation that granted nothing produced an account that can create projects")
	}
}

// An administrator can always create, so inviting one without ticking the
// other box must not produce an admin who cannot start anything.
func TestAnAdministratorCanAlwaysCreate(t *testing.T) {
	a := accountsFixture(t)
	ctx := t.Context()

	tok, err := a.Invite(ctx, "root@tinci.com", "Root", "boss@tinci.com", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Redeem(ctx, tok, "Root", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}

	u, _, err := a.DB.UserByEmail(ctx, "root@tinci.com")
	if err != nil {
		t.Fatal(err)
	}
	if !u.CanCreate {
		t.Fatal("an administrator who cannot create a project is a contradiction")
	}
}

// The first account on a deployment has nobody to grant it anything.
func TestTheBootstrapAdminCanCreate(t *testing.T) {
	a := accountsFixture(t)
	ctx := t.Context()

	if err := a.Bootstrap(ctx, "first@tinci.com", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	u, _, err := a.DB.UserByEmail(ctx, "first@tinci.com")
	if err != nil {
		t.Fatal(err)
	}
	if !u.CanCreate {
		t.Fatal("the only account on a fresh deployment could not start a project")
	}
}
