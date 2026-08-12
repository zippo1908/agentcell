package access

import (
	"os/exec"
	"strings"
	"testing"
)

func loadWith(t *testing.T, overlay string) {
	t.Helper()
	var ov [][]byte
	if overlay != "" {
		ov = [][]byte{[]byte(overlay)}
	}
	if _, err := LoadWithRunners(nil, ov); err != nil {
		t.Fatalf("load runners: %v", err)
	}
}

// A task is arbitrary user text and must arrive at the CLI as exactly ONE
// argument. Templating argv would be worthless if a space split it.
func TestTaskStaysOneArgumentThroughTheTemplate(t *testing.T) {
	loadWith(t, "")
	hostile := "fix the login bug; rm -rf /workspace/users && echo $(whoami)"
	for _, runner := range Runners() {
		argv, err := HeadlessArgv(runner, hostile)
		if err != nil {
			t.Fatalf("%s: %v", runner, err)
		}
		found := 0
		for _, a := range argv {
			if a == hostile {
				found++
			}
		}
		if found != 1 {
			t.Errorf("%s: task appears as a whole argument %d times in %q", runner, found, argv)
		}
		// And nothing in the rendered argv is a shell metacharacter smuggled
		// in as its own element.
		for _, a := range argv {
			if a != hostile && strings.ContainsAny(a, ";&|") {
				t.Errorf("%s: argv element %q carries shell syntax", runner, a)
			}
		}
	}
}

// Kimi ships as a runner, pairs with the moonshot provider, and — because
// its conversation handling is unverified — deliberately does not claim to
// resume. A follow-up starting fresh is honest; one that silently opens a new
// context while looking like a continuation is not.
func TestKimiIsUsableAndDoesNotClaimResume(t *testing.T) {
	reg, err := LoadWithRunners(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	argv, err := HeadlessArgv("kimi", "两列布局")
	if err != nil || len(argv) == 0 || argv[0] != "kimi" {
		t.Fatalf("kimi headless = %v (%v)", argv, err)
	}
	if _, err := reg.Resolve("kimi", "moonshot", ""); err != nil {
		t.Errorf("kimi cannot be paired with moonshot: %v", err)
	}
	if Resumable("kimi") {
		t.Error("kimi claims resume support that has not been verified against a pinned CLI")
	}
	if _, err := ResumeArgvFor("kimi", "more", ""); err == nil {
		t.Error("resuming an unverified runner should be refused, not guessed")
	}
}

// The point of runners-as-data: an operator whose CLI renamed a flag fixes
// one file instead of waiting for a release.
func TestOverlayCanTeachARunnerToResume(t *testing.T) {
	loadWith(t, `
version: 1
runners:
  kimi:
    display_name: Kimi CLI
    protocols: [anthropic]
    headless: ["kimi", "--yes", "-p", "{{task}}"]
    session_id: uuid
    start:  ["kimi", "--yes", "--session", "{{session}}", "-p", "{{task}}"]
    resume: ["kimi", "--yes", "--resume", "{{session}}", "-p", "{{task}}"]
`)
	defer loadWith(t, "")
	if !Resumable("kimi") {
		t.Fatal("the overlay did not take effect")
	}
	sid := NewRunnerSession("kimi")
	if sid == "" {
		t.Fatal("no conversation id minted")
	}
	resume, err := ResumeArgvFor("kimi", "keep going", sid)
	if err != nil {
		t.Fatal(err)
	}
	if !containsArg(resume, "--resume") || !containsArg(resume, sid) {
		t.Errorf("resume does not address the conversation: %v", resume)
	}
}

// A typo in a template must fail at load, not produce a literal "{{tsak}}"
// argument at 3am.
func TestBadTemplatesAreRefusedAtLoad(t *testing.T) {
	for name, overlay := range map[string]string{
		"unknown placeholder": `
version: 1
runners:
  x: {protocols: [openai], headless: ["x", "{{tsak}}"]}`,
		"unterminated": `
version: 1
runners:
  x: {protocols: [openai], headless: ["x", "{{task"]}`,
		"no headless": `
version: 1
runners:
  x: {protocols: [openai]}`,
		"no protocols": `
version: 1
runners:
  x: {headless: ["x", "{{task}}"]}`,
		"resumes by id without an id shape": `
version: 1
runners:
  x: {protocols: [openai], headless: ["x", "{{task}}"], resume: ["x", "-r", "{{session}}"]}`,
		"unknown id shape": `
version: 1
runners:
  x: {protocols: [openai], headless: ["x", "{{task}}"], session_id: ulid}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadWithRunners(nil, [][]byte{[]byte(overlay)}); err == nil {
				t.Error("accepted a runner definition that cannot work")
			}
		})
	}
	loadWith(t, "") // restore the built-ins for other tests
}

// The built-in table has to be valid, or the binary ships broken.
func TestBuiltinRunnersLoad(t *testing.T) {
	loadWith(t, "")
	for _, want := range []string{"claude", "codex", "pi", "kimi"} {
		if !containsArg(Runners(), want) {
			t.Errorf("built-in runner %q is missing (have %v)", want, Runners())
		}
	}
	if !Resumable("claude") || !Resumable("codex") {
		t.Error("claude and codex lost their resume support in the move to data")
	}
	if SessionHomeVar("codex") != "CODEX_HOME" {
		t.Error("codex lost its per-session state directory")
	}
}

// Round-trip the rendered argv through a real shell the way the runtime does,
// to prove the quoting the runtime applies survives a hostile task.
func TestRenderedArgvSurvivesAShell(t *testing.T) {
	loadWith(t, "")
	argv, err := HeadlessArgv("kimi", "a'b\"c; echo pwned")
	if err != nil {
		t.Fatal(err)
	}
	quoted := make([]string, 0, len(argv))
	for _, a := range argv {
		quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", `'"'"'`)+"'")
	}
	out, err := exec.Command("sh", "-c", "printf '%s\\n' "+strings.Join(quoted, " ")).Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(got) != len(argv) {
		t.Fatalf("shell saw %d arguments, want %d: %q", len(got), len(argv), got)
	}
	if strings.Contains(string(out), "pwned\n") && got[len(got)-1] != "a'b\"c; echo pwned" {
		t.Error("the task was executed rather than passed")
	}
}

func containsArg(list []string, want string) bool {
	for _, a := range list {
		if a == want {
			return true
		}
	}
	return false
}
