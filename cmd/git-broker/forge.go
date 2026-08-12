package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

// ADR-0006: the broker also fronts a NARROW slice of the forge REST API so
// the control plane can diff a session branch, open a PR and track its
// merge state without ever holding a forge credential. This is deliberately
// not a general proxy — only the operations below are reachable, with the
// PR head/base constrained the same way pushes are.

// forgeRequest is what celld posts to /forge/<cell>.
type forgeRequest struct {
	// Op is one of: compare | pull-create | pull-get.
	Op string `json:"op"`
	// SessionID identifies the session branch involved.
	SessionID string `json:"sessionID"`
	// Title/Body are used by pull-create.
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	// Number is used by pull-get.
	Number int `json:"number,omitempty"`
}

type forgeResponse struct {
	// compare
	Files     []forgeFile `json:"files,omitempty"`
	Additions int         `json:"additions,omitempty"`
	Deletions int         `json:"deletions,omitempty"`
	// pull-create / pull-get
	URL    string `json:"url,omitempty"`
	Number int    `json:"number,omitempty"`
	State  string `json:"state,omitempty"`
}

type forgeFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch,omitempty"`
}

// handleForge serves POST /forge/<cell> for the control plane only.
func (s *server) handleForge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	cell, _ := splitCellPath(strings.TrimPrefix(r.URL.Path, "/forge"))
	if cell == "" {
		http.Error(w, "usage: POST /forge/<cell>", http.StatusBadRequest)
		return
	}
	id, err := s.authenticate(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Basic realm=agentcell")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Only the control plane's own ServiceAccount may use the forge API —
	// never a workload SA, whatever namespace it is in.
	if id.namespace != s.controlNS || id.saName != runtimeapi.SACelld {
		http.Error(w, "forge API is restricted to the control plane", http.StatusForbidden)
		return
	}

	var req forgeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}

	var c acv1.Cell
	if err := s.k8s.Get(s.ctx(), types.NamespacedName{Namespace: s.controlNS, Name: cell}, &c); err != nil {
		http.Error(w, "cell not found", http.StatusNotFound)
		return
	}
	if c.Spec.Repo.SecretName == "" {
		http.Error(w, "cell has no git secret configured", http.StatusForbidden)
		return
	}
	var secret corev1.Secret
	if err := s.k8s.Get(s.ctx(), types.NamespacedName{Namespace: s.controlNS, Name: c.Spec.Repo.SecretName}, &secret); err != nil {
		http.Error(w, "git secret not found", http.StatusInternalServerError)
		return
	}
	bound := strings.TrimSpace(string(secret.Data["repo_url"]))
	if bound == "" || !sameRepo(bound, c.Spec.Repo.URL) {
		http.Error(w, "cell repo url is not authorized by this credential", http.StatusForbidden)
		return
	}
	cred, err := s.creds.credentials(s.ctx(), c.Spec.Repo.URL, secret.Data)
	if err != nil {
		http.Error(w, "credential error", http.StatusInternalServerError)
		return
	}

	out, status, err := s.forgeCall(&c, cred, req, forgeKind(&c, secret.Data))
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// forgeKind selects the forge API dialect. An explicit `forge` key in the
// git secret wins; otherwise github.com is GitHub and anything else is
// assumed to be GitLab, which is what self-hosted/on-prem clusters run.
func forgeKind(c *acv1.Cell, secret map[string][]byte) string {
	if v := strings.TrimSpace(string(secret["forge"])); v != "" {
		return strings.ToLower(v)
	}
	if u, err := url.Parse(c.Spec.Repo.URL); err == nil {
		host := strings.ToLower(u.Hostname())
		if host == "github.com" || strings.HasSuffix(host, ".github.com") {
			return "github"
		}
	}
	return "gitlab"
}

// forgeCall performs one allow-listed operation against the forge.
func (s *server) forgeCall(c *acv1.Cell, cred forgeCred, req forgeRequest, kind string) (*forgeResponse, int, error) {
	base := c.Spec.Repo.Branch
	if base == "" {
		base = "main"
	}
	if kind == "gitlab" {
		return s.gitlabCall(c.Spec.Repo.URL, base, cred, req)
	}
	owner, repo, err := ownerRepo(c.Spec.Repo.URL)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	api := githubAPIBase(c.Spec.Repo.URL)

	switch req.Op {
	case "compare":
		if req.SessionID == "" {
			return nil, http.StatusBadRequest, fmt.Errorf("sessionID required")
		}
		head := "session/" + req.SessionID
		u := fmt.Sprintf("%s/repos/%s/%s/compare/%s...%s", api, owner, repo,
			url.PathEscape(base), url.PathEscape(head))
		var payload struct {
			Files []struct {
				Filename  string `json:"filename"`
				Status    string `json:"status"`
				Additions int    `json:"additions"`
				Deletions int    `json:"deletions"`
				Patch     string `json:"patch"`
			} `json:"files"`
		}
		if err := s.forgeJSON(http.MethodGet, u, cred, nil, &payload); err != nil {
			return nil, http.StatusBadGateway, err
		}
		out := &forgeResponse{}
		for _, f := range payload.Files {
			out.Files = append(out.Files, forgeFile{
				Filename: f.Filename, Status: f.Status,
				Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch,
			})
			out.Additions += f.Additions
			out.Deletions += f.Deletions
		}
		return out, http.StatusOK, nil

	case "pull-create":
		if req.SessionID == "" {
			return nil, http.StatusBadRequest, fmt.Errorf("sessionID required")
		}
		// Constrained like pushes: head must be this session's branch, base
		// the Cell's base branch. The review channel cannot be used to make
		// arbitrary changes.
		body := map[string]any{
			"title": req.Title, "body": req.Body,
			"head": "session/" + req.SessionID, "base": base,
		}
		var payload struct {
			HTMLURL string `json:"html_url"`
			Number  int    `json:"number"`
			State   string `json:"state"`
		}
		u := fmt.Sprintf("%s/repos/%s/%s/pulls", api, owner, repo)
		if err := s.forgeJSON(http.MethodPost, u, cred, body, &payload); err != nil {
			return nil, http.StatusBadGateway, err
		}
		return &forgeResponse{URL: payload.HTMLURL, Number: payload.Number, State: payload.State},
			http.StatusOK, nil

	case "pull-find":
		// Idempotency support: find an existing PR for this session's head
		// branch regardless of state, so a create that succeeded but whose
		// status write was lost can be recovered instead of re-created.
		if req.SessionID == "" {
			return nil, http.StatusBadRequest, fmt.Errorf("sessionID required")
		}
		u := fmt.Sprintf("%s/repos/%s/%s/pulls?state=all&head=%s:session/%s&per_page=1",
			api, owner, repo, url.QueryEscape(owner), url.PathEscape(req.SessionID))
		var payload []struct {
			HTMLURL  string  `json:"html_url"`
			Number   int     `json:"number"`
			State    string  `json:"state"`
			Merged   bool    `json:"merged"`
			MergedAt *string `json:"merged_at"`
		}
		if err := s.forgeJSON(http.MethodGet, u, cred, nil, &payload); err != nil {
			return nil, http.StatusBadGateway, err
		}
		if len(payload) == 0 {
			return &forgeResponse{}, http.StatusOK, nil // none yet
		}
		p := payload[0]
		state := p.State
		if p.Merged || p.MergedAt != nil {
			state = "merged"
		}
		return &forgeResponse{URL: p.HTMLURL, Number: p.Number, State: state}, http.StatusOK, nil

	case "pull-get":
		if req.Number == 0 {
			return nil, http.StatusBadRequest, fmt.Errorf("number required")
		}
		u := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", api, owner, repo, req.Number)
		var payload struct {
			HTMLURL string `json:"html_url"`
			Number  int    `json:"number"`
			State   string `json:"state"`
			Merged  bool   `json:"merged"`
		}
		if err := s.forgeJSON(http.MethodGet, u, cred, nil, &payload); err != nil {
			return nil, http.StatusBadGateway, err
		}
		state := payload.State
		if payload.Merged {
			state = "merged"
		}
		return &forgeResponse{URL: payload.HTMLURL, Number: payload.Number, State: state},
			http.StatusOK, nil
	}
	return nil, http.StatusBadRequest, fmt.Errorf("operation %q is not allowed", req.Op)
}

// forgeJSONHeaders is forgeJSON with explicit headers instead of basic
// auth — GitLab authenticates its API with PRIVATE-TOKEN.
func (s *server) forgeJSONHeaders(method, u string, headers map[string]string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(s.ctx(), method, u, rdr)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.creds.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("forge returned %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (s *server) forgeJSON(method, u string, cred forgeCred, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(s.ctx(), method, u, rdr)
	if err != nil {
		return err
	}
	req.SetBasicAuth(cred.username, cred.password)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.creds.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("forge returned %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	return json.Unmarshal(raw, out)
}

// ownerRepo splits https://host/owner/repo(.git) into owner and repo.
func ownerRepo(repoURL string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil {
		return "", "", err
	}
	p := strings.Trim(strings.TrimSuffix(strings.TrimRight(u.Path, "/"), ".git"), "/")
	parts := strings.Split(p, "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("cannot derive owner/repo from %q", repoURL)
	}
	return parts[len(parts)-2], parts[len(parts)-1], nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
