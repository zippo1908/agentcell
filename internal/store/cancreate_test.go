package store

import (
	"path/filepath"
	"testing"
)

// An upgrade must not quietly take away what people already had.
//
// Before this column existed, anyone with an account could create a project.
// Adding the grant with a default of 0 would have demoted every existing
// colleague at the moment of an upgrade, with no message and nothing in the
// UI to explain why the button had stopped working.
func TestExistingAccountsKeepTheRightToCreateProjects(t *testing.T) {
	db := open(t)
	ctx := t.Context()

	// A row written the way the previous schema wrote it: no such column.
	if _, err := db.sql.ExecContext(ctx,
		`INSERT INTO users (id, email, name, password, is_admin, created_at)
		 VALUES ('u-old','old@tinci.com','Old','hash',0,0)`); err != nil {
		t.Fatal(err)
	}

	u, _, err := db.UserByEmail(ctx, "old@tinci.com")
	if err != nil {
		t.Fatal(err)
	}
	if !u.CanCreate {
		t.Fatal("an account that predates the grant must keep it; this upgrade demoted somebody")
	}
}

// Opening the same database twice must not fail on the ALTER.
func TestSchemaUpgradeIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twice.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopening an already-migrated database must work: %v", err)
	}
	_ = second.Close()
}

func TestTheGrantSurvivesARoundTrip(t *testing.T) {
	db := open(t)
	ctx := t.Context()

	if err := db.CreateUser(ctx, "u-a", "a@tinci.com", "A", "h", false, true); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateUser(ctx, "u-b", "b@tinci.com", "B", "h", false, false); err != nil {
		t.Fatal(err)
	}

	a, _, _ := db.UserByEmail(ctx, "a@tinci.com")
	b, _, _ := db.UserByEmail(ctx, "b@tinci.com")
	if !a.CanCreate {
		t.Error("a granted account came back without the grant")
	}
	if b.CanCreate {
		t.Error("an account invited without the grant came back with it")
	}

	// And the list view — the one an admin reads — must agree with the row.
	users, err := db.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		want := u.Email == "a@tinci.com"
		if u.CanCreate != want {
			t.Errorf("%s: list says CanCreate=%v, want %v", u.Email, u.CanCreate, want)
		}
	}
}

func TestWithdrawingTheGrant(t *testing.T) {
	db := open(t)
	ctx := t.Context()
	if err := db.CreateUser(ctx, "u-a", "a@tinci.com", "A", "h", false, true); err != nil {
		t.Fatal(err)
	}
	if err := db.SetCanCreate(ctx, "a@tinci.com", false); err != nil {
		t.Fatal(err)
	}
	if u, _, _ := db.UserByEmail(ctx, "a@tinci.com"); u.CanCreate {
		t.Fatal("withdrawing the grant did not stick")
	}
	if err := db.SetCanCreate(ctx, "nobody@tinci.com", true); err != ErrNotFound {
		t.Fatalf("granting to nobody must say so, got %v", err)
	}
}
