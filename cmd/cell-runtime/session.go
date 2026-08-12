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
	wt := ids.WorktreePath(uid, id)
	branch := ids.SessionBranch(id)
	if _, err := os.Stat(wt); os.IsNotExist(err) {
		// 0700 all the way down: a peer's pod runs as a different uid on the
		// same volume, and an unpublished worktree is nobody else's business.
		if err := os.MkdirAll(filepath.Dir(wt), 0o700); err != nil {
			return err
		}
		if err := git(ids.RepoPath, "worktree", "add", "-b", branch, wt, base); err != nil {
			return fmt.Errorf("worktree add: %w", err)
		}
	}

	// Record the work order and product context next to the code, for the
	// agent now and for humans reviewing the settled branch later.
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
	_ = os.WriteFile(filepath.Join(wt, ".agentcell", "TASK.md"), []byte(task), 0o644)

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
