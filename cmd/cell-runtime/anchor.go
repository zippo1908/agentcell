package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

// runAnchor is PID 1 of the anchor pod: it makes the workspace real (clone
// on first boot), then keeps the resident product preview alive for the
// whole life of the Cell so the user can watch the agent's work and
// recalibrate the product description against it.
func runAnchor() error {
	if err := ensureAskpass(); err != nil {
		return err
	}
	if err := ensureClone(); err != nil {
		return err
	}
	// The persistent, session-shared knowledge directory lives on the PVC
	// outside the checkout; sessions read it and distill learnings back.
	_ = os.MkdirAll(runtimeapi.KnowledgePath, 0o755)
	// The anchor holds the project identity, so it is the right process to
	// lay down the directory every user's private tree hangs off: created
	// once, group-writable and sticky, rather than by whichever user happens
	// to start first (which would give it that user's 0700). Sessions repair
	// it if the anchor has not run yet, but this is where it belongs.
	if err := ensureSharedParent(filepath.Dir(ids.UserHome(0))); err != nil {
		fmt.Fprintf(os.Stderr, "anchor: %v\n", err)
	}
	go reapZombies()
	go heartbeat()
	go syncBase(os.Getenv(runtimeapi.EnvRepoBranch))

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	var previewCmd []string
	if raw := os.Getenv(runtimeapi.EnvPreviewCmd); raw != "" && raw != "null" {
		if err := json.Unmarshal([]byte(raw), &previewCmd); err != nil {
			return fmt.Errorf("parse %s: %w", runtimeapi.EnvPreviewCmd, err)
		}
	}
	if len(previewCmd) == 0 {
		fmt.Println("anchor: no preview command configured; idling")
		<-stop
		return nil
	}

	target := os.Getenv(runtimeapi.EnvPreviewTarget)
	if target == "" {
		target = ids.RepoPath
	}
	supervisePreview(previewCmd, target, stop)
	return nil
}

// ensureClone prepares EVERY repository this project is made of.
//
// One failure fails the lot: half a project group is worse than a clean
// error, because an agent would then be looking at a codebase with a piece
// missing and no indication which piece.
func ensureClone() error {
	repos := reposFromEnv()
	for _, r := range repos {
		if len(repos) == 1 {
			// Single-repo project: the URL it always had.
			r.Name = ""
		}
		if err := ensureOneClone(r); err != nil {
			return fmt.Errorf("repo %q: %w", r.Name, err)
		}
	}
	return nil
}

// reposFromEnv reads the project's repositories, falling back to the
// single-repo variables so an existing Cell needs no change at all.
func reposFromEnv() []runtimeapi.Repo {
	if raw := os.Getenv(runtimeapi.EnvRepos); raw != "" {
		var out []runtimeapi.Repo
		if err := json.Unmarshal([]byte(raw), &out); err == nil && len(out) > 0 {
			return out
		}
	}
	return []runtimeapi.Repo{{
		Name:   "main",
		URL:    os.Getenv(runtimeapi.EnvRepoURL),
		Branch: os.Getenv(runtimeapi.EnvRepoBranch),
	}}
}

func ensureOneClone(r runtimeapi.Repo) error {
	url, branch := r.URL, r.Branch
	repoPath := ids.RepoDir(r.Path)
	if url == "" {
		return fmt.Errorf("%s not set", runtimeapi.EnvRepoURL)
	}
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err == nil {
		// Refresh AND advance the local base branch — sessions fork worktrees
		// from the local ref, so fetch alone would leave them on a stale
		// base. The main checkout is a pristine mirror of the remote base by
		// contract (sessions edit worktrees, never this checkout), so a hard
		// reset is the correct semantic.
		if err := git(repoPath, "fetch", "origin"); err != nil {
			fmt.Fprintln(os.Stderr, "anchor: fetch failed (continuing with stale checkout)")
			return nil
		}
		if branch != "" {
			if err := git(repoPath, "reset", "--hard", "origin/"+branch); err != nil {
				fmt.Fprintln(os.Stderr, "anchor: reset to origin/"+branch+" failed (continuing)")
			}
		}
		// Readable by the project, writable only by the anchor (ADR-0012):
		// users commit into their OWN repositories now, which read through
		// to this one via alternates. A group-writable object store was the
		// price of many uids writing here, and that price is no longer due.
		if err := shareRepoReadOnly(); err != nil {
			fmt.Fprintf(os.Stderr, "anchor: sharing the object store failed: %v\n", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		return err
	}
	args := []string{"clone"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, effectiveGitURLFor(url, r.Name), repoPath)
	if err := gitNet("/", args...); err != nil {
		return err
	}
	return shareRepoReadOnly()
}

// shareRepoReadOnly makes the object store readable — and only readable — by
// the project group.
//
// Since ADR-0009 each user's session runs as its own uid while the checkout
// belongs to the project identity, and a session's commits land in the
// SHARED object database. git creates object directories 0755 by default, so
// whichever uid happens to create a prefix directory first owns it and every
// other user gets:
//
//	insufficient permission for adding an object to repository database
//
// It fails by luck of the hash, which makes it look intermittent. This is
// precisely what core.sharedRepository exists for: git then creates
// directories 2775 and files 0664, so the group can write.
//
// The config only governs objects created from now on, so the existing tree
// is relaxed too. Only the owner may chmod, which the anchor is — it did the
// clone.
func shareRepoReadOnly() error {
	// core.sharedRepository is deliberately NOT set: nothing but the anchor
	// writes here any more, and leaving it would keep creating
	// group-writable object directories that nothing needs.
	if err := git(ids.RepoPath, "config", "--unset-all", "core.sharedRepository"); err != nil {
		// Absent is the desired state, and --unset-all says so with exit 5.
		_ = err
	}
	return filepath.Walk(filepath.Join(ids.RepoPath, ".git"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // best effort: a file we cannot stat is one we cannot fix
		}
		mode := info.Mode()
		want := mode&^0o022 | 0o040 // group read; nobody but the owner writes
		if mode.IsDir() {
			want |= 0o010 // traversable
		}
		if want != mode {
			_ = os.Chmod(path, want)
		}
		return nil
	})
}

// syncBase keeps the local base branch tracking the remote so worktrees
// created days into a Cell's life still fork from the true base. The main
// checkout is a pristine mirror by contract (sessions edit worktrees, not
// this checkout), so a periodic hard reset is the correct semantic.
func syncBase(branch string) {
	if branch == "" {
		return
	}
	for {
		time.Sleep(5 * time.Minute)
		if err := git(ids.RepoPath, "fetch", "origin"); err != nil {
			continue
		}
		_ = git(ids.RepoPath, "reset", "--hard", "origin/"+branch)
	}
}

// supervisePreview keeps the dev server alive with capped backoff. The
// follow-target is fixed per pod (env change rolls the pod), but the
// followed worktree may not exist yet at pod start: each cycle re-resolves
// the directory, and while serving the fallback a watcher kicks the server
// the moment the real target appears.
func supervisePreview(argv []string, dir string, stop <-chan os.Signal) {
	backoff := time.Second
	for {
		serveDir := dir
		if _, err := os.Stat(dir); err != nil {
			// Followed worktree not created yet (session still starting);
			// serve the main checkout meanwhile — and switch when it lands.
			fmt.Printf("anchor: preview target %s absent, serving %s until it appears\n", dir, ids.RepoPath)
			serveDir = ids.RepoPath
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = serveDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		// The dev server runs repo-controlled code; it must not inherit the
		// git credentials this supervisor holds for clone/fetch.
		cmd.Env = envWithoutGitCreds()
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		start := time.Now()
		err := cmd.Start()
		if err != nil {
			fmt.Fprintf(os.Stderr, "anchor: preview start: %v\n", err)
		} else {
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			quit := make(chan struct{})
			if serveDir != dir {
				// Serving the fallback: the moment the followed worktree
				// appears, kick the server so the next cycle serves it.
				go func(pid int) {
					for {
						select {
						case <-quit:
							return
						case <-time.After(2 * time.Second):
							if _, err := os.Stat(dir); err == nil {
								fmt.Printf("anchor: preview target %s appeared, switching\n", dir)
								_ = syscall.Kill(-pid, syscall.SIGTERM)
								return
							}
						}
					}
				}(cmd.Process.Pid)
			}
			select {
			case <-stop:
				close(quit)
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
				<-done
				return
			case err = <-done:
				close(quit)
				fmt.Fprintf(os.Stderr, "anchor: preview exited: %v\n", err)
			}
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second // ran fine for a while: reset
		} else if backoff < 30*time.Second {
			backoff *= 2
		}
		select {
		case <-stop:
			return
		case <-time.After(backoff):
		}
	}
}

// reapZombies collects children re-parented onto PID 1.
func reapZombies() {
	sig := make(chan os.Signal, 16)
	signal.Notify(sig, syscall.SIGCHLD)
	for range sig {
		for {
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
		}
	}
}

func heartbeat() {
	dir := "/workspace/.agentcell"
	_ = os.MkdirAll(dir, 0o755)
	for {
		_ = os.WriteFile(filepath.Join(dir, "heartbeat"), []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
		time.Sleep(30 * time.Second)
	}
}
