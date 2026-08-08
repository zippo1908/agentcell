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
	// Protocols in preference order; first intersecting protocol wins.
	Protocols []string
	// HeadlessArgv builds the one-shot dispatch command for a task.
	HeadlessArgv func(task string) []string
}

var runners = map[string]Runner{
	"claude": {
		Name:      "claude",
		Protocols: []string{ProtoAnthropic},
		HeadlessArgv: func(task string) []string {
			return []string{"claude", "--dangerously-skip-permissions", "-p", task}
		},
	},
	"codex": {
		Name:      "codex",
		Protocols: []string{ProtoOpenAI},
		HeadlessArgv: func(task string) []string {
			// The container is the boundary; Codex's inner sandbox must yield
			// to it (ADR appendix A).
			return []string{"codex", "exec", "--sandbox", "danger-full-access", task}
		},
	},
	"pi": {
		Name:      "pi",
		Protocols: []string{ProtoAnthropic, ProtoOpenAI},
		HeadlessArgv: func(task string) []string {
			return []string{"pi", "-p", task}
		},
	},
}

// Runners returns the built-in runner names, sorted.
func Runners() []string {
	names := make([]string, 0, len(runners))
	for n := range runners {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
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
