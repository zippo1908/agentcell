package access

import (
	"strings"
	"testing"
)

// The bug this file exists for: Codex takes its endpoint from a config file,
// not from OPENAI_BASE_URL. Setting only the environment left a session
// dispatched at DeepSeek announcing "provider: openai, model: gpt-5.6-sol"
// and retrying against a host the cluster cannot reach — a run that looks
// alive and is talking to the wrong vendor.

func TestSessionConfigCarriesTheChosenEndpoint(t *testing.T) {
	path, content, ok := SessionConfig("codex", Binding{
		Provider: "deepseek", Model: "deepseek-chat",
		BaseURL: "https://api.deepseek.com", Protocol: ProtoOpenAI,
	})
	if !ok {
		t.Fatal("codex declares no config file; the provider choice cannot reach it")
	}
	if path != "config.toml" {
		t.Errorf("path = %q, want config.toml", path)
	}
	for _, want := range []string{
		`model = "deepseek-chat"`,
		`base_url = "https://api.deepseek.com"`,
		`model_provider = "agentcell"`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("config missing %s\n---\n%s", want, content)
		}
	}
}

// The key belongs in the environment. A config file is not a secret store:
// it is written to a volume, and the agent being driven can read it.
func TestSessionConfigNeverContainsTheKey(t *testing.T) {
	_, content, _ := SessionConfig("codex", Binding{
		Provider: "deepseek", Model: "deepseek-chat",
		BaseURL: "https://api.deepseek.com", Protocol: ProtoOpenAI,
	})
	if !strings.Contains(content, `env_key = "OPENAI_API_KEY"`) {
		t.Error("config should reference the key by variable name")
	}
	if strings.Contains(strings.ToLower(content), "sk-") {
		t.Error("config appears to embed a key")
	}
}

// A model name comes from whoever dispatched the session. An unescaped quote
// would close the TOML string and leave the CLI reading a file that means
// something else.
func TestSessionConfigEscapesUntrustedValues(t *testing.T) {
	_, content, _ := SessionConfig("codex", Binding{
		Provider: "deepseek", Model: `evil"
model_provider = "openai`,
		BaseURL: "https://api.deepseek.com", Protocol: ProtoOpenAI,
	})
	if strings.Contains(content, "\nmodel_provider = \"openai\"") {
		t.Errorf("a model name broke out of its string:\n%s", content)
	}
	if strings.Count(content, `model_provider = "agentcell"`) != 1 {
		t.Errorf("the chosen provider is no longer the only one declared:\n%s", content)
	}
}

// Runners that take their endpoint from the environment must not grow a file
// they never asked for.
func TestSessionConfigAbsentForEnvironmentRunners(t *testing.T) {
	for _, r := range []string{"claude", "pi"} {
		if _, _, ok := SessionConfig(r, Binding{Model: "m", BaseURL: "u"}); ok {
			t.Errorf("runner %q unexpectedly declares a config file", r)
		}
	}
	if _, _, ok := SessionConfig("no-such-runner", Binding{}); ok {
		t.Error("unknown runner returned a config file")
	}
}

// A config file lands inside the session's state directory, so a runner that
// wants one without declaring where its state lives is a loading error, not
// a file written somewhere shared.
func TestConfigFileRequiresAStateDirectory(t *testing.T) {
	_, err := loadRunners([]byte(`
version: 1
runners:
  broken:
    protocols: [openai]
    headless: ["x", "{{task}}"]
    config_file:
      path: config.toml
      template: "model = \"{{model}}\""
`))
	if err == nil || !strings.Contains(err.Error(), "session_home_env") {
		t.Fatalf("expected a session_home_env error, got %v", err)
	}
}

func TestConfigFilePathStaysInsideTheStateDirectory(t *testing.T) {
	for _, bad := range []string{"/etc/codex.toml", "../../.codex/config.toml"} {
		_, err := loadRunners([]byte(`
version: 1
runners:
  broken:
    protocols: [openai]
    headless: ["x", "{{task}}"]
    session_home_env: CODEX_HOME
    config_file:
      path: "` + bad + `"
      template: "model = \"{{model}}\""
`))
		if err == nil {
			t.Errorf("path %q was accepted", bad)
		}
	}
}

// Kimi Code reads credentials only from its config file — its documentation
// says plainly that an exported KIMI_API_KEY is not picked up — so the file
// is mandatory, not a convenience.
func TestKimiDeclaresAConfigWithCredentials(t *testing.T) {
	path, content, ok := SessionConfig("kimi", Binding{
		Provider: "kimi-code", Model: "k3",
		BaseURL: "https://api.kimi.com/coding/v1", Protocol: ProtoOpenAI,
	})
	if !ok {
		t.Fatal("kimi declares no config file; it would fail at startup with missing credentials")
	}
	if path != "config.toml" {
		t.Errorf("path = %q", path)
	}
	for _, want := range []string{
		`default_model = "k3"`,
		`type = "kimi"`,
		`base_url = "https://api.kimi.com/coding/v1"`,
		`[models."k3"]`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("config missing %s\n---\n%s", want, content)
		}
	}
}

// The rendered config travels in the pod spec, which anyone who can read
// pods can read. So the key is a MARKER here and is expanded by the runtime
// inside the pod — the literal value must never appear at this layer.
func TestTheKeyIsAMarkerNotTheKey(t *testing.T) {
	_, content, _ := SessionConfig("kimi", Binding{
		Provider: "kimi-code", Model: "k3", BaseURL: "https://x", Protocol: ProtoOpenAI,
	})
	if !strings.Contains(content, "${"+APIKeyMarker+"}") {
		t.Errorf("the key placeholder is gone, so either the key is inline or the config is broken:\n%s", content)
	}
	for _, leak := range []string{"sk-", "api_key = \"sk"} {
		if strings.Contains(content, leak) {
			t.Errorf("a literal key reached the control plane's rendering: %s", content)
		}
	}
}
