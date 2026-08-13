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

// Pairing one vendor's CLI with another vendor's models is normal, works,
// and is often the only shape available to a team that cannot reach a
// foreign API. AgentCell states the pairing; it does not adjudicate it.
func TestCrossVendorPairingIsReportedNotBlocked(t *testing.T) {
	reg, err := LoadWithRunners(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("claude against kimi models is allowed and noted", func(t *testing.T) {
		b, err := reg.Resolve("claude", "moonshot", "kimi-k2-turbo-preview")
		if err != nil {
			t.Fatalf("the pairing was refused: %v", err)
		}
		if !b.CrossVendor || b.Advisory == "" {
			t.Error("a cross-vendor pairing produced no note")
		}
		for _, want := range []string{"anthropic", "moonshot"} {
			if !strings.Contains(strings.ToLower(b.Advisory), want) {
				t.Errorf("the note does not name %s: %q", want, b.Advisory)
			}
		}
		// It must not pretend to have decided anything.
		for _, forbidden := range []string{"illegal", "not permitted", "violat", "prohibited"} {
			if strings.Contains(strings.ToLower(b.Advisory), forbidden) {
				t.Errorf("the note passes judgement it has no basis for: %q", b.Advisory)
			}
		}
	})

	t.Run("a vendor's own CLI and models get no note", func(t *testing.T) {
		b, err := reg.Resolve("kimi", "moonshot", "")
		if err != nil {
			t.Fatal(err)
		}
		if b.CrossVendor || b.Advisory != "" {
			t.Errorf("same-vendor pairing was flagged: %q", b.Advisory)
		}
		if b2, err := reg.Resolve("claude", "anthropic", ""); err != nil || b2.CrossVendor {
			t.Errorf("claude on anthropic was flagged (%v)", err)
		}
	})

	t.Run("a runner with no declared vendor is never flagged", func(t *testing.T) {
		b, err := reg.Resolve("pi", "moonshot", "")
		if err != nil {
			t.Fatal(err)
		}
		if b.CrossVendor {
			t.Error("a vendor-less runner was flagged")
		}
	})
}

// The catalogue exists so the UI cannot offer a combination the API refuses.
func TestCatalogueOnlyPairsCompatibleProtocols(t *testing.T) {
	reg, err := LoadWithRunners(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	runs, provs := reg.Catalogue()
	byName := map[string]ProviderInfo{}
	for _, p := range provs {
		byName[p.Name] = p
	}
	if len(runs) == 0 || len(provs) == 0 {
		t.Fatal("empty catalogue")
	}
	for _, r := range runs {
		if len(r.Providers) == 0 {
			t.Errorf("runner %s can drive nothing", r.Name)
		}
		for _, pn := range r.Providers {
			// Every offered pairing must actually resolve.
			if _, err := reg.Resolve(r.Name, pn, ""); err != nil {
				t.Errorf("catalogue offers %s+%s but Resolve refuses it: %v", r.Name, pn, err)
			}
		}
		// And nothing compatible is hidden.
		for _, p := range provs {
			offered := containsArg(r.Providers, p.Name)
			_, err := reg.Resolve(r.Name, p.Name, "")
			if (err == nil) != offered {
				t.Errorf("%s+%s: offered=%v but resolvable=%v", r.Name, p.Name, offered, err == nil)
			}
		}
	}
}

// A runner defaults to its own vendor's models where possible: that is the
// pairing with no third-party licence question to answer.
func TestRunnerDefaultsToItsOwnVendor(t *testing.T) {
	reg, err := LoadWithRunners(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	runs, _ := reg.Catalogue()
	want := map[string]string{"claude": "anthropic", "codex": "openai", "kimi": "moonshot"}
	for _, r := range runs {
		if w, ok := want[r.Name]; ok && r.DefaultProvider != w {
			t.Errorf("%s defaults to %q, want its own vendor %q", r.Name, r.DefaultProvider, w)
		}
		if r.DefaultProvider != "" && !containsArg(r.Providers, r.DefaultProvider) {
			t.Errorf("%s defaults to %q, which is not in its own list", r.Name, r.DefaultProvider)
		}
	}
}

// The UI has to tell a user when a follow-up will start fresh instead of
// continuing — assuming continuity that is not there is the expensive
// mistake.
func TestCatalogueReportsResumeHonestly(t *testing.T) {
	reg, _ := LoadWithRunners(nil, nil)
	runs, _ := reg.Catalogue()
	for _, r := range runs {
		if r.Resumable != Resumable(r.Name) {
			t.Errorf("%s: catalogue says resumable=%v, runner says %v", r.Name, r.Resumable, Resumable(r.Name))
		}
	}
}

// A CLI that does not recognise a model name falls back to its own assumed
// context window — Claude Code assumes 200k and compacts early, which
// silently truncates work a 256k model could have held. The provider knows
// its window; the session should be told.
func TestRealContextWindowIsPassedToTheCLI(t *testing.T) {
	reg, err := LoadWithRunners(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := reg.Resolve("claude", "moonshot", "kimi-k2-turbo-preview")
	if err != nil {
		t.Fatal(err)
	}
	env := reg.SessionEnv(b, "sk-test")
	if env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] == "" {
		t.Error("the CLI is left to guess the context window of an unfamiliar model")
	}
	if env["ANTHROPIC_BASE_URL"] == "" || env["ANTHROPIC_AUTH_TOKEN"] == "" {
		t.Errorf("the endpoint or credential is missing: %v", env)
	}
	// A provider that declares nothing must not fabricate a window.
	b2, _ := reg.Resolve("claude", "anthropic", "")
	if _, set := reg.SessionEnv(b2, "sk-test")["CLAUDE_CODE_MAX_CONTEXT_TOKENS"]; set {
		t.Error("a window was invented for a provider that declared none")
	}
}
