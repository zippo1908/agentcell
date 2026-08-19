package webui

import (
	"testing"
	"time"
)

// The probe verdicts decide whether the board tells somebody "go reconnect"
// before they ever ask — a wrong verdict either cries wolf or stays silent
// about a dead grant, and both cost the trust this feature exists to keep.
func TestClassifyProbe(t *testing.T) {
	cases := []struct {
		name string
		blob string
		want string
	}{
		{"healthy run starts with the meta line", `{"role":"meta","type":"system.version"}`, "ok"},
		{"dead grant is invalid", "error: failed to run prompt: internal: The provided authorization grant is invalid", "invalid"},
		{"provider refusal is invalid", "401 Invalid Authentication", "invalid"},
		{"no file means never connected", "AGENTCELL_NOFILE\nerror: failed to run prompt: provider kimi-code has no credential configured", "missing"},
		{"configured-but-dead is invalid not missing", "error: failed to run prompt: provider kimi-code has no credential configured", "invalid"},
		{"unrecognised failure is unknown, not an alarm", "command terminated with exit code 137", "unknown"},
		{"silence is unknown", "", "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyProbe(c.blob); got != c.want {
				t.Errorf("classifyProbe(%q) = %q, want %q", c.blob, got, c.want)
			}
		})
	}
}

// The cache exists so a page open does not spend a model call: a verdict is
// trusted for its TTL and not a moment longer.
func TestProbeCacheTTL(t *testing.T) {
	c := &probeCache{}
	if _, ok := c.get("k"); ok {
		t.Fatal("empty cache answered")
	}
	c.put("k", "ok")
	if v, ok := c.get("k"); !ok || v != "ok" {
		t.Fatalf("fresh verdict lost: %q %v", v, ok)
	}
	c.m["k"] = probeVerdict{result: "ok", at: c.m["k"].at.Add(-prewarmProbeTTL - time.Second)}
	if _, ok := c.get("k"); ok {
		t.Fatal("expired verdict still trusted")
	}
}
