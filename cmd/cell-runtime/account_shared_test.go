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

// A second session must not roll the shared login back to whatever the
// control plane last stored: the live file has been refreshed since, and the
// stored copy is older by construction.
func TestASecondSessionDoesNotOverwriteALiveCredential(t *testing.T) {
	shared := t.TempDir()
	live := filepath.Join(shared, "kimi-code.json")
	if err := os.WriteFile(live, []byte(`{"refresh_token":"current"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	empty, err := dirEmpty(shared)
	if err != nil {
		t.Fatal(err)
	}
	if empty {
		t.Fatal("a directory holding a live credential must not read as empty")
	}

	// And the guard the other way: a first session finds nothing and unpacks.
	if empty, err := dirEmpty(t.TempDir()); err != nil || !empty {
		t.Fatalf("a fresh directory should be empty: %v %v", empty, err)
	}
}

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
