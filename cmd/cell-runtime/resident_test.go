package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The agent command line is assembled into a string for tmux send-keys, and
// its arguments carry operator- and task-supplied text. A task containing a
// semicolon must stay one argument rather than becoming a second command.
func TestShellJoinQuotesArgumentsSoTheyStayArguments(t *testing.T) {
	hostile := []string{
		"codex", "exec",
		"fix the bug; rm -rf /workspace/users",
		"and $(whoami) `id` 'quoted'",
	}
	line := shellJoin(hostile)

	// Round-trip through a real shell: the arguments it sees must be exactly
	// the ones we passed, and nothing else must have run.
	out, err := exec.Command("sh", "-c", "printf '%s\\n' "+line).Output()
	if err != nil {
		t.Fatalf("shell rejected the line %q: %v", line, err)
	}
	got := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(got) != len(hostile) {
		t.Fatalf("shell saw %d arguments, want %d: %q", len(got), len(hostile), got)
	}
	for i := range hostile {
		if got[i] != hostile[i] {
			t.Errorf("argument %d became %q, want %q", i, got[i], hostile[i])
		}
	}
}

func TestShellQuoteHandlesEmbeddedQuotes(t *testing.T) {
	for _, s := range []string{"it's", "'", `a'"'"'b`, ""} {
		out, err := exec.Command("sh", "-c", "printf '%s' "+shellQuote(s)).Output()
		if err != nil {
			t.Fatalf("shell rejected %q: %v", s, err)
		}
		if string(out) != s {
			t.Errorf("quote round-trip of %q gave %q", s, out)
		}
	}
}
