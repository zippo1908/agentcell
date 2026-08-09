package main

// Real-git tests for the settle contract: these run actual git against
// temp repositories (no cluster needed) and pin the platform's core
// data-safety promise — undelivered commits never count as settled and
// never lose their worktree.

import (
	"os"
	"path/filepath"
	"testing"
)

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if err := git(dir, args...); err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
}

// newFixture builds: a bare origin, a main checkout (repoPath) tracking
// it, and a session worktree with branch session/<id>.
func newFixture(t *testing.T, id string) (repoPath, wt, branch, origin string) {
	t.Helper()
	tmp := t.TempDir()
	origin = filepath.Join(tmp, "origin.git")
	repoPath = filepath.Join(tmp, "repo")
	wt = filepath.Join(tmp, "cells", id)
	branch = "session/" + id

	mustGit(t, tmp, "init", "--bare", origin)
	mustGit(t, tmp, "init", "-b", "main", repoPath)
	if err := os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoPath, "add", "-A")
	mustGit(t, repoPath, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "init")
	mustGit(t, repoPath, "remote", "add", "origin", origin)
	mustGit(t, repoPath, "push", "-u", "origin", "main")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoPath, "worktree", "add", "-b", branch, wt, "main")
	return repoPath, wt, branch, origin
}

func TestSettleProducedPushesAndReclaims(t *testing.T) {
	repoPath, wt, branch, origin := newFixture(t, "aaa")
	if err := os.WriteFile(filepath.Join(wt, "feature.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := settleWorktree(repoPath, wt, branch, "main", "aaa")
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if !v.Produced {
		t.Fatalf("verdict = %+v, want produced", v)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("worktree not reclaimed after confirmed push")
	}
	if _, err := gitOut(origin, "rev-parse", branch); err != nil {
		t.Error("branch missing on origin after produced settle")
	}
}

func TestSettleEmptySessionDiscards(t *testing.T) {
	repoPath, wt, branch, _ := newFixture(t, "bbb")
	v, err := settleWorktree(repoPath, wt, branch, "main", "bbb")
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if v.Produced {
		t.Fatalf("verdict = %+v, want discarded", v)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("empty worktree not reclaimed")
	}
	if _, err := gitOut(repoPath, "rev-parse", "--verify", branch); err == nil {
		t.Error("empty session branch not deleted locally")
	}
}

// The review-critical case: commits exist but the push cannot be
// confirmed. Settle must fail (so the job retries) and must keep the
// worktree; a later retry with the remote back must deliver and reclaim.
func TestSettlePushFailureKeepsWorktreeAndRetrySucceeds(t *testing.T) {
	repoPath, wt, branch, origin := newFixture(t, "ccc")
	if err := os.WriteFile(filepath.Join(wt, "feature.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Break the remote.
	mustGit(t, repoPath, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "nonexistent.git"))

	v, err := settleWorktree(repoPath, wt, branch, "main", "ccc")
	if err == nil {
		t.Fatal("settle succeeded with unconfirmed push — the data-safety contract is broken")
	}
	if v.Produced {
		t.Error("verdict claims produced despite failed push")
	}
	if _, statErr := os.Stat(wt); statErr != nil {
		t.Fatal("worktree was reclaimed despite failed push — work lost")
	}

	// Heal the remote and retry: settle must be idempotent.
	mustGit(t, repoPath, "remote", "set-url", "origin", origin)
	v, err = settleWorktree(repoPath, wt, branch, "main", "ccc")
	if err != nil {
		t.Fatalf("retry settle: %v", err)
	}
	if !v.Produced {
		t.Fatalf("retry verdict = %+v, want produced", v)
	}
	if _, err := gitOut(origin, "rev-parse", branch); err != nil {
		t.Error("branch missing on origin after retry")
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("worktree not reclaimed after successful retry")
	}
}
