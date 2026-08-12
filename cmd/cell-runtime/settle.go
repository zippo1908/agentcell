package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

// runSettle is the mandatory reckoning after every session. Data-safety
// contract (the platform's core promise):
//
//   - commits that exist but were NOT confirmed on the remote never lead
//     to a successful settle — the job fails and retries, the worktree
//     stays on disk untouched;
//   - the worktree is removed only after a confirmed push (produced) or
//     when it verifiably contains no commits (discarded);
//   - settle is idempotent: a retry after a crash re-runs add/commit
//     (no-ops), re-pushes (no-op if already delivered), then reclaims.
func runSettle() error {
	id := os.Getenv(runtimeapi.EnvSessionID)
	base := os.Getenv(runtimeapi.EnvBaseBranch)
	if id == "" {
		return fmt.Errorf("%s not set", runtimeapi.EnvSessionID)
	}
	if err := ensureAskpass(); err != nil {
		return err
	}
	v, err := settleWorktree(ids.RepoPath, ids.WorktreePath(id), ids.SessionBranch(id), base, id,
		effectiveGitURL(os.Getenv(runtimeapi.EnvRepoURL)))
	raw, _ := json.Marshal(v)
	// Termination message is the transport back to the controller; write it
	// on failure too so a final failed attempt still explains itself.
	if werr := os.WriteFile(runtimeapi.SettleResultPath, raw, 0o644); werr != nil {
		fmt.Fprintf(os.Stderr, "settle: cannot write termination message: %v\n", werr)
	}
	fmt.Printf("settle %s: %s\n", id, raw)
	// A non-nil error fails the job pod, so Kubernetes retries the settle
	// with the worktree still in place. Success is never reported for
	// undelivered work.
	return err
}

type verdict struct {
	Produced bool   `json:"produced"`
	Branch   string `json:"branch"`
	Message  string `json:"message"`
}

// settleWorktree settles one session worktree. It returns an error for
// every outcome that must NOT count as a completed settle (commits present
// but push unconfirmed, or state that could not be determined) — in those
// cases the worktree is always left on disk.
//
// pushURL, when set, is the ONLY destination pushed to: the worktree's
// remote configuration is attacker-writable (sessions share the .git), so
// a credentialed push must never trust it.
func settleWorktree(repoPath, wt, branch, base, id, pushURL string) (verdict, error) {
	if _, err := os.Stat(wt); os.IsNotExist(err) {
		return verdict{Produced: false, Message: "worktree absent (session never started)"}, nil
	}

	// Autosave anything the agent left dirty. Failures here are fatal for
	// this attempt: proceeding could misjudge the commit count below.
	if err := git(wt, "add", "-A"); err != nil {
		return verdict{Branch: branch, Message: "git add failed"}, fmt.Errorf("git add: %w", err)
	}
	status, err := gitOut(wt, "status", "--porcelain")
	if err != nil {
		return verdict{Branch: branch, Message: "git status failed"}, fmt.Errorf("git status: %w", err)
	}
	if status != "" {
		if err := git(wt, "-c", "user.name=agentcell", "-c", "user.email=agentcell@local",
			"commit", "-m", "agentcell: session "+id+" autosave"); err != nil {
			return verdict{Branch: branch, Message: "autosave commit failed"}, fmt.Errorf("git commit: %w", err)
		}
	}

	// Determine whether the session produced commits. If this cannot be
	// determined, keep everything: deleting a worktree on an unknown state
	// is how work gets lost.
	out, err := gitOut(wt, "rev-list", "--count", base+".."+branch)
	if err != nil {
		return verdict{Branch: branch, Message: "rev-list failed — worktree kept"},
			fmt.Errorf("rev-list %s..%s: %w", base, branch, err)
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return verdict{Branch: branch, Message: "rev-list output unparseable — worktree kept"},
			fmt.Errorf("rev-list output %q: %w", out, err)
	}

	produced := false
	msg := "no commits produced"
	if n > 0 {
		dest := pushURL
		if dest == "" {
			dest = "origin"
		}
		// Push must be confirmed before anything is reclaimed or reported
		// settled. On failure the job retries with the worktree intact.
		if err := git(wt, "push", dest, branch); err != nil {
			return verdict{Branch: branch,
					Message: fmt.Sprintf("%d commit(s) present but push unconfirmed — worktree kept for retry", n)},
				fmt.Errorf("git push: %w", err)
		}
		produced = true
		msg = fmt.Sprintf("pushed %d commit(s)", n)
	}

	// Reclaim only now: either the work is confirmed on the remote, or the
	// worktree verifiably holds no commits.
	if err := git(repoPath, "worktree", "remove", "--force", wt); err != nil {
		_ = os.RemoveAll(wt)
		_ = git(repoPath, "worktree", "prune")
	}
	if !produced {
		_ = git(repoPath, "branch", "-D", branch)
	}
	return verdict{Produced: produced, Branch: branch, Message: msg}, nil
}
