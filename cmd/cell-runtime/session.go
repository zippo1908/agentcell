package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

	wt := ids.WorktreePath(id)
	branch := ids.SessionBranch(id)
	if _, err := os.Stat(wt); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
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
