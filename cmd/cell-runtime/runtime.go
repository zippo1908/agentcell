package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

// runUserRuntime is PID 1 of a user's runtime pod: one tmux server, holding
// every session that user has open in this Cell.
//
// One server per user rather than one per session. The agent CLIs already
// manage conversations — Claude Code by an id the caller can choose, Codex by
// its own bookkeeping — so a second layer of per-session processes buys
// nothing and costs a pod each. What the platform owes them is a private
// $HOME to keep that state in and a terminal that outlives any single run.
//
// The runtime holds no credential: model keys arrive per window, over the
// exec channel, at the moment a session starts.
func runUserRuntime() error {
	uid := int64(os.Getuid())
	if err := ensurePrivateHome(uid); err != nil {
		return err
	}
	if err := ensureRepoTrusted(ids.RepoPath); err != nil {
		return err
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("the user runtime needs tmux, and it is not in this Cell's image: " +
			"install it in the devbox image")
	}
	sock := ids.TmuxSocket(uid)
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(sock), 0o700); err != nil {
		return err
	}
	// A server with no sessions exits, so it is held open by one that is
	// never handed out. Windows for real sessions are added beside it.
	if out, err := tmux(sock, "new-session", "-d", "-s", ids.TmuxHolder, "-c", ids.RepoPath); err != nil {
		return fmt.Errorf("tmux start: %v: %s", err, out)
	}
	fmt.Printf("user runtime: tmux server on %s\n", sock)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	for {
		select {
		case <-stop:
			fmt.Println("user runtime: stopping; worktrees stay for settle")
			return nil
		case <-time.After(30 * time.Second):
			// Reap: PID 1 in a pod inherits orphans from every window.
			for {
				var ws syscall.WaitStatus
				if pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil); pid <= 0 || err != nil {
					break
				}
			}
		}
	}
}

// runWindowOpen starts one session inside the user's runtime: its worktree,
// its window, its agent.
//
// Invoked by the control plane over exec. Everything that is not secret
// arrives in argv; the model credential arrives on stdin as KEY=VALUE lines
// and is set on the window alone. A key in argv would be readable from
// /proc by every other window this user has open — same user, but a session
// is still the boundary a credential is scoped to.
func runWindowOpen(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("window-open: need <session-id> <agent argv...>")
	}
	id, argv := args[0], args[1:]
	uid := int64(os.Getuid())
	if err := ensurePrivateHome(uid); err != nil {
		return err
	}
	// Read stdin BEFORE anything that needs a session value. An exec inherits
	// the runtime pod's environment, which is deliberately empty of
	// per-session detail — the task text, the base branch and the model key
	// all arrive here, and the briefing would otherwise be written blank.
	env, err := readEnvFromStdin()
	if err != nil {
		return err
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	wt := ids.WorktreePath(uid, id)
	if err := prepareWorktree(wt, id, os.Getenv(runtimeapi.EnvBaseBranch)); err != nil {
		return err
	}
	sock := ids.TmuxSocket(uid)
	window := ids.TmuxWindow(id)
	// Idempotent: a reconciler retries, and a second window for one session
	// would run the agent twice against the same worktree.
	if out, err := tmux(sock, "list-windows", "-a", "-F", "#{window_name}"); err == nil {
		for _, name := range strings.Split(out, "\n") {
			if strings.TrimSpace(name) == window {
				fmt.Printf("window %s already open\n", window)
				return nil
			}
		}
	}
	// The environment goes through a 0600 file the window sources and then
	// unlinks — NOT through `tmux new-window -e`, which would put the model
	// key in the tmux client's argv and therefore in /proc for every other
	// window this user has open. That is the exposure this whole path exists
	// to avoid; putting it back one layer down would be pointless.
	envFile := filepath.Join(ids.UserHome(uid), "env-"+id)
	if err := writeEnvFile(envFile, env); err != nil {
		return err
	}
	if out, err := tmux(sock, "new-window", "-d", "-t", ids.TmuxHolder, "-n", window, "-c", wt); err != nil {
		_ = os.Remove(envFile)
		return fmt.Errorf("tmux new-window: %v: %s", err, out)
	}
	if err := sendCommand(sock, window, argv, true, id, envFile); err != nil {
		return err
	}
	fmt.Printf("window %s opened in %s\n", window, wt)
	return nil
}

// writeEnvFile lays down the window's environment, readable only by its
// owner. Created with O_EXCL so a stale file from a previous attempt cannot
// be silently reused, and 0600 before anything is written to it.
func writeEnvFile(path string, env []string) error {
	_ = os.Remove(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("window environment: %w", err)
	}
	defer f.Close()
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, err := fmt.Fprintf(f, "export %s=%s\n", k, shellQuote(v)); err != nil {
			return err
		}
	}
	return nil
}

// runWindowClose ends a session's window. The worktree is left alone: settle
// owns it, and reclaiming work is never this applet's job.
func runWindowClose(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("window-close: need <session-id>")
	}
	sock := ids.TmuxSocket(int64(os.Getuid()))
	// Missing is success: this runs on the way to settle, and a window that
	// is already gone is the state we wanted.
	_, _ = tmux(sock, "kill-window", "-t", ids.TmuxWindow(args[0]))
	return nil
}

// sendCommand types a command into a window. resetDone clears the completion
// marker first, so "is it still working?" answers about this run.
func sendCommand(sock, window string, argv []string, resetDone bool, sessionID, envFile string) error {
	line := shellJoin(argv)
	if resetDone {
		marker := shellQuote(runtimeapi.DoneMarkerFor(sessionID))
		line = "rm -f " + marker + "; " + line + "; printf '%s' \"$?\" > " + marker
	}
	if envFile != "" {
		// Sourced and unlinked in one go: the key lives in the window's
		// environment, and nowhere on disk a moment longer than it must.
		q := shellQuote(envFile)
		line = ". " + q + "; rm -f " + q + "; " + line
	}
	// C-u first: never concatenate onto a half-typed line.
	if out, err := tmux(sock, "send-keys", "-t", window, "C-u"); err != nil {
		return fmt.Errorf("tmux send-keys: %v: %s", err, out)
	}
	if out, err := tmux(sock, "send-keys", "-t", window, "-l", line); err != nil {
		return fmt.Errorf("tmux send-keys: %v: %s", err, out)
	}
	if out, err := tmux(sock, "send-keys", "-t", window, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys Enter: %v: %s", err, out)
	}
	return nil
}

// readEnvFromStdin collects KEY=VALUE lines. Secrets travel this way rather
// than in argv, and the reader stops at EOF so an empty stdin is fine.
func readEnvFromStdin() ([]string, error) {
	var out []string
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !strings.Contains(line, "=") {
			return nil, fmt.Errorf("window-open: stdin must be KEY=VALUE lines")
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

func tmux(sock string, args ...string) (string, error) {
	full := append([]string{"-S", sock}, args...)
	b, err := exec.Command("tmux", full...).CombinedOutput()
	return string(b), err
}

// prepareWorktree carves the session's worktree and lays down its briefing.
// Shared by the one-shot session applet and the runtime, so both produce the
// same thing.
func prepareWorktree(wt, id, base string) error {
	branch := ids.SessionBranch(id)
	if _, err := os.Stat(wt); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(wt), 0o700); err != nil {
			return err
		}
		if err := git(ids.RepoPath, "worktree", "add", "-b", branch, wt, base); err != nil {
			return fmt.Errorf("worktree add: %w", err)
		}
	}
	_ = os.MkdirAll(filepath.Join(wt, ".agentcell"), 0o755)
	task := "# 本单任务\n\n" + os.Getenv(runtimeapi.EnvTask) + "\n"
	if desc := os.Getenv(runtimeapi.EnvDescription); desc != "" {
		_ = os.WriteFile(filepath.Join(wt, ".agentcell", "PRODUCT.md"),
			[]byte("# 产品描述(用户随预览持续校准)\n\n"+desc+"\n"), 0o644)
		task += "\n产品整体描述见 `.agentcell/PRODUCT.md`,以它为准对齐方向。\n"
	}
	if _, err := os.Stat(runtimeapi.KnowledgePath); err == nil {
		task += "\n项目持久知识库在 `" + runtimeapi.KnowledgePath + "/`(跨会话共享):" +
			"开工前浏览;本单学到的可复用经验(约定、坑、决策)以 md 文件沉淀回去。\n"
	}
	return os.WriteFile(filepath.Join(wt, ".agentcell", "TASK.md"), []byte(task), 0o644)
}

var _ = json.Marshal
