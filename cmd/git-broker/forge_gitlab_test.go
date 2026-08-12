package main

import (
	"testing"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
)

func TestForgeKindSelection(t *testing.T) {
	cell := func(u string) *acv1.Cell {
		c := &acv1.Cell{}
		c.Spec.Repo.URL = u
		return c
	}
	cases := []struct {
		url    string
		secret map[string][]byte
		want   string
	}{
		{"https://github.com/you/repo.git", nil, "github"},
		// Self-hosted forges are the on-prem norm; assume GitLab there.
		{"http://git.example.com:6006/group/repo.git", nil, "gitlab"},
		// An explicit key always wins (GitHub Enterprise on a custom host).
		{"https://ghe.corp.com/you/repo.git", map[string][]byte{"forge": []byte("github")}, "github"},
		{"https://github.com/you/repo.git", map[string][]byte{"forge": []byte("gitlab")}, "gitlab"},
	}
	for _, c := range cases {
		if got := forgeKind(cell(c.url), c.secret); got != c.want {
			t.Errorf("forgeKind(%s, %v) = %q, want %q", c.url, c.secret, got, c.want)
		}
	}
}

func TestGitlabAddressing(t *testing.T) {
	const repo = "http://git.example.com:6006/group/sub/agentcell_e2etest.git"
	api, err := gitlabAPIBase(repo)
	if err != nil || api != "http://git.example.com:6006/api/v4" {
		t.Errorf("gitlabAPIBase = %q, %v", api, err)
	}
	// The whole namespaced path is the project id, URL-encoded.
	id, err := gitlabProjectID(repo)
	if err != nil || id != "group%2Fsub%2Fagentcell_e2etest" {
		t.Errorf("gitlabProjectID = %q, %v", id, err)
	}
	if _, err := gitlabProjectID("http://git.example.com:6006/noslash"); err == nil {
		t.Error("a path without a group must be rejected")
	}
}

func TestGitlabStateMapping(t *testing.T) {
	merged := "2026-08-12T00:00:00Z"
	empty := ""
	cases := []struct {
		state    string
		mergedAt *string
		want     string
	}{
		{"opened", nil, "open"},
		{"merged", &merged, "merged"},
		{"opened", &merged, "merged"}, // merged_at wins
		{"closed", nil, "closed"},
		{"opened", &empty, "open"},
	}
	for _, c := range cases {
		if got := gitlabState(c.state, c.mergedAt); got != c.want {
			t.Errorf("gitlabState(%q,%v) = %q, want %q", c.state, c.mergedAt, got, c.want)
		}
	}
}

// GitLab does not summarise diffs, so the broker counts the lines itself.
func TestCountDiffLines(t *testing.T) {
	diff := "--- a/x\n+++ b/x\n@@ -1,2 +1,3 @@\n context\n+added one\n+added two\n-removed one\n"
	add, del := countDiffLines(diff)
	if add != 2 || del != 1 {
		t.Errorf("countDiffLines = +%d -%d, want +2 -1 (file headers must not count)", add, del)
	}
}
