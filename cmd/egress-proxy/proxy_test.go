package main

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zippo1908/agentcell/internal/egress"
)

func newProxy(t *testing.T, pol egress.Policy) *httptest.Server {
	t.Helper()
	p := &proxy{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	p.policy.Store(&pol)
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return srv
}

// connect speaks the CONNECT half of the proxy protocol by hand, because
// that is what an agent's HTTP client does and it is the path being tested.
func connect(t *testing.T, proxyURL, target string) (int, net.Conn) {
	t.Helper()
	c, err := net.Dial("tcp", strings.TrimPrefix(proxyURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, c
}

// An unlisted destination is refused, and the refusal reaches the client.
//
// This is the property the whole component exists for: without it, a
// prompt-injected agent posts the workspace anywhere it likes over 443.
func TestAnUnlistedDestinationIsRefused(t *testing.T) {
	srv := newProxy(t, egress.Policy{Rules: []egress.Rule{{Host: "allowed.example"}}})
	code, c := connect(t, srv.URL, "attacker.example:443")
	defer c.Close()
	if code != http.StatusForbidden {
		t.Fatalf("CONNECT to an unlisted host returned %d, want 403", code)
	}
}

// A listed destination is connected end to end, and bytes actually flow.
//
// Refusing everything would also pass the test above; this is what proves
// the proxy is a proxy and not just a rejector.
func TestAListedDestinationIsConnectedThrough(t *testing.T) {
	// A stand-in for the upstream: echoes one line back.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		b := make([]byte, 5)
		if _, err := io.ReadFull(conn, b); err != nil {
			return
		}
		_, _ = conn.Write(b)
	}()

	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := net.LookupPort("tcp", port)
	srv := newProxy(t, egress.Policy{Rules: []egress.Rule{{Host: "localhost", Port: p}}})

	code, c := connect(t, srv.URL, "localhost:"+port)
	defer c.Close()
	if code != http.StatusOK {
		t.Fatalf("CONNECT to a listed host returned %d, want 200", code)
	}
	if _, err := io.WriteString(c, "hello"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 5)
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("nothing came back through the tunnel: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q through the tunnel, want %q", got, "hello")
	}
}

// A literal address is refused by the proxy, not merely by the policy type.
//
// Wired separately because the CONNECT path parses host:port itself, and an
// address that survives that parsing bypasses the entire allowlist.
func TestConnectToABareAddressIsRefused(t *testing.T) {
	srv := newProxy(t, egress.Policy{Observe: true}) // observing: still refused
	code, c := connect(t, srv.URL, "203.0.113.7:443")
	defer c.Close()
	if code != http.StatusForbidden {
		t.Fatalf("CONNECT to a bare address returned %d, want 403", code)
	}
}

// A policy file that stops being valid JSON must not open the door.
//
// The dangerous failure is not the parse error; it is replacing a working
// policy with an empty permissive one because somebody mistyped a comma.
func TestABrokenPolicyFileKeepsTheOldOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "egress.json")
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"observe": false, "allow": [{"host": "good.example"}]}`)

	p := &proxy{log: slog.New(slog.NewTextHandler(io.Discard, nil)), policyPath: path}
	p.load()
	if got := p.policy.Load(); len(got.Rules) != 1 || got.Observe {
		t.Fatalf("first load: %+v", got)
	}

	write(`{"allow": [ this is not json`)
	p.load()
	got := p.policy.Load()
	if len(got.Rules) != 1 || got.Rules[0].Host != "good.example" {
		t.Fatalf("a broken file replaced the working policy: %+v", got)
	}
	if got.Observe {
		t.Error("a broken file flipped enforcement off — the door opened on a syntax error")
	}
}

// A missing policy file allows nothing by name.
func TestAMissingPolicyFileAllowsNothing(t *testing.T) {
	p := &proxy{
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		policyPath: filepath.Join(t.TempDir(), "absent.json"),
	}
	p.load()
	if v := p.policy.Load().Check("anything.example", 443); v.Allow {
		t.Fatal("with no policy file at all, a destination was allowed by name")
	}
}

// The file's own observe flag beats the process flag, so the mode lives next
// to the list it applies to.
func TestTheFileDecidesTheMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "egress.json")
	if err := os.WriteFile(path, []byte(`{"observe": false, "allow": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &proxy{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		policyPath:     path,
		defaultObserve: true, // process says observe; the file says enforce
	}
	p.load()
	if p.policy.Load().Observe {
		t.Fatal("the file said enforce and was overruled by the flag")
	}
}

// The on-disk shape is what operators will actually write.
func TestThePolicyFileShapeRoundTrips(t *testing.T) {
	in := policyFile{Allow: []egress.Rule{
		{Host: "api.moonshot.cn", Note: "模型 API"},
		{Host: "*.githubusercontent.com"},
		{Host: "git.tinci.com", Port: 6006, Note: "内网 GitLab"},
	}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out policyFile
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Allow) != 3 || out.Allow[2].Port != 6006 || out.Allow[0].Note != "模型 API" {
		t.Fatalf("round trip lost something: %+v", out)
	}
}
