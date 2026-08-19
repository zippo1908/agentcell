package controller

import (
	"strings"
	"testing"
)

// Nothing configured, nothing injected: a deployment that has not turned the
// proxy on must not have its workloads pointed at a service that is not there.
func TestNoProxyConfiguredInjectsNothing(t *testing.T) {
	if len(egressEnv("")) != 0 {
		t.Fatal("an unset proxy still produced environment")
	}
}

// Cluster-internal traffic must bypass the proxy.
//
// The git broker, the API server and every other service live inside; an
// egress allowlist names EXTERNAL destinations, so routing internal calls
// through it refuses them. The visible symptom is a git push failing, which
// sends whoever debugs it looking at the broker rather than at NO_PROXY.
func TestInternalTrafficBypassesTheProxy(t *testing.T) {
	env := map[string]string{}
	for _, e := range egressEnv("http://egress-proxy.agentcell-system.svc:3128") {
		env[e.Name] = e.Value
	}
	no := env["NO_PROXY"]
	for _, must := range []string{".svc", ".cluster.local", "10.0.0.0/8", "localhost", "127.0.0.1"} {
		if !strings.Contains(no, must) {
			t.Errorf("NO_PROXY does not exempt %q: %q", must, no)
		}
	}
}

// Both spellings, or some libraries proxy and others do not.
func TestBothCasesAreSet(t *testing.T) {
	env := map[string]string{}
	for _, e := range egressEnv("http://p:3128") {
		env[e.Name] = e.Value
	}
	for _, pair := range [][2]string{
		{"HTTP_PROXY", "http_proxy"},
		{"HTTPS_PROXY", "https_proxy"},
		{"NO_PROXY", "no_proxy"},
	} {
		if env[pair[0]] == "" || env[pair[1]] == "" {
			t.Errorf("%s/%s: only one case is set (%q / %q)", pair[0], pair[1], env[pair[0]], env[pair[1]])
		}
		if env[pair[0]] != env[pair[1]] {
			t.Errorf("%s and %s disagree: %q vs %q", pair[0], pair[1], env[pair[0]], env[pair[1]])
		}
	}
}
