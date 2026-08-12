package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/zippo1908/agentcell/pkg/runtimeapi"
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
	username := strings.Contains(prompt, "username")
	// Broker mode: authenticate to the git-broker with the pod's SA token,
	// never a forge credential (ADR-0005). The forge token is not present
	// in this pod at all.
	if os.Getenv(runtimeapi.EnvGitBroker) != "" {
		if username {
			fmt.Println(runtimeapi.BrokerGitUser)
		} else {
			tok, err := os.ReadFile(runtimeapi.SATokenPath)
			if err != nil {
				return fmt.Errorf("read service account token: %w", err)
			}
			fmt.Println(strings.TrimSpace(string(tok)))
		}
		return nil
	}
	if username {
		fmt.Println(os.Getenv(runtimeapi.EnvGitUsername))
	} else {
		fmt.Println(os.Getenv(runtimeapi.EnvGitToken))
	}
	return nil
}

// effectiveGitURL returns the URL git should target for the workload's
// repository. In broker mode that is <broker>/<cell> (the real remote is
// resolved by the broker from the Cell CR, and never appears here); in
// direct mode it is the real remote passed in.
func effectiveGitURL(realURL string) string {
	broker := strings.TrimRight(os.Getenv(runtimeapi.EnvGitBroker), "/")
	if broker == "" {
		return realURL
	}
	return broker + "/" + os.Getenv(runtimeapi.EnvCellName)
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

// envWithoutGitCreds is the environment for child processes that execute
// repo-controlled code: everything except the git credential variables.
func envWithoutGitCreds() []string {
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_USERNAME=") || strings.HasPrefix(kv, "GIT_TOKEN=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// gitNet runs a network git op (clone/fetch/push) with bounded retry: a
// pod that starts before CoreDNS or egress is ready would otherwise fail
// permanently on a transient "could not resolve host". Backoff is fixed and
// short; the caller's own supervision handles anything past this window.
func gitNet(dir string, args ...string) error {
	var err error
	for attempt := 1; attempt <= 5; attempt++ {
		if err = git(dir, args...); err == nil {
			return nil
		}
		fmt.Fprintf(os.Stderr, "git %s: attempt %d failed: %v\n", args[0], attempt, err)
		time.Sleep(time.Duration(attempt*attempt) * time.Second) // 1,4,9,16s
	}
	return err
}

// gitOut runs a git command and captures trimmed stdout.
func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_ASKPASS="+askpassPath, "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
