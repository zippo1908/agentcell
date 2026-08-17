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
	if err := ensurePrivateHome(int64(os.Getuid())); err != nil {
		return err
	}
	// Both: the user's own repository is where the work is, and it reads
	// through alternates into the shared mirror, which git also checks.
	uid := int64(os.Getuid())
	if err := ensureRepoTrusted(ids.RepoPath); err != nil {
		return err
	}
	if err := ensureRepoTrusted(ids.UserRepoPath(uid)); err != nil {
		return err
	}
	// One repository or several: each settles on its own, and each result is
	// reported on its own. The repositories are separate on the forge — own
	// remote, own history, own reviewers — so a session that changed two of
	// them produces two branches and two verdicts, and somebody may
	// reasonably take one and not the other.
	repos := reposFromEnv()
	v, err := settleAll(uid, id, base, repos)
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
	// Repo names which repository this verdict is about, empty on the
	// single-repo form so nothing about it changes.
	Repo string `json:"repo,omitempty"`
	// Repos carries the per-repository results of a project group. The
	// controller turns these into one reviewable output each.
	Repos    []verdict `json:"repos,omitempty"`
	Produced bool      `json:"produced"`
	Branch   string    `json:"branch"`
	Message  string    `json:"message"`
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
			// Carry the cause into the verdict: this surfaces on the
			// Session status, and "autosave commit failed" alone sent this
			// exact bug (an unwritable shared object store) looking like an
			// intermittent flake.
			return verdict{Branch: branch, Message: "autosave commit failed: " + err.Error()},
				fmt.Errorf("git commit: %w", err)
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

// settleAll settles every repository a session touched.
//
// A repository the agent did not change settles as "produced nothing", which
// is the ordinary case rather than a failure: a task usually touches part of
// a project, not all of it. The overall result is produced if ANY repository
// produced, because that is what decides whether there is something to
// review at all.
func settleAll(uid int64, id, base string, repos []runtimeapi.Repo) (verdict, error) {
	if len(repos) == 1 && repos[0].Path == "" {
		// Exactly what it always did, byte for byte.
		return settleWorktree(ids.UserRepoPath(uid), ids.WorktreePath(uid, id),
			ids.SessionBranch(id), base, id, effectiveGitURLFor(repos[0].URL, ""))
	}
	out := verdict{}
	var firstErr error
	for _, r := range repos {
		b := r.Branch
		if b == "" {
			b = base
		}
		name := r.Name
		if len(repos) == 1 {
			name = ""
		}
		one, err := settleWorktree(
			ids.UserRepoDirFor(uid, r.Path),
			ids.WorktreeDirFor(uid, id, r.Path),
			ids.SessionBranch(id), b, id, effectiveGitURLFor(r.URL, name))
		one.Repo = r.Name
		out.Repos = append(out.Repos, one)
		if err != nil && firstErr == nil {
			// Keep going: a repository that cannot be pushed must not stop
			// the others from being delivered. The error is still returned,
			// so the job fails and Kubernetes retries with the worktrees
			// still in place — nothing is reported as settled that is not.
			firstErr = fmt.Errorf("repo %q: %w", r.Name, err)
		}
		if one.Produced {
			out.Produced = true
		}
	}
	out.Message = summarise(out.Repos)
	return out, firstErr
}

func summarise(rs []verdict) string {
	made := 0
	for _, r := range rs {
		if r.Produced {
			made++
		}
	}
	return fmt.Sprintf("%d/%d 个仓库有产出", made, len(rs))
}
