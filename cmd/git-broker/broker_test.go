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
	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"io"
	"strings"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktypes "k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// pkt encodes one git pkt-line.
func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

const flushPkt = "0000"

func TestParseAndCheckRefPolicy(t *testing.T) {
	const sess = "01xyz"
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{
			name: "own session branch allowed",
			body: pkt("0000000000000000000000000000000000000000 abcdef0000000000000000000000000000000001 refs/heads/session/01xyz\x00report-status") + flushPkt,
		},
		{
			name:    "another session's branch rejected",
			body:    pkt("dead00 beef11 refs/heads/session/other\x00report-status") + flushPkt,
			wantErr: true,
		},
		{
			name:    "base branch rejected",
			body:    pkt("dead00 beef11 refs/heads/main\x00report-status") + flushPkt,
			wantErr: true,
		},
		{
			name:    "delete of own branch rejected",
			body:    pkt("abc123 0000000000000000000000000000000000000000 refs/heads/session/01xyz") + flushPkt,
			wantErr: true,
		},
		{
			name:    "update of existing branch rejected (create-only)",
			body:    pkt("abc123 def456 refs/heads/session/01xyz") + flushPkt,
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
			err = checkRefPolicy(cmds, sess)
			if c.wantErr && err == nil {
				t.Errorf("expected policy rejection, got none (cmds=%v)", cmds)
			}
			if !c.wantErr && err != nil {
				t.Errorf("expected accept, got %v", err)
			}
		})
	}
}

func TestAuthorizeRoles(t *testing.T) {
	cellNS := "cell-shop"
	if err := authorize(identity{namespace: "cell-other", saName: "anchor"}, "shop", false); err == nil {
		t.Error("wrong namespace must be rejected")
	}
	if err := authorize(identity{namespace: cellNS, saName: "anchor"}, "shop", true); err == nil {
		t.Error("anchor must not push")
	}
	if err := authorize(identity{namespace: cellNS, saName: "prod"}, "shop", true); err == nil {
		t.Error("prod must not push")
	}
	if err := authorize(identity{namespace: cellNS, saName: "anchor"}, "shop", false); err != nil {
		t.Errorf("anchor fetch should be allowed: %v", err)
	}
	if err := authorize(identity{namespace: cellNS, saName: "default"}, "shop", false); err == nil {
		t.Error("unknown service account must be rejected")
	}
}

func TestSettleSessionVerifiesPodOwner(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	jobPod := func(name, uid, jobName string, controller bool) *corev1.Pod {
		p := &corev1.Pod{}
		p.Name, p.Namespace, p.UID = name, "cell-shop", ktypes.UID(uid)
		p.OwnerReferences = []metav1.OwnerReference{{Kind: "Job", Name: jobName, Controller: &controller}}
		return p
	}
	good := jobPod("settle-01hxyz-abc", "uid-1", "settle-01hxyz", true)
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(good).Build()
	s := &server{k8s: fc, controlNS: "agentcell-system"}

	id := identity{namespace: "cell-shop", saName: "settle", podName: "settle-01hxyz-abc", podUID: "uid-1"}
	sess, err := s.settleSession(id)
	if err != nil || sess != "01hxyz" {
		t.Fatalf("settleSession = %q, %v; want 01hxyz", sess, err)
	}

	// uid mismatch → rejected (stolen token replayed from another pod)
	if _, err := s.settleSession(identity{namespace: "cell-shop", saName: "settle", podName: "settle-01hxyz-abc", podUID: "wrong"}); err == nil {
		t.Error("uid mismatch must be rejected")
	}
	// non-settle role
	if _, err := s.settleSession(identity{namespace: "cell-shop", saName: "anchor", podName: "x", podUID: "y"}); err == nil {
		t.Error("only settle may push")
	}
	// pod not owned by a Job
	bad := &corev1.Pod{}
	bad.Name, bad.Namespace, bad.UID = "rogue", "cell-shop", "uid-2"
	fc2 := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bad).Build()
	s2 := &server{k8s: fc2, controlNS: "agentcell-system"}
	if _, err := s2.settleSession(identity{namespace: "cell-shop", saName: "settle", podName: "rogue", podUID: "uid-2"}); err == nil {
		t.Error("pod without a settle Job owner must be rejected")
	}
}

func TestSameRepoNormalization(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"https://github.com/You/Repo.git", "https://GitHub.com/You/Repo", true},
		{"https://github.com/you/repo", "https://github.com/you/repo/", true},
		{"https://github.com/you/repo.git", "https://github.com/you/other.git", false},
		{"https://github.com/you/repo", "https://evil.com/you/repo", false},
		{"", "https://github.com/you/repo", false},
	}
	for _, c := range cases {
		if got := sameRepo(c.a, c.b); got != c.want {
			t.Errorf("sameRepo(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestIdentityFromReview(t *testing.T) {
	u := userInfo("system:serviceaccount:cell-shop:settle", map[string][]string{
		"authentication.kubernetes.io/pod-name": {"settle-01h-abc"},
	})
	id, err := identityFromReview(u)
	if err != nil {
		t.Fatal(err)
	}
	if id.namespace != "cell-shop" || id.saName != "settle" || id.podName != "settle-01h-abc" {
		t.Errorf("identity = %+v", id)
	}
	if _, err := identityFromReview(userInfo("alice", nil)); err == nil {
		t.Error("non-SA username must be rejected")
	}
}

func TestEnforcePushPolicyForwardsOriginalBytesAndHandlesGzip(t *testing.T) {
	body := pkt("0000000000000000000000000000000000000000 abc refs/heads/session/x\x00report-status") + flushPkt + "PACKDATA"

	// plain
	out, err := enforcePushPolicy(strings.NewReader(body), false, "x")
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
	out, err = enforcePushPolicy(bytes.NewReader(raw), true, "x")
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
	if _, err := enforcePushPolicy(bytes.NewReader(bad.Bytes()), true, "x"); err == nil {
		t.Error("expected rejection of gzipped push to main")
	}
}

func userInfo(username string, extra map[string][]string) authnv1.UserInfo {
	u := authnv1.UserInfo{Username: username}
	if extra != nil {
		u.Extra = map[string]authnv1.ExtraValue{}
		for k, v := range extra {
			u.Extra[k] = authnv1.ExtraValue(v)
		}
	}
	return u
}

func TestSplitCellPath(t *testing.T) {
	for _, c := range []struct{ in, cell, repo, rest string }{
		// A single-repo project keeps the path it always had, so every
		// existing checkout and stored remote stays valid.
		{"/shop/info/refs", "shop", "", "info/refs"},
		{"/shop/git-receive-pack", "shop", "", "git-receive-pack"},
		{"/shop", "shop", "", ""},
		{"/", "", "", ""},
		// A project group names the repository. Without this every
		// repository routed to the same upstream and a two-repo workspace
		// quietly held the same code twice.
		{"/shop/~web/info/refs", "shop", "web", "info/refs"},
		{"/shop/~api/git-receive-pack", "shop", "api", "git-receive-pack"},
		{"/shop/~web", "shop", "web", ""},
	} {
		cell, repo, rest := splitCellPath(c.in)
		if cell != c.cell || repo != c.repo || rest != c.rest {
			t.Errorf("splitCellPath(%q) = (%q,%q,%q), want (%q,%q,%q)",
				c.in, cell, repo, rest, c.cell, c.repo, c.rest)
		}
	}
}

// An unknown repository name is refused, not quietly resolved to the default.
// Serving a different repository under the name somebody asked for is how a
// workspace ends up holding the wrong code with no error anywhere.
func TestUnknownRepoIsRefused(t *testing.T) {
	c := &acv1.Cell{}
	c.Spec.Repo = acv1.RepoSpec{Name: "api", Path: "api", URL: "https://git/api.git"}
	c.Spec.Repos = []acv1.RepoSpec{{Name: "web", Path: "web", URL: "https://git/web.git"}}

	if r, ok := repoOf(c, "web"); !ok || r.URL != "https://git/web.git" {
		t.Errorf("named repo resolved to %+v, ok=%v", r, ok)
	}
	if _, ok := repoOf(c, "mobile"); ok {
		t.Error("an unknown repository name was accepted")
	}
	if r, ok := repoOf(c, ""); !ok || r.Name != "api" {
		t.Errorf("empty name should mean the primary, got %+v", r)
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
