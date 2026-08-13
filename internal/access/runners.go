package access

import (
	"crypto/rand"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zippo1908/agentcell/configs"
)

// Runners are data, not code.
//
// Providers have been overridable YAML since ADR-0002; runners were a Go
// map, which is the wrong asymmetry: a CLI's flags are the fastest-moving
// thing in this system. An upstream release that renames `--resume` should
// cost an operator one line in /etc/agentcell/runners.d/, not an AgentCell
// release and a redeploy.

// runnerPreset is one entry of runners.yaml.
type runnerPreset struct {
	DisplayName string `yaml:"display_name"`
	// Vendor is who publishes this CLI. Used to tell a caller when they are
	// pairing one vendor's client with another vendor's model — a common and
	// well-supported thing to do, and one whose terms are the operator's to
	// check, not ours to assume.
	Vendor    string   `yaml:"vendor"`
	Protocols []string `yaml:"protocols"`
	// Headless is the one-shot form. Required.
	Headless []string `yaml:"headless"`
	// Start names a conversation the caller chose an id for; Resume
	// continues one. Either may be absent: a runner that cannot resume makes
	// a follow-up start fresh, which is honest and visible.
	Start  []string `yaml:"start"`
	Resume []string `yaml:"resume"`
	// SessionID is the id shape this CLI accepts from the caller: "uuid", or
	// empty when it names its own conversations.
	SessionID string `yaml:"session_id"`
	// SessionHomeEnv points a recency-resuming CLI at a per-session state
	// directory, so "the most recent conversation" cannot mean a sibling's.
	SessionHomeEnv string `yaml:"session_home_env"`
	// ConfigFile is written into that state directory before the CLI runs,
	// for CLIs that read their endpoint from a file rather than from the
	// environment. Same reasoning as argv: this is the shape of somebody
	// else's tool, so it is data an operator can correct, not code.
	ConfigFile *configFilePreset `yaml:"config_file"`
}

// configFilePreset renders {{model}} and {{base_url}} — never the key, which
// stays in the environment and is referenced from the file by name.
type configFilePreset struct {
	Path     string `yaml:"path"`
	Template string `yaml:"template"`
}

type runnersFile struct {
	Version int                     `yaml:"version"`
	Runners map[string]runnerPreset `yaml:"runners"`
}

// loadRunners merges the built-in table with overlays; later overlays win
// per runner name, exactly like providers.
func loadRunners(overlays ...[]byte) (map[string]Runner, error) {
	merged := map[string]runnerPreset{}
	for _, raw := range append([][]byte{configs.RunnersYAML}, overlays...) {
		if len(raw) == 0 {
			continue
		}
		var f runnersFile
		if err := yaml.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("parse runners yaml: %w", err)
		}
		for name, p := range f.Runners {
			merged[name] = p
		}
	}
	out := map[string]Runner{}
	for name, p := range merged {
		r, err := p.compile(name)
		if err != nil {
			return nil, err
		}
		out[name] = r
	}
	return out, nil
}

func (p runnerPreset) compile(name string) (Runner, error) {
	if len(p.Headless) == 0 {
		return Runner{}, fmt.Errorf("runner %q has no headless command", name)
	}
	if len(p.Protocols) == 0 {
		return Runner{}, fmt.Errorf("runner %q declares no protocols", name)
	}
	for _, tpl := range [][]string{p.Headless, p.Start, p.Resume} {
		if err := validateTemplate(name, tpl); err != nil {
			return Runner{}, err
		}
	}
	// A resume template that addresses a conversation by id is useless
	// without a way to have chosen one; catching it here beats a follow-up
	// that quietly resumes "" at runtime.
	if usesSession(p.Resume) && p.SessionID == "" {
		return Runner{}, fmt.Errorf("runner %q resumes by id but declares no session_id shape", name)
	}
	if p.ConfigFile != nil {
		if p.ConfigFile.Path == "" || p.ConfigFile.Template == "" {
			return Runner{}, fmt.Errorf("runner %q: config_file needs both path and template", name)
		}
		// The file lands inside the state directory, so a runner asking for
		// one without declaring where its state lives has nowhere to put it.
		if p.SessionHomeEnv == "" {
			return Runner{}, fmt.Errorf("runner %q: config_file requires session_home_env", name)
		}
		if filepath.IsAbs(p.ConfigFile.Path) || strings.Contains(p.ConfigFile.Path, "..") {
			return Runner{}, fmt.Errorf("runner %q: config_file path must stay inside the state directory", name)
		}
	}
	r := Runner{
		Name:           name,
		Display:        p.DisplayName,
		Vendor:         p.Vendor,
		Protocols:      p.Protocols,
		SessionHomeEnv: p.SessionHomeEnv,
		HeadlessArgv:   func(task string) []string { return render(p.Headless, task, "") },
	}
	if p.ConfigFile != nil {
		r.ConfigPath, r.ConfigTemplate = p.ConfigFile.Path, p.ConfigFile.Template
	}
	switch p.SessionID {
	case "":
	case "uuid":
		r.NewRunnerSessionID = newUUIDv4
	default:
		return Runner{}, fmt.Errorf("runner %q: unknown session_id shape %q", name, p.SessionID)
	}
	if len(p.Start) > 0 {
		r.StartArgv = func(task, sid string) []string { return render(p.Start, task, sid) }
	}
	if len(p.Resume) > 0 {
		r.ResumeArgv = func(task, sid string) []string { return render(p.Resume, task, sid) }
	}
	return r, nil
}

const (
	phTask    = "{{task}}"
	phSession = "{{session}}"
)

// render substitutes placeholders WITHOUT splitting.
//
// This is the whole safety property of templating argv: a task is arbitrary
// user text, and it must arrive at the CLI as exactly one argument. Building
// a command line and splitting it on spaces would turn "fix login; rm -rf /"
// into something else entirely.
func render(tpl []string, task, session string) []string {
	out := make([]string, 0, len(tpl))
	for _, el := range tpl {
		el = strings.ReplaceAll(el, phTask, task)
		el = strings.ReplaceAll(el, phSession, session)
		out = append(out, el)
	}
	return out
}

// validateTemplate refuses placeholders we do not substitute, so a typo
// reaches the CLI as a literal "{{tsak}}" only if someone insists.
func validateTemplate(runner string, tpl []string) error {
	for _, el := range tpl {
		rest := el
		for {
			i := strings.Index(rest, "{{")
			if i < 0 {
				break
			}
			j := strings.Index(rest[i:], "}}")
			if j < 0 {
				return fmt.Errorf("runner %q: unterminated placeholder in %q", runner, el)
			}
			ph := rest[i : i+j+2]
			if ph != phTask && ph != phSession {
				return fmt.Errorf("runner %q: unknown placeholder %s (only %s and %s are substituted)",
					runner, ph, phTask, phSession)
			}
			rest = rest[i+j+2:]
		}
	}
	return nil
}

func usesSession(tpl []string) bool {
	for _, el := range tpl {
		if strings.Contains(el, phSession) {
			return true
		}
	}
	return false
}

// newUUIDv4 builds the id shape CLIs like Claude Code require. Small enough
// to do here rather than take a dependency for.
func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A guessable conversation id would let a later resume address
		// somebody else's; there is no safe degraded value.
		panic("access: crypto/rand unavailable: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
