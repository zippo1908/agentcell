package access

import "testing"

func mustLoad(t *testing.T) *Registry {
	t.Helper()
	r, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return r
}

func TestBuiltinPresetsPresent(t *testing.T) {
	r := mustLoad(t)
	for _, want := range []string{"anthropic", "openai", "aliyun-bailian", "tencent-hunyuan", "deepseek", "moonshot", "zhipu"} {
		if _, ok := r.Provider(want); !ok {
			t.Errorf("built-in provider %q missing", want)
		}
	}
}

func TestClaudeOnAliyunBailianUsesAnthropicProxy(t *testing.T) {
	r := mustLoad(t)
	b, err := r.Resolve("claude", "aliyun-bailian", "qwen3-coder-plus")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if b.Protocol != ProtoAnthropic {
		t.Fatalf("protocol = %q, want anthropic", b.Protocol)
	}
	env := r.SessionEnv(b, "sk-test")
	if env["ANTHROPIC_BASE_URL"] == "" {
		t.Error("expected ANTHROPIC_BASE_URL for a non-native anthropic provider")
	}
	if env["ANTHROPIC_AUTH_TOKEN"] != "sk-test" {
		t.Error("expected ANTHROPIC_AUTH_TOKEN to carry the key")
	}
	if env["ANTHROPIC_MODEL"] != "qwen3-coder-plus" {
		t.Error("expected ANTHROPIC_MODEL to carry the model")
	}
}

func TestCodexOnTencentHunyuanUsesOpenAI(t *testing.T) {
	r := mustLoad(t)
	b, err := r.Resolve("codex", "tencent-hunyuan", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if b.Protocol != ProtoOpenAI {
		t.Fatalf("protocol = %q, want openai", b.Protocol)
	}
	env := r.SessionEnv(b, "key")
	if env["OPENAI_BASE_URL"] == "" || env["OPENAI_API_KEY"] != "key" {
		t.Errorf("bad env: %v", env)
	}
}

func TestCodexOnAnthropicOnlyProviderFails(t *testing.T) {
	r := mustLoad(t)
	if _, err := r.Resolve("codex", "anthropic", ""); err == nil {
		t.Fatal("expected protocol-intersection error, got nil")
	}
}

func TestClaudeOnNativeAnthropicOmitsBaseURL(t *testing.T) {
	r := mustLoad(t)
	b, err := r.Resolve("claude", "anthropic", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := r.SessionEnv(b, "sk")
	if _, has := env["ANTHROPIC_BASE_URL"]; has {
		t.Error("native anthropic must not override base URL")
	}
	if env["ANTHROPIC_API_KEY"] != "sk" {
		t.Error("native anthropic uses ANTHROPIC_API_KEY")
	}
}

func TestOverlayWins(t *testing.T) {
	overlay := []byte(`
providers:
  aliyun-bailian:
    display_name: Custom Bailian
    region: cn
    protocols:
      anthropic:
        base_url: https://internal-proxy.example/anthropic
    auth_env: DASHSCOPE_API_KEY
`)
	r, err := Load(overlay)
	if err != nil {
		t.Fatalf("Load overlay: %v", err)
	}
	b, err := r.Resolve("claude", "aliyun-bailian", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if b.BaseURL != "https://internal-proxy.example/anthropic" {
		t.Errorf("overlay did not win: %q", b.BaseURL)
	}
}

func TestHeadlessArgv(t *testing.T) {
	argv, err := HeadlessArgv("claude", "fix the login bug")
	if err != nil || len(argv) == 0 || argv[0] != "claude" {
		t.Fatalf("argv=%v err=%v", argv, err)
	}
	if _, err := HeadlessArgv("nope", "x"); err == nil {
		t.Fatal("unknown runner must error")
	}
}
