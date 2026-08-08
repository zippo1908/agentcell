// celld is the non-root control-plane daemon: HTTP API, auth, project and
// session registry, review queue, reconciler, and the embedded web UI.
// All privileged operations are delegated to cell-provisionerd over a
// typed gRPC contract on a Unix domain socket.
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
		fmt.Println("celld", version.String())
		return
	}
	fmt.Fprintln(os.Stderr, "celld: not implemented yet (M2); see docs/PLAN.md")
	os.Exit(1)
}
