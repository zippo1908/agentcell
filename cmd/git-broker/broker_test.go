package main

import (
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// pkt encodes one git pkt-line.
func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

const flushPkt = "0000"

func TestParseAndCheckRefPolicy(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{
			name: "session branch allowed",
			body: pkt("0000000000000000000000000000000000000000 abcdef0000000000000000000000000000000001 refs/heads/session/01xyz\x00report-status") + flushPkt,
		},
		{
			name:    "base branch rejected",
			body:    pkt("dead00 beef11 refs/heads/main\x00report-status") + flushPkt,
			wantErr: true,
		},
		{
			name:    "arbitrary ref rejected",
			body:    pkt("dead00 beef11 refs/heads/hack") + flushPkt,
			wantErr: true,
		},
		{
			name:    "delete of session branch rejected",
			body:    pkt("abc123 0000000000000000000000000000000000000000 refs/heads/session/gone") + flushPkt,
			wantErr: true,
		},
		{
			name:    "mixed session + base rejected",
			body:    pkt("a b refs/heads/session/ok") + pkt("c d refs/heads/main") + flushPkt,
			wantErr: true,
		},
		{
			name:    "empty rejected",
			body:    flushPkt,
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmds, err := parseReceivePackCommands([]byte(c.body))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			err = checkRefPolicy(cmds)
			if c.wantErr && err == nil {
				t.Errorf("expected policy rejection, got none (cmds=%v)", cmds)
			}
			if !c.wantErr && err != nil {
				t.Errorf("expected accept, got %v", err)
			}
		})
	}
}

func TestEnforcePushPolicyForwardsOriginalBytesAndHandlesGzip(t *testing.T) {
	body := pkt("0000000000000000000000000000000000000000 abc refs/heads/session/x\x00report-status") + flushPkt + "PACKDATA"

	// plain
	out, err := enforcePushPolicy(strings.NewReader(body), false)
	if err != nil {
		t.Fatalf("plain: %v", err)
	}
	got, _ := io.ReadAll(out)
	if string(got) != body {
		t.Error("plain body not forwarded byte-identically")
	}

	// gzipped: policy must still inspect, and forward the ORIGINAL gzip bytes
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(body))
	_ = zw.Close()
	raw := gz.Bytes()
	out, err = enforcePushPolicy(bytes.NewReader(raw), true)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	got, _ = io.ReadAll(out)
	if !bytes.Equal(got, raw) {
		t.Error("gzip body not forwarded byte-identically")
	}

	// gzipped push to a forbidden ref must be rejected
	var bad bytes.Buffer
	zw = gzip.NewWriter(&bad)
	_, _ = zw.Write([]byte(pkt("a b refs/heads/main") + flushPkt))
	_ = zw.Close()
	if _, err := enforcePushPolicy(bytes.NewReader(bad.Bytes()), true); err == nil {
		t.Error("expected rejection of gzipped push to main")
	}
}

func TestSplitCellPath(t *testing.T) {
	for _, c := range []struct{ in, cell, rest string }{
		{"/shop/info/refs", "shop", "info/refs"},
		{"/shop/git-receive-pack", "shop", "git-receive-pack"},
		{"/shop", "shop", ""},
		{"/", "", ""},
	} {
		cell, rest := splitCellPath(c.in)
		if cell != c.cell || rest != c.rest {
			t.Errorf("splitCellPath(%q) = (%q,%q), want (%q,%q)", c.in, cell, rest, c.cell, c.rest)
		}
	}
}

func TestGithubAPIBase(t *testing.T) {
	for _, c := range []struct{ url, want string }{
		{"https://github.com/you/repo.git", "https://api.github.com"},
		{"https://ghe.corp.com/you/repo.git", "https://ghe.corp.com/api/v3"},
	} {
		if got := githubAPIBase(c.url); got != c.want {
			t.Errorf("githubAPIBase(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

// appJWT must produce a well-formed RS256 JWT verifiable with the public key.
func TestAppJWT(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	now := time.Unix(1_700_000_000, 0)
	jwt, err := appJWT("12345", pemBytes, now)
	if err != nil {
		t.Fatalf("appJWT: %v", err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts, want 3", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Errorf("signature does not verify: %v", err)
	}
}
