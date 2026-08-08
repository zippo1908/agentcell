package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

// runSettle is the mandatory reckoning after every session: commit
// whatever the agent left uncommitted, push the branch if the session
// produced anything, then reclaim the worktree. The verdict goes to the
// termination message so the controller can set Settled vs Discarded.
func runSettle() error {
	id := os.Getenv(runtimeapi.EnvSessionID)
	base := os.Getenv(runtimeapi.EnvBaseBranch)
	if id == "" {
		return fmt.Errorf("%s not set", runtimeapi.EnvSessionID)
	}
	if err := ensureAskpass(); err != nil {
		return err
	}
	verdict := settleWorktree(id, base)
	raw, _ := json.Marshal(verdict)
	// Termination message is the transport back to the controller.
	if err := os.WriteFile(runtimeapi.SettleResultPath, raw, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "settle: cannot write termination message: %v\n", err)
	}
	fmt.Printf("settle %s: %s\n", id, raw)
	return nil
}

type verdict struct {
	Produced bool   `json:"produced"`
	Branch   string `json:"branch"`
	Message  string `json:"message"`
}

func settleWorktree(id, base string) verdict {
	wt := ids.WorktreePath(id)
	branch := ids.SessionBranch(id)

	if _, err := os.Stat(wt); os.IsNotExist(err) {
		return verdict{Produced: false, Message: "worktree absent (session never started)"}
	}
	// Autosave anything the agent left dirty.
	_ = git(wt, "add", "-A")
	if out, _ := gitOut(wt, "status", "--porcelain"); out != "" {
		_ = git(wt, "-c", "user.name=agentcell", "-c", "user.email=agentcell@local",
			"commit", "-m", "agentcell: session "+id+" autosave")
	}

	produced := false
	msg := "no commits produced"
	if out, err := gitOut(wt, "rev-list", "--count", base+".."+branch); err == nil {
		if n, _ := strconv.Atoi(out); n > 0 {
			if err := git(wt, "push", "-u", "origin", branch); err != nil {
				// Keep the worktree: commits exist but could not be pushed;
				// a rerun of the job retries the push.
				return verdict{Produced: true, Branch: branch,
					Message: fmt.Sprintf("%d commit(s) but push failed — worktree kept for retry", n)}
			}
			produced = true
			msg = fmt.Sprintf("pushed %d commit(s)", n)
		}
	}

	// Reclaim: a resident Cell never accumulates garbage.
	if err := git(ids.RepoPath, "worktree", "remove", "--force", wt); err != nil {
		_ = os.RemoveAll(wt)
		_ = git(ids.RepoPath, "worktree", "prune")
	}
	if !produced {
		_ = git(ids.RepoPath, "branch", "-D", branch)
	}
	return verdict{Produced: produced, Branch: branch, Message: msg}
}
