package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The session's credentials directory must be a LINK, not a copy.
//
// A copy is what the platform had: KIMI_CODE_HOME pointed at the session, so
// every session got its own login, and since the provider issues a new
// refresh token on each renewal those copies drifted into separate lineages.
// The failure is quiet — each session works, and the control plane has
// several "current" credentials to choose between.
func TestASessionLinksToThePersonsCredential(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(t.TempDir(), "credentials")
	if err := os.MkdirAll(shared, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := linkSharedCredentials(home, shared); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(home, "credentials")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("the session's credentials directory is not a link: %v", err)
	}
	if target != shared {
		t.Fatalf("linked to %q, want %q", target, shared)
	}
}

// An older session left a real directory there. Leaving it would mean this
// session keeps refreshing its own copy while believing it shares.
func TestARealDirectoryIsReplacedByTheLink(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(t.TempDir(), "credentials")
	if err := os.MkdirAll(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(home, "credentials")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "kimi-code.json"), []byte(`{"old":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := linkSharedCredentials(home, shared); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Readlink(stale); err != nil {
		t.Fatalf("a stale real directory survived: %v", err)
	}
}

// Running twice is not two links.
func TestLinkingIsIdempotent(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(t.TempDir(), "credentials")
	if err := os.MkdirAll(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := linkSharedCredentials(home, shared); err != nil {
			t.Fatal(err)
		}
	}
	target, err := os.Readlink(filepath.Join(home, "credentials"))
	if err != nil || target != shared {
		t.Fatalf("after three runs: %q %v", target, err)
	}
}

// The rollback property this file used to guard lives in
// account_credential_test.go now.
//
// It was checked here through dirEmpty: "the directory is not empty, so do
// not unpack". That guard was too blunt — it could not tell a stale snapshot
// of the SAME login from a NEW one, so a person who reconnected their account
// never received the new credential and saw "reconnected and it still does
// not work". installAccountCredential replaced it with the blob hash as a
// version, and TestInstallAccountCredential covers both directions: the same
// blob must not roll a rotated file back, and a different blob must land.
//
// The test that used to be here was removed rather than kept, because
// dirEmpty no longer has a caller: a green test over a function nothing
// calls is confidence about nothing.

// moveInto carries the unpacked credential across without leaving a copy.
func TestMoveIntoLeavesNothingBehind(t *testing.T) {
	from := filepath.Join(t.TempDir(), "credentials")
	to := t.TempDir()
	if err := os.MkdirAll(from, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "kimi-code.json"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := moveInto(from, to); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(to, "kimi-code.json")); err != nil {
		t.Fatalf("the credential did not arrive: %v", err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Error("the source directory survived; a stale copy of a login is exactly what this removes")
	}
}
