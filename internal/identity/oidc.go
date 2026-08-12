package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDC verifies ID tokens against the provider's own signing keys.
//
// celld verifies the token itself rather than trusting an identity header
// from a gateway. celld is reachable inside the cluster by construction, so
// header trust would let anything on the pod network assert any user — the
// identity layer would be decorative (ADR-0008).
type OIDC struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	// RedirectURL is where the provider sends the browser back. Empty means
	// it is derived per-request from the console's own origin.
	RedirectURL string
	// Scopes beyond openid; "profile" and "email" populate the display name.
	Scopes []string

	mu       sync.Mutex
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	lastTry  time.Time
	lastErr  error
}

// discoveryRetry bounds how often a cold or unreachable IdP is re-probed,
// so a login storm cannot turn into a discovery storm.
const discoveryRetry = 15 * time.Second

// Configured reports whether an identity provider was set up at all.
func (o *OIDC) Configured() bool {
	return o != nil && o.IssuerURL != "" && o.ClientID != ""
}

// ensure performs provider discovery lazily.
//
// Deliberately not done at startup: celld would then crash-loop whenever the
// IdP is slow to come up — a control plane that cannot start because the
// login service is down is worse than one that cannot log people in.
func (o *OIDC) ensure(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.verifier != nil {
		return nil
	}
	if time.Since(o.lastTry) < discoveryRetry && o.lastErr != nil {
		return o.lastErr
	}
	o.lastTry = time.Now()
	p, err := oidc.NewProvider(ctx, o.IssuerURL)
	if err != nil {
		o.lastErr = fmt.Errorf("oidc discovery on %s: %w", o.IssuerURL, err)
		return o.lastErr
	}
	o.provider, o.verifier, o.lastErr = p, p.Verifier(&oidc.Config{ClientID: o.ClientID}), nil
	return nil
}

// Verify checks signature, issuer, audience and expiry, and returns the
// principal the token asserts.
func (o *OIDC) Verify(ctx context.Context, rawIDToken string) (Principal, error) {
	if !o.Configured() {
		return Principal{}, errors.New("no identity provider configured")
	}
	if err := o.ensure(ctx); err != nil {
		return Principal{}, err
	}
	o.mu.Lock()
	v := o.verifier
	o.mu.Unlock()

	tok, err := v.Verify(ctx, rawIDToken)
	if err != nil {
		return Principal{}, err
	}
	var claims struct {
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
	}
	// Claims are optional: a provider that returns only `sub` still yields a
	// usable principal, it just has nothing pretty to display.
	_ = tok.Claims(&claims)
	name := claims.Name
	if name == "" {
		name = claims.PreferredUsername
	}
	return Principal{
		Subject: OIDCSubject(tok.Issuer, tok.Subject),
		Name:    name,
		Email:   claims.Email,
		Kind:    KindOIDC,
	}, nil
}

func (o *OIDC) oauth2Config(redirectURL string) (*oauth2.Config, error) {
	o.mu.Lock()
	p := o.provider
	o.mu.Unlock()
	if p == nil {
		return nil, errors.New("identity provider not reachable yet")
	}
	scopes := append([]string{oidc.ScopeOpenID}, o.Scopes...)
	return &oauth2.Config{
		ClientID:     o.ClientID,
		ClientSecret: o.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  redirectURL,
		Scopes:       scopes,
	}, nil
}

// AuthCodeURL starts an authorization-code flow with PKCE.
//
// celld runs the flow itself so the browser login works with no gateway in
// front of it. PKCE is used even though this is a confidential client: it
// costs nothing and removes the code-interception class of attack outright.
func (o *OIDC) AuthCodeURL(ctx context.Context, redirectURL, state, verifier string) (string, error) {
	if err := o.ensure(ctx); err != nil {
		return "", err
	}
	cfg, err := o.oauth2Config(redirectURL)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return cfg.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:])),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	), nil
}

// Exchange trades the authorization code for tokens and returns the raw ID
// token, which the caller stores in the session cookie.
func (o *OIDC) Exchange(ctx context.Context, redirectURL, code, verifier string) (string, error) {
	if err := o.ensure(ctx); err != nil {
		return "", err
	}
	cfg, err := o.oauth2Config(redirectURL)
	if err != nil {
		return "", err
	}
	tok, err := cfg.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", verifier))
	if err != nil {
		return "", err
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		// Without an ID token there is no verifiable identity — an access
		// token alone says nothing about who the user is.
		return "", errors.New("provider returned no id_token")
	}
	return raw, nil
}

// RandomString produces state and PKCE verifiers.
func RandomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is not recoverable and must never degrade to a
		// guessable value.
		panic("identity: crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
