// cell-runtime is the static multi-call binary baked into every Cell
// image. Applets:
//
//	anchor   PID 1 of the anchor pod: clone/refresh the repo, keep the
//	         resident product preview running, reap zombies, heartbeat
//	session  PID 1 of a session pod: create the worktree, run the agent
//	settle   settle job: commit/push produced work, reclaim the worktree,
//	         report {produced,branch} via the termination message
//	askpass  git credential helper (reads GIT_USERNAME / GIT_TOKEN)
package main

import (
	"fmt"
	"os"

	"github.com/zippo1908/agentcell/internal/version"
)

func main() {
	applet := ""
	if len(os.Args) > 1 {
		applet = os.Args[1]
	}
	var err error
	switch applet {
	case "--version":
		fmt.Println("cell-runtime", version.String())
	case "anchor":
		err = runAnchor()
	case "session":
		err = runSession()
	case "settle":
		err = runSettle()
	case "prod":
		err = runProd()
	case "askpass":
		err = runAskpass(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "usage: cell-runtime <anchor|session|settle|prod|askpass|--version>")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cell-runtime "+applet+": "+err.Error())
		os.Exit(1)
	}
}
