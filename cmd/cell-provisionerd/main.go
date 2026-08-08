// cell-provisionerd is the only root component of AgentCell. It listens on a
// group-restricted Unix domain socket and exposes exclusively typed gRPC
// operations: unix user + subuid provisioning, storage layout, quadlet
// rendering, session slot create/reclaim, the git broker, and the reaper.
//
// Contract iron rule: every RPC receives ids and typed configuration only —
// never a command string, never a host path chosen by the caller.
//
// M0 stub: real wiring lands in M2+.
package main

import (
	"fmt"
	"os"

	"github.com/agentcell/agentcell/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("cell-provisionerd", version.String())
		return
	}
	fmt.Fprintln(os.Stderr, "cell-provisionerd: not implemented yet (M2); see docs/PLAN.md")
	os.Exit(1)
}
