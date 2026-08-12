// Package access implements the model-service access layer decided in
// ADR-0002: a two-dimensional runner × provider registry. Providers are
// data (embedded YAML, user-overridable); runners are code implementing a
// small fixed interface. A (runner, provider, model) binding is valid iff
// the protocol sets intersect, and this package turns a valid binding into
// the environment variables and headless argv for one session.
//
// This package deliberately imports no Kubernetes types so the core logic
// stays testable without a cluster (dependency rule from docs/PLAN.md).
package access

import (
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/zippo1908/agentcell/configs"
)

// Protocol flavors spoken between runners and providers.
const (
	ProtoAnthropic = "anthropic"
	ProtoOpenAI    = "openai"
)

type Endpoint struct {
	BaseURL string `yaml:"base_url"`
}

type Provider struct {
	DisplayName string              `yaml:"display_name"`
	Region      string              `yaml:"region"`
	Protocols   map[string]Endpoint `yaml:"protocols"`
	AuthEnv     string              `yaml:"auth_env"`
	Models      []string            `yaml:"models"`
	Docs        string              `yaml:"docs"`
}

type providersFile struct {
	Version   int                 `yaml:"version"`
	Providers map[string]Provider `yaml:"providers"`
}

// Registry holds the merged provider table plus the built-in runner set.
type Registry struct {
	providers map[string]Provider
}

// Load parses the built-in preset table and applies overlay files (each a
// full or partial providers.yaml); later overlays win per provider name.
func Load(overlays ...[]byte) (*Registry, error) {
	return LoadWithRunners(overlays, nil)
}

// LoadWithRunners parses the built-in tables and applies overlays to each.
func LoadWithRunners(overlays [][]byte, runnerOverlays [][]byte) (*Registry, error) {
	merged := map[string]Provider{}
	for _, raw := range append([][]byte{configs.ProvidersYAML}, overlays...) {
		if len(raw) == 0 {
			continue
		}
		var f providersFile
		if err := yaml.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("parse providers yaml: %w", err)
		}
		for name, p := range f.Providers {
			merged[name] = p
		}
	}
	// Runner overlays live beside provider overlays and are loaded the same
	// way; a deployment that ships a different CLI build fixes its flags in
	// config rather than waiting for a release.
	rs, err := loadRunners(runnerOverlays...)
	if err != nil {
		return nil, err
	}
	runners = rs
	return &Registry{providers: merged}, nil
}

// Providers returns provider names sorted for stable UI listings.
func (r *Registry) Providers() []string {
	names := make([]string, 0, len(r.providers))
	for n := range r.providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Provider returns one provider by name.
func (r *Registry) Provider(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// Runner is the five-point interface from ADR-0002 D1, reduced to what the
// full-chain slice needs: protocols spoken (in preference order), headless
// argv, and env synthesis per protocol.
type Runner struct {
	Name string
	// Display is the human label shown in the UI.
	Display string
	// Protocols in preference order; first intersecting protocol wins.
	Protocols []string
	// HeadlessArgv builds the one-shot dispatch command for a task.
	HeadlessArgv func(task string) []string

	// The agent CLIs keep their own conversations, with their own ids, under
	// $HOME. AgentCell should not reimplement that — a platform-side replay
	// of a transcript is a worse copy of what the tool already does well.
	// What the platform owes them is a private $HOME (ADR-0009) and a stable
	// id to name the conversation by.

	// NewRunnerSessionID mints an id in whatever shape this CLI requires,
	// when the CLI lets the caller choose one. Nil means it does not, and
	// resume has to work from the CLI's own bookkeeping instead.
	NewRunnerSessionID func() string
	// StartArgv runs a task in a NAMED conversation, so a later resume can
	// address it. Nil falls back to HeadlessArgv.
	StartArgv func(task, runnerSessionID string) []string
	// ResumeArgv continues an existing conversation with more instructions.
	// Nil means this runner cannot resume and a follow-up starts fresh.
	ResumeArgv func(task, runnerSessionID string) []string
	// SessionHomeEnv names the variable that points this CLI at its state
	// directory, for runners that resume by recency rather than by id. Giving
	// each session its own directory is what makes "the most recent
	// conversation" mean this session's — otherwise two of a user's sessions,
	// sharing a runtime and a $HOME, would resume into each other.
	//
	// Empty for CLIs that address conversations by id, which need no such
	// separation.
	SessionHomeEnv string
}

// SessionHomeVar returns the state-directory variable a runner needs scoped
// per session, or "" if it addresses conversations by id instead.
func SessionHomeVar(runner string) string {
	r, ok := runners[runner]
	if !ok {
		return ""
	}
	return r.SessionHomeEnv
}

// Resumable reports whether a runner can continue its own conversation.
func Resumable(runner string) bool {
	r, ok := runners[runner]
	return ok && r.ResumeArgv != nil
}

// NewRunnerSession mints the CLI-side conversation id for a runner, or "" if
// that CLI does not accept one from the caller.
func NewRunnerSession(runner string) string {
	r, ok := runners[runner]
	if !ok || r.NewRunnerSessionID == nil {
		return ""
	}
	return r.NewRunnerSessionID()
}

// StartArgvFor builds the command that begins a named conversation.
func StartArgvFor(runner, task, runnerSessionID string) ([]string, error) {
	r, ok := runners[runner]
	if !ok {
		return nil, fmt.Errorf("unknown runner %q", runner)
	}
	if r.StartArgv == nil || runnerSessionID == "" {
		return r.HeadlessArgv(task), nil
	}
	return r.StartArgv(task, runnerSessionID), nil
}

// ResumeArgvFor builds the command that continues one.
func ResumeArgvFor(runner, task, runnerSessionID string) ([]string, error) {
	r, ok := runners[runner]
	if !ok {
		return nil, fmt.Errorf("unknown runner %q", runner)
	}
	if r.ResumeArgv == nil {
		return nil, fmt.Errorf("runner %q cannot resume a conversation", runner)
	}
	return r.ResumeArgv(task, runnerSessionID), nil
}

// runners is populated from configs/runners.yaml (plus overlays) by Load.
//
// Package-level rather than per-Registry because a runner is a property of
// the images this deployment ships, not of one provider table; Load is
// called once at startup.
var runners = map[string]Runner{}

// Runners returns the configured runner names, sorted.
func Runners() []string {
	names := make([]string, 0, len(runners))
	for n := range runners {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// RunnerDisplay returns a human label for a runner, or the name itself.
func RunnerDisplay(name string) string {
	if r, ok := runners[name]; ok && r.Display != "" {
		return r.Display
	}
	return name
}

// Binding is a resolved (runner, provider, model) triple.
type Binding struct {
	Runner   string
	Provider string
	Model    string
	Protocol string
	BaseURL  string
	// AuthEnv is the provider-native key variable (informational; SessionEnv
	// already maps the key onto protocol variables).
	AuthEnv string
}

// Resolve validates a triple and picks the protocol: the runner's first
// preferred protocol that the provider serves.
func (r *Registry) Resolve(runner, provider, model string) (Binding, error) {
	run, ok := runners[runner]
	if !ok {
		return Binding{}, fmt.Errorf("unknown runner %q (have %v)", runner, Runners())
	}
	prov, ok := r.providers[provider]
	if !ok {
		return Binding{}, fmt.Errorf("unknown provider %q (have %v)", provider, r.Providers())
	}
	for _, proto := range run.Protocols {
		if ep, ok := prov.Protocols[proto]; ok {
			return Binding{
				Runner: runner, Provider: provider, Model: model,
				Protocol: proto, BaseURL: ep.BaseURL, AuthEnv: prov.AuthEnv,
			}, nil
		}
	}
	return Binding{}, fmt.Errorf(
		"runner %q speaks %v but provider %q serves %v — no protocol intersection",
		runner, run.Protocols, provider, protoNames(prov.Protocols))
}

func protoNames(m map[string]Endpoint) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// SessionEnv synthesizes the environment injected into exactly one session
// (never into the Cell container spec: credentials are per-session by
// design). apiKey is the operator-resolved credential value reference —
// callers may pass a literal or a placeholder they substitute later.
func (r *Registry) SessionEnv(b Binding, apiKey string) map[string]string {
	env := map[string]string{}
	switch b.Protocol {
	case ProtoAnthropic:
		if b.Provider == "anthropic" {
			// Native endpoint: the CLI default base URL is correct.
			env["ANTHROPIC_API_KEY"] = apiKey
		} else {
			env["ANTHROPIC_BASE_URL"] = b.BaseURL
			env["ANTHROPIC_AUTH_TOKEN"] = apiKey
		}
		if b.Model != "" {
			env["ANTHROPIC_MODEL"] = b.Model
		}
	case ProtoOpenAI:
		if b.Provider != "openai" {
			env["OPENAI_BASE_URL"] = b.BaseURL
		}
		env["OPENAI_API_KEY"] = apiKey
		if b.Model != "" {
			env["OPENAI_MODEL"] = b.Model
		}
	}
	return env
}

// HeadlessArgv returns the dispatch command for a runner and task.
func HeadlessArgv(runner, task string) ([]string, error) {
	run, ok := runners[runner]
	if !ok {
		return nil, fmt.Errorf("unknown runner %q", runner)
	}
	return run.HeadlessArgv(task), nil
}
