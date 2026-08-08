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

	"github.com/agentcell/agentcell/pkg/ids"
	"github.com/agentcell/agentcell/pkg/runtimeapi"
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
		// Refresh so the preview of the main checkout tracks the base branch.
		if err := git(ids.RepoPath, "fetch", "origin"); err != nil {
			fmt.Fprintln(os.Stderr, "anchor: fetch failed (continuing with stale checkout)")
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

// supervisePreview keeps the dev server alive with capped backoff. The
// preview target directory is fixed per pod: switching follow-target edits
// the StatefulSet env, which rolls the pod — restart is the reload.
func supervisePreview(argv []string, dir string, stop <-chan os.Signal) {
	backoff := time.Second
	for {
		if _, err := os.Stat(dir); err != nil {
			// Followed worktree not created yet (session still starting);
			// serve the main checkout meanwhile.
			fmt.Printf("anchor: preview target %s absent, serving %s\n", dir, ids.RepoPath)
			dir = ids.RepoPath
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = dir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		start := time.Now()
		err := cmd.Start()
		if err != nil {
			fmt.Fprintf(os.Stderr, "anchor: preview start: %v\n", err)
		} else {
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-stop:
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
				<-done
				return
			case err = <-done:
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
