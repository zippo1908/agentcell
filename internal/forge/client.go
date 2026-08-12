// Package forge is celld's client for the broker's narrow forge REST route
// (ADR-0006). celld never holds a forge credential: it authenticates to the
// broker with its own audience-bound ServiceAccount token, and the broker
// injects the real credential.
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

type File struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch,omitempty"`
}

type Result struct {
	Files     []File `json:"files,omitempty"`
	Additions int    `json:"additions,omitempty"`
	Deletions int    `json:"deletions,omitempty"`
	URL       string `json:"url,omitempty"`
	Number    int    `json:"number,omitempty"`
	State     string `json:"state,omitempty"`
}

type request struct {
	Op        string `json:"op"`
	SessionID string `json:"sessionID,omitempty"`
	Title     string `json:"title,omitempty"`
	Body      string `json:"body,omitempty"`
	Number    int    `json:"number,omitempty"`
}

// Client talks to the broker. A zero BrokerURL disables it (direct mode),
// in which case every call returns ErrDisabled.
type Client struct {
	BrokerURL string
	TokenPath string
	HTTP      *http.Client

	mu    sync.Mutex
	token string
	read  time.Time
}

var ErrDisabled = fmt.Errorf("forge operations require the git-broker (--git-broker-url)")

func New(brokerURL string) *Client {
	return &Client{
		BrokerURL: strings.TrimRight(brokerURL, "/"),
		TokenPath: runtimeapi.SATokenPath,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) Enabled() bool { return c != nil && c.BrokerURL != "" }

// saToken reads celld's projected token, re-reading periodically since the
// kubelet rotates it.
func (c *Client) saToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Since(c.read) < time.Minute {
		return c.token, nil
	}
	raw, err := os.ReadFile(c.TokenPath)
	if err != nil {
		return "", fmt.Errorf("read service account token: %w", err)
	}
	c.token = strings.TrimSpace(string(raw))
	c.read = time.Now()
	return c.token, nil
}

func (c *Client) call(ctx context.Context, cell string, req request) (*Result, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	tok, err := c.saToken()
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BrokerURL+"/forge/"+cell, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.SetBasicAuth(runtimeapi.BrokerGitUser, tok)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("broker forge %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out Result
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode broker response: %w", err)
	}
	return &out, nil
}

// Compare returns the diff of a session branch against the base branch.
func (c *Client) Compare(ctx context.Context, cell, sessionID string) (*Result, error) {
	return c.call(ctx, cell, request{Op: "compare", SessionID: sessionID})
}

// CreatePull opens a PR from session/<id> to the Cell's base branch.
func (c *Client) CreatePull(ctx context.Context, cell, sessionID, title, body string) (*Result, error) {
	return c.call(ctx, cell, request{Op: "pull-create", SessionID: sessionID, Title: title, Body: body})
}

// FindPull returns the existing PR for session/<id> if there is one (any
// state); Number == 0 means none. Used to make PR creation idempotent.
func (c *Client) FindPull(ctx context.Context, cell, sessionID string) (*Result, error) {
	return c.call(ctx, cell, request{Op: "pull-find", SessionID: sessionID})
}

// GetPull fetches a PR's current state (open | merged | closed).
func (c *Client) GetPull(ctx context.Context, cell string, number int) (*Result, error) {
	return c.call(ctx, cell, request{Op: "pull-get", Number: number})
}
