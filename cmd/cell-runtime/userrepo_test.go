package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// The property ADR-0012 buys, on real git: a user's unpublished commit lands
// in THEIR object store and not in the shared one, which is what stops a
// peer's agent from reading work that has not been published.
//
// This is the whole reason a mediating git daemon was judged unnecessary —
// so it is worth proving against git itself rather than asserting.
func TestUnpublishedCommitsStayInTheUsersOwnObjectStore(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, root, "init", "-q", "-b", "main", shared)
	if err := os.WriteFile(filepath.Join(shared, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, shared, "add", "-A")
	run(t, shared, "commit", "-qm", "base")

	// A user's own repository over the shared base.
	user := filepath.Join(root, "user")
	run(t, root, "clone", "-q", "--shared", "--no-checkout", shared, user)

	// Alternates is what makes reads resolve through while writes stay here.
	alts, err := os.ReadFile(filepath.Join(user, ".git", "objects", "info", "alternates"))
	if err != nil {
		t.Fatalf("no alternates: the clone is not sharing the base: %v", err)
	}
	if !strings.Contains(string(alts), filepath.Join(shared, ".git", "objects")) {
		t.Errorf("alternates points elsewhere: %q", alts)
	}

	// Work in a worktree of the USER repository, as a session does.
	wt := filepath.Join(root, "wt")
	run(t, user, "worktree", "add", "-q", "-b", "session/x", wt, "origin/main")
	if err := os.WriteFile(filepath.Join(wt, "SECRET.md"), []byte("unpublished\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wt, "add", "-A")
	run(t, wt, "commit", "-qm", "unpublished work")
	sha := strings.TrimSpace(run(t, wt, "rev-parse", "HEAD"))

	// The user's own repository has it...
	if out := run(t, user, "cat-file", "-t", sha); strings.TrimSpace(out) != "commit" {
		t.Fatalf("the user's repository lost its own commit: %q", out)
	}
	// ...and the SHARED store does not. This is the leak that existed when
	// sessions were worktrees of the shared repository.
	cmd := exec.Command("git", "cat-file", "-e", sha)
	cmd.Dir = shared
	cmd.Env = append(os.Environ(), "GIT_ALTERNATE_OBJECT_DIRECTORIES=")
	if err := cmd.Run(); err == nil {
		t.Error("an unpublished commit reached the shared object store: every other user can read it")
	}

	// And the base is still readable from the user's repository — a member is
	// entitled to the project's published history.
	if out := run(t, wt, "log", "--oneline", "origin/main"); !strings.Contains(out, "base") {
		t.Errorf("published history is not reachable: %q", out)
	}
}
