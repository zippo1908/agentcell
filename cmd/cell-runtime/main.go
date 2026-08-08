// cell-runtime is the multi-call binary that runs inside every Cell
// container. Applets are selected by the first argument (or argv[0] symlink):
//
//	init        container PID 1: umask, zombie reaping, heartbeat, tmux healing,
//	            per-slot cgroup subtrees (cgroup v2 delegation from quadlet)
//	slot        create / settle / reclaim a session slot (worktree + tmux + cgroup)
//	agent-entry launch and supervise the agent process of one session
//
// The binary is statically linked (CGO_ENABLED=0) so it can be bind-mounted
// read-only into any container image.
//
// M0 stub: real applets land in M3/M4.
package main

import (
	"fmt"
	"os"

	"github.com/agentcell/agentcell/internal/version"
)

func main() {
	applet := ""
	if len(os.Args) > 1 {
		applet = os.Args[1]
	}
	switch applet {
	case "--version":
		fmt.Println("cell-runtime", version.String())
	case "init", "slot", "agent-entry":
		fmt.Fprintf(os.Stderr, "cell-runtime %s: not implemented yet (M3/M4); see docs/PLAN.md\n", applet)
		os.Exit(1)
	default:
		fmt.Fprintln(os.Stderr, "usage: cell-runtime <init|slot|agent-entry|--version>")
		os.Exit(2)
	}
}
