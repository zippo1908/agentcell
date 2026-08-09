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

func ensureClone() error {
	url := os.Getenv(runtimeapi.EnvRepoURL)
	branch := os.Getenv(runtimeapi.EnvRepoBranch)
	if url == "" {
		return fmt.Errorf("%s not set", runtimeapi.EnvRepoURL)
	}
	if _, err := os.Stat(filepath.Join(ids.RepoPath, ".git")); err == nil {
		// Refresh AND advance the local base branch — sessions fork worktrees
		// from the local ref, so fetch alone would leave them on a stale
		// base. The main checkout is a pristine mirror of the remote base by
		// contract (sessions edit worktrees, never this checkout), so a hard
		// reset is the correct semantic.
		if err := git(ids.RepoPath, "fetch", "origin"); err != nil {
			fmt.Fprintln(os.Stderr, "anchor: fetch failed (continuing with stale checkout)")
			return nil
		}
		if branch != "" {
			if err := git(ids.RepoPath, "reset", "--hard", "origin/"+branch); err != nil {
				fmt.Fprintln(os.Stderr, "anchor: reset to origin/"+branch+" failed (continuing)")
			}
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(ids.RepoPath), 0o755); err != nil {
		return err
	}
	args := []string{"clone"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, url, ids.RepoPath)
	return git("/", args...)
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
