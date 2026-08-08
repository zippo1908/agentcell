package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// askpassScript is written at container start; git invokes it for
// credentials and it defers back into this binary, which answers from
// GIT_USERNAME / GIT_TOKEN. Tokens therefore never land in .git/config.
const askpassPath = "/tmp/agentcell-askpass.sh"

func ensureAskpass() error {
	script := "#!/bin/sh\nexec /agentcell/cell-runtime askpass \"$@\"\n"
	return os.WriteFile(askpassPath, []byte(script), 0o755)
}

func runAskpass(args []string) error {
	prompt := strings.ToLower(strings.Join(args, " "))
	if strings.Contains(prompt, "username") {
		fmt.Println(os.Getenv("GIT_USERNAME"))
	} else {
		fmt.Println(os.Getenv("GIT_TOKEN"))
	}
	return nil
}

// git runs a git command with output to our stdout/stderr and askpass wired.
func git(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "GIT_ASKPASS="+askpassPath, "GIT_TERMINAL_PROMPT=0")
	return cmd.Run()
}

// gitOut runs a git command and captures trimmed stdout.
func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_ASKPASS="+askpassPath, "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
