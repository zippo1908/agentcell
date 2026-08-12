package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ADR-0005 v3: the broker prefers short-lived credentials over a long-lived
// PAT. Selection is by the shape of the Cell's git Secret:
//
//   - keys username + password        -> static credential (the PAT itself)
//   - keys github_app_id, github_app_installation_id, github_app_private_key
//                                      -> mint a GitHub App installation token
//                                         (~1h TTL), cached until near expiry
//
// So a deployment opts into short-lived tokens simply by storing App
// credentials instead of a PAT; no code change per Cell.

type forgeCred struct {
	username string
	password string
}

type cachedToken struct {
	token   string
	expires time.Time
}

// credProvider resolves forge credentials from a Cell's secret data, caching
// minted installation tokens.
type credProvider struct {
	mu    sync.Mutex
	cache map[string]cachedToken // keyed by installation id
	http  *http.Client
	now   func() time.Time
}

func newCredProvider() *credProvider {
	return &credProvider{
		cache: map[string]cachedToken{},
		http:  &http.Client{Timeout: 15 * time.Second},
		now:   time.Now,
	}
}

// credentials returns the basic-auth pair to present to the forge for repoURL.
func (p *credProvider) credentials(ctx context.Context, repoURL string, secret map[string][]byte) (forgeCred, error) {
	if appID, ok := secret["github_app_id"]; ok {
		return p.githubApp(ctx, repoURL,
			strings.TrimSpace(string(appID)),
			strings.TrimSpace(string(secret["github_app_installation_id"])),
			secret["github_app_private_key"])
	}
	u := strings.TrimSpace(string(secret["username"]))
	pw := strings.TrimSpace(string(secret["password"]))
	if pw == "" {
		return forgeCred{}, fmt.Errorf("git secret has neither github_app_* nor username/password")
	}
	if u == "" {
		u = "x-access-token"
	}
	return forgeCred{username: u, password: pw}, nil
}

func (p *credProvider) githubApp(ctx context.Context, repoURL, appID, installID string, keyPEM []byte) (forgeCred, error) {
	if appID == "" || installID == "" || len(keyPEM) == 0 {
		return forgeCred{}, fmt.Errorf("incomplete github_app_* credentials")
	}
	p.mu.Lock()
	if c, ok := p.cache[installID]; ok && p.now().Before(c.expires.Add(-2*time.Minute)) {
		p.mu.Unlock()
		return forgeCred{username: "x-access-token", password: c.token}, nil
	}
	p.mu.Unlock()

	jwt, err := appJWT(appID, keyPEM, p.now())
	if err != nil {
		return forgeCred{}, err
	}
	apiBase := githubAPIBase(repoURL)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		apiBase+"/app/installations/"+url.PathEscape(installID)+"/access_tokens", nil)
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := p.http.Do(req)
	if err != nil {
		return forgeCred{}, fmt.Errorf("mint installation token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return forgeCred{}, fmt.Errorf("installation token endpoint returned %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return forgeCred{}, fmt.Errorf("decode installation token: %w", err)
	}
	p.mu.Lock()
	p.cache[installID] = cachedToken{token: out.Token, expires: out.ExpiresAt}
	p.mu.Unlock()
	return forgeCred{username: "x-access-token", password: out.Token}, nil
}

// githubAPIBase derives the API base from the repo host: api.github.com for
// github.com, https://<host>/api/v3 for GitHub Enterprise.
func githubAPIBase(repoURL string) string {
	u, err := url.Parse(repoURL)
	if err != nil || u.Host == "" || u.Host == "github.com" {
		return "https://api.github.com"
	}
	return "https://" + u.Host + "/api/v3"
}

// appJWT builds an RS256-signed GitHub App JWT by hand (no jwt dependency).
func appJWT(appID string, keyPEM []byte, now time.Time) (string, error) {
	key, err := parseRSAKey(keyPEM)
	if err != nil {
		return "", err
	}
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	header := enc(map[string]string{"alg": "RS256", "typ": "JWT"})
	// iat backdated 60s for clock skew; exp within GitHub's 10-minute limit.
	claims := enc(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	})
	signing := header + "." + claims
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign app jwt: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func parseRSAKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(bytes.TrimSpace(pemBytes))
	if block == nil {
		return nil, fmt.Errorf("github_app_private_key is not valid PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse RSA private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("github_app_private_key is not an RSA key")
	}
	return rk, nil
}
