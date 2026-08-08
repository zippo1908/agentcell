// cellctl is the operator CLI. v0.1 is CLI-first: every platform operation
// (cell up/down/rebuild, session dispatch/list/attach, review, doctor) is
// available here before any web UI exists. It talks only to the celld HTTP
// API and contains no business logic of its own.
//
// M0 stub: real subcommands land alongside their milestones.
package main

import (
	"fmt"
	"os"

	"github.com/agentcell/agentcell/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("cellctl", version.String())
		return
	}
	fmt.Fprintln(os.Stderr, "cellctl: not implemented yet; see docs/PLAN.md for the milestone map")
	os.Exit(1)
}
