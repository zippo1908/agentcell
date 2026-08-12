package main

import (
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

// runSession is PID 1 of a session pod: carve a worktree out of the shared
// object store, then hand the task to the agent CLI. The pod's exit —
// clean, crashed or killed — always leads to the settle job; this applet
// never cleans up after itself by design.
func runSession() error {
	id := os.Getenv(runtimeapi.EnvSessionID)
	base := os.Getenv(runtimeapi.EnvBaseBranch)
	if id == "" {
		return fmt.Errorf("%s not set", runtimeapi.EnvSessionID)
	}
	var argv []string
	if err := json.Unmarshal([]byte(os.Getenv("AGENTCELL_AGENT_ARGV")), &argv); err != nil || len(argv) == 0 {
		return fmt.Errorf("AGENTCELL_AGENT_ARGV invalid: %v", err)
	}
	if err := ensureAskpass(); err != nil {
		return err
	}
	if err := waitForRepo(5 * time.Minute); err != nil {
		return err
	}

	// The pod runs as its owner (ADR-0009), so our own uid is the identity
	// everything private belongs to — no need to be told it.
	uid := int64(os.Getuid())
	if err := ensurePrivateHome(uid); err != nil {
		return err
	}
	// After HOME points at the private tree, so the trust lands in this
	// user's own git config rather than a shared one.
	if err := ensureRepoTrusted(ids.RepoPath); err != nil {
		return err
	}
	wt := ids.WorktreePath(uid, id)
	if err := prepareWorktree(wt, id, base); err != nil {
		return err
	}

	// When the user asked to watch this session live, serve the preview from
	// this pod. The worktree is private to its owner, so the anchor cannot
	// serve it — the resident preview for a followed session belongs to the
	// user's own runtime (ADR-0009). The Cell's preview Service selects this
	// pod for as long as it is the followed one.
	var previewArgv []string
	if raw := os.Getenv(runtimeapi.EnvPreviewCmd); raw != "" {
		if err := json.Unmarshal([]byte(raw), &previewArgv); err == nil && len(previewArgv) > 0 {
			stop := make(chan os.Signal, 1)
			signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
			go supervisePreview(previewArgv, wt, stop)
		}
	}

	if os.Getenv(runtimeapi.EnvResident) == "1" {
		return runResident(uid, id, wt, argv)
	}

	fmt.Printf("session %s: running %v in %s\n", id, argv, wt)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = wt
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = nil
	if err := cmd.Run(); err != nil {
		// Non-zero agent exit still settles; surface the cause in the log.
		return fmt.Errorf("agent exited: %w", err)
	}
	return nil
}

// runResident hosts the agent in a tmux server the owner can attach to, and
// keeps the slot alive after it finishes.
//
// The socket lives in the owner's private tree, never in the shared
// /tmp/tmux-<uid> that tmux picks by default. That default is the whole
// problem: it is derived from the uid, so on a shared volume two users could
// end up naming the same socket, and anything that could reach it could
// attach to somebody else's terminal. Application-level separation would not
// help — the socket is the authority.
//
// The agent runs as a command typed into a shell rather than as the window's
// process, so the window survives it. That is what makes "look at what it
// did, then tell it one more thing" possible in the same context instead of
// a fresh session that has to rediscover everything.
func runResident(uid int64, id, wt string, argv []string) error {
	// Say why, once, in a sentence an operator can act on. Without this the
	// pod simply fails and the Session reports "agent finished (Failed)",
	// which points at the agent rather than at the image.
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("resident sessions need tmux, and it is not in this Cell's image (%s): "+
			"install it in the devbox image, or dispatch without resident", os.Getenv("AGENTCELL_IMAGE"))
	}
	sock := ids.TmuxSocket(uid)
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return fmt.Errorf("tmux socket dir: %w", err)
	}
	if err := os.Chmod(filepath.Dir(sock), 0o700); err != nil {
		return err
	}
	window := ids.TmuxWindow(id)
	if out, err := exec.Command("tmux", "-S", sock, "new-session", "-d",
		"-s", window, "-c", wt).CombinedOutput(); err != nil {
		return fmt.Errorf("tmux new-session: %v: %s", err, out)
	}
	// The marker is how anything outside the pod can tell "still working"
	// from "waiting for you" without a Kubernetes token in here (ADR-0005).
	done := runtimeapi.DoneMarker
	_ = os.Remove(done)
	line := shellJoin(argv) + "; printf '%s' \"$?\" > " + shellQuote(done)
	if out, err := exec.Command("tmux", "-S", sock, "send-keys", "-t", window,
		line, "Enter").CombinedOutput(); err != nil {
		return fmt.Errorf("tmux send-keys: %v: %s", err, out)
	}
	fmt.Printf("session %s: resident in tmux %s (socket %s), window %s\n", id, window, sock, window)

	// PID 1 from here on: hold the pod so the slot stays attachable. The
	// controller ends it — TTL, an explicit settle, or the Cell going away —
	// which is what keeps mandatory settle intact.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	for {
		select {
		case <-stop:
			fmt.Println("session: stop requested, leaving the worktree for settle")
			return nil
		case <-time.After(30 * time.Second):
			if err := exec.Command("tmux", "-S", sock, "has-session", "-t", window).Run(); err != nil {
				// The user killed the window: nothing left to attach to, so
				// stop holding the slot and let settle run.
				fmt.Println("session: tmux window gone, releasing the slot")
				return nil
			}
		}
	}
}

// shellJoin renders argv as one command line for send-keys. The agent's own
// arguments are operator- and task-supplied, so they are quoted rather than
// pasted: a task containing a semicolon must not become two commands.
func shellJoin(argv []string) string {
	out := make([]string, 0, len(argv))
	for _, a := range argv {
		out = append(out, shellQuote(a))
	}
	return strings.Join(out, " ")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func waitForRepo(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(ids.RepoPath, ".git")); err == nil {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("repo not cloned after %s (anchor still starting?)", timeout)
}

// ensurePrivateHome creates this user's private tree on the shared project
// volume (ADR-0009).
//
// 0700 is the whole mechanism: the volume is shared with every other user's
// pods, but those pods run as different uids, so the mode bits are what
// actually withhold one person's worktrees, CLI state, transcripts and tmux
// sockets from another. Nothing here may be group- or world-readable, even
// by accident — hence the explicit Chmod: MkdirAll honours the umask and
// would silently leave 0755 where the umask is the usual 022.
func ensurePrivateHome(uid int64) error {
	home := ids.UserHome(uid)
	// The parent is project-layer infrastructure and must be created as
	// such. Letting MkdirAll create it implicitly gives it the 0700 of
	// whichever user arrives first and locks every other user out of the
	// whole tree — a failure that only appears once two people work in one
	// Cell, which is exactly when it matters.
	//
	// Sticky, because /workspace is world-writable: without it any user can
	// delete another's private directory. They still cannot read it, but
	// "cannot read" is not the whole property worth having.
	if err := ensureSharedParent(filepath.Dir(home)); err != nil {
		return err
	}
	for _, dir := range []string{home, filepath.Join(home, "worktrees"), filepath.Join(home, "home")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("private home %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("private home %s: %w", dir, err)
		}
	}
	// Point the agent CLI at it: Codex/Claude/pi all keep configuration,
	// credentials and transcripts under $HOME, and those are exactly the
	// things that must not be shared.
	return os.Setenv("HOME", filepath.Join(home, "home"))
}

// ensureSharedParent creates the directory that holds every user's private
// tree: group-writable so each user can make their own, sticky so only the
// owner can remove it.
//
// Chmod failures are tolerated: if the directory already exists and belongs
// to the project identity, we are not its owner and cannot chmod it — but it
// already has the mode we want, so there is nothing to fix.
func ensureSharedParent(dir string) error {
	if err := os.MkdirAll(dir, 0o775); err != nil {
		return fmt.Errorf("users directory %s: %w", dir, err)
	}
	_ = os.Chmod(dir, 0o775|os.ModeSticky)
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o070 == 0 {
		return fmt.Errorf("users directory %s is %v — not group-writable, so other users cannot create their own trees", dir, info.Mode())
	}
	return nil
}

// runTell types another instruction into this session's tmux window.
//
// It runs INSIDE the session pod, as the session's own user, so it needs no
// credential and cannot address anyone else's tmux: the socket path is
// derived from the uid it is running as, and that socket is 0700.
//
// The text arrives as one argv element and is handed to tmux as one
// argument. It is never spliced into a shell string — it is user input, and a
// semicolon in a task description must stay a semicolon rather than becoming
// the start of another command.
func runTell(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("tell: need <session-id> <argv...>")
	}
	id, args := args[0], args[1:]
	sock := ids.TmuxSocket(int64(os.Getuid()))
	if err := sendCommand(sock, ids.TmuxWindow(id), args, true, id, ""); err != nil {
		return err
	}
	fmt.Println("sent")
	return nil
}

// runAttach attaches the caller's terminal to this session's tmux window.
//
// Everything it needs — the socket path and the window name — is derived
// inside the pod from the uid it runs as and the session id in the
// environment. That is deliberate: an operator attaching should not have to
// know, or be told, another user's private paths.
func runAttach(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("attach: need <session-id>")
	}
	id := args[0]
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("attach: tmux is not in this image")
	}
	sock := ids.TmuxSocket(int64(os.Getuid()))
	// Replace this process so the terminal is tmux's, not ours.
	return syscall.Exec(tmux, []string{"tmux", "-S", sock, "attach", "-t", ids.TmuxWindow(id)}, os.Environ())
}
