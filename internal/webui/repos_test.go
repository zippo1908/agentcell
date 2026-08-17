package webui

import (
	"strings"
	"testing"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
)

// A project made of several repositories, and why the layout is validated
// rather than discovered.

func group(repos ...acv1.RepoSpec) *acv1.Cell {
	c := &acv1.Cell{}
	c.Spec.Repo = repos[0]
	c.Spec.Repos = repos[1:]
	return c
}

// The single-repo project is the one every existing Cell is: one entry, at
// the workspace root, nothing moved.
func TestSingleRepoKeepsTheRootAndNeedsNoPath(t *testing.T) {
	c := group(acv1.RepoSpec{URL: "https://git/x.git"})
	all := c.AllRepos()
	if len(all) != 1 || all[0].Path != "" {
		t.Fatalf("got %+v, want one repo at the root", all)
	}
	if all[0].Name != "main" {
		t.Errorf("name = %q, want a usable default", all[0].Name)
	}
	if err := validateRepoLayout(c); err != nil {
		t.Errorf("a single-repo project was rejected: %v", err)
	}
}

// In a group every repository needs a directory: "at the root" only means
// something when there is nothing to sit beside.
func TestGroupRequiresEveryRepoToHaveItsOwnDirectory(t *testing.T) {
	c := group(
		acv1.RepoSpec{Name: "api", URL: "https://git/api.git"}, // no path
		acv1.RepoSpec{Name: "web", Path: "web", URL: "https://git/web.git"},
	)
	err := validateRepoLayout(c)
	if err == nil {
		t.Fatal("a repo without a directory was accepted in a group")
	}
	if !strings.Contains(err.Error(), "api") {
		t.Errorf("the refusal does not name the offending repo: %v", err)
	}
}

// Two clones into one directory is one clone overwriting another. Catching
// it here beats discovering it as a workspace that is quietly wrong.
func TestTwoReposCannotShareADirectory(t *testing.T) {
	c := group(
		acv1.RepoSpec{Name: "api", Path: "svc", URL: "https://git/api.git"},
		acv1.RepoSpec{Name: "web", Path: "svc", URL: "https://git/web.git"},
	)
	if err := validateRepoLayout(c); err == nil {
		t.Fatal("two repositories were allowed to occupy the same directory")
	}
}

func TestRepoPathsStayInsideTheWorkspace(t *testing.T) {
	for _, bad := range []string{"../etc", "/etc"} {
		c := group(
			acv1.RepoSpec{Name: "api", Path: "api", URL: "https://git/api.git"},
			acv1.RepoSpec{Name: "x", Path: bad, URL: "https://git/x.git"},
		)
		if err := validateRepoLayout(c); err == nil {
			t.Errorf("path %q was accepted", bad)
		}
	}
}

// Each repository keeps its own base branch: they are separate repositories
// on the forge, and assuming one branch name for all of them is the kind of
// fiction that fails on the first project with a `master`.
func TestEachRepoKeepsItsOwnBaseBranch(t *testing.T) {
	c := group(
		acv1.RepoSpec{Name: "api", Path: "api", URL: "https://git/api.git", Branch: "master"},
		acv1.RepoSpec{Name: "web", Path: "web", URL: "https://git/web.git"},
	)
	all := c.AllRepos()
	if all[0].Branch != "master" {
		t.Errorf("branch = %q, want the one that was set", all[0].Branch)
	}
	if all[1].Branch != "main" {
		t.Errorf("unset branch = %q, want the default", all[1].Branch)
	}
}
