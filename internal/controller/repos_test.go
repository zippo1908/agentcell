package controller

import (
	"os"
	"strings"
	"testing"
)

// Every workload that touches git needs the project's repositories.
//
// This is checked against the source because the failure mode is a MISSING
// line, not a wrong one: the repo list was added to the anchor, then to the
// session pod, then to the window — and each time the one that was forgotten
// silently fell back to the single-repo layout and went looking for a
// checkout a project group does not have. Three separate cluster runs found
// three separate omissions; a test that counts them costs nothing.
func TestEveryGitWorkloadGetsTheRepoList(t *testing.T) {
	for _, f := range []string{"cell_controller.go", "session_controller.go", "user_runtime.go"} {
		src := readControllerSource(t, f)
		if !strings.Contains(src, "runtimeapi.EnvRepos") {
			t.Errorf("%s renders a git workload without the repository list: a project "+
				"group would fall back to the single-repo layout", f)
		}
	}
}

func readControllerSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
