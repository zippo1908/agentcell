package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// GitLab implementation of the same three allow-listed operations
// (ADR-0006). Self-hosted GitLab is the common case for on-prem and China
// deployments, where GitHub is often unreachable — so this is not an
// afterthought, it is the primary forge for those clusters.
//
// Differences that matter:
//   - the project is addressed by URL-encoded "group/name", not owner+repo;
//   - authentication is a PRIVATE-TOKEN header, not basic auth;
//   - merge requests are numbered by `iid` (per project), not a global id;
//   - compare returns raw diffs with no line counts, so we count them.

func gitlabAPIBase(repoURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("cannot derive GitLab API base from %q", repoURL)
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + u.Host + "/api/v4", nil
}

// gitlabProjectID is the URL-encoded "group/subgroup/name" path.
func gitlabProjectID(repoURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil {
		return "", err
	}
	p := strings.Trim(strings.TrimSuffix(strings.TrimRight(u.Path, "/"), ".git"), "/")
	if p == "" || !strings.Contains(p, "/") {
		return "", fmt.Errorf("cannot derive project path from %q", repoURL)
	}
	return url.PathEscape(p), nil
}

// gitlabState maps GitLab's merge-request states onto ours.
func gitlabState(state string, mergedAt *string) string {
	if mergedAt != nil && *mergedAt != "" {
		return "merged"
	}
	switch state {
	case "opened":
		return "open"
	case "merged":
		return "merged"
	}
	return "closed"
}

// countDiffLines derives added/removed counts from a unified diff body,
// which GitLab (unlike GitHub) does not summarise for us.
func countDiffLines(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			// file headers, not content
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

func (s *server) gitlabCall(repoURL, base string, cred forgeCred, req forgeRequest) (*forgeResponse, int, error) {
	api, err := gitlabAPIBase(repoURL)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	project, err := gitlabProjectID(repoURL)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	// GitLab authenticates API calls with a token header; the credential's
	// password carries the token (the same one git uses over HTTP).
	hdr := map[string]string{"PRIVATE-TOKEN": cred.password}

	switch req.Op {
	case "compare":
		if req.SessionID == "" {
			return nil, http.StatusBadRequest, fmt.Errorf("sessionID required")
		}
		head := "session/" + req.SessionID
		u := fmt.Sprintf("%s/projects/%s/repository/compare?from=%s&to=%s",
			api, project, url.QueryEscape(base), url.QueryEscape(head))
		var payload struct {
			Diffs []struct {
				OldPath     string `json:"old_path"`
				NewPath     string `json:"new_path"`
				Diff        string `json:"diff"`
				NewFile     bool   `json:"new_file"`
				DeletedFile bool   `json:"deleted_file"`
				RenamedFile bool   `json:"renamed_file"`
			} `json:"diffs"`
		}
		if err := s.forgeJSONHeaders(http.MethodGet, u, hdr, nil, &payload); err != nil {
			return nil, http.StatusBadGateway, err
		}
		out := &forgeResponse{}
		for _, d := range payload.Diffs {
			status := "modified"
			switch {
			case d.NewFile:
				status = "added"
			case d.DeletedFile:
				status = "removed"
			case d.RenamedFile:
				status = "renamed"
			}
			add, del := countDiffLines(d.Diff)
			out.Files = append(out.Files, forgeFile{
				Filename: d.NewPath, Status: status,
				Additions: add, Deletions: del, Patch: d.Diff,
			})
			out.Additions += add
			out.Deletions += del
		}
		return out, http.StatusOK, nil

	case "pull-find":
		if req.SessionID == "" {
			return nil, http.StatusBadRequest, fmt.Errorf("sessionID required")
		}
		u := fmt.Sprintf("%s/projects/%s/merge_requests?state=all&source_branch=%s&per_page=1",
			api, project, url.QueryEscape("session/"+req.SessionID))
		var payload []struct {
			WebURL   string  `json:"web_url"`
			IID      int     `json:"iid"`
			State    string  `json:"state"`
			MergedAt *string `json:"merged_at"`
		}
		if err := s.forgeJSONHeaders(http.MethodGet, u, hdr, nil, &payload); err != nil {
			return nil, http.StatusBadGateway, err
		}
		if len(payload) == 0 {
			return &forgeResponse{}, http.StatusOK, nil
		}
		p := payload[0]
		return &forgeResponse{URL: p.WebURL, Number: p.IID, State: gitlabState(p.State, p.MergedAt)},
			http.StatusOK, nil

	case "pull-create":
		if req.SessionID == "" {
			return nil, http.StatusBadRequest, fmt.Errorf("sessionID required")
		}
		// Same constraint as GitHub: source must be this session's branch,
		// target the Cell's base branch.
		body := map[string]any{
			"source_branch": "session/" + req.SessionID,
			"target_branch": base,
			"title":         req.Title,
			"description":   req.Body,
		}
		var payload struct {
			WebURL   string  `json:"web_url"`
			IID      int     `json:"iid"`
			State    string  `json:"state"`
			MergedAt *string `json:"merged_at"`
		}
		u := fmt.Sprintf("%s/projects/%s/merge_requests", api, project)
		if err := s.forgeJSONHeaders(http.MethodPost, u, hdr, body, &payload); err != nil {
			return nil, http.StatusBadGateway, err
		}
		return &forgeResponse{URL: payload.WebURL, Number: payload.IID, State: gitlabState(payload.State, payload.MergedAt)},
			http.StatusOK, nil

	case "pull-get":
		if req.Number == 0 {
			return nil, http.StatusBadRequest, fmt.Errorf("number required")
		}
		u := fmt.Sprintf("%s/projects/%s/merge_requests/%d", api, project, req.Number)
		var payload struct {
			WebURL   string  `json:"web_url"`
			IID      int     `json:"iid"`
			State    string  `json:"state"`
			MergedAt *string `json:"merged_at"`
		}
		if err := s.forgeJSONHeaders(http.MethodGet, u, hdr, nil, &payload); err != nil {
			return nil, http.StatusBadGateway, err
		}
		return &forgeResponse{URL: payload.WebURL, Number: payload.IID, State: gitlabState(payload.State, payload.MergedAt)},
			http.StatusOK, nil
	}
	return nil, http.StatusBadRequest, fmt.Errorf("operation %q is not allowed", req.Op)
}
