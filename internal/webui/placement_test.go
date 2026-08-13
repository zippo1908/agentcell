package webui

import (
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// The hole this file guards.
//
// Placement first read the cluster's node labels, let a Cell MAINTAINER pick
// any of them, and derived the tolerations needed to land there — taints
// included. "Maintainer of a project" is not "administrator of the cluster";
// a maintainer could have named the control-plane label and the platform
// would have tolerated the taint that keeps workloads off it, for a pod
// running a model's output against a repository.
//
// A taint is an administrator's refusal. Nothing here may read one and write
// the matching toleration.

// There must be no code path that turns observed taints into tolerations.
// This is checked against the source because the guarantee is "the platform
// never does this", not "this particular function does not".
func TestNothingDerivesTolerationsFromTaints(t *testing.T) {
	src := readSource(t, "placement.go")
	for _, banned := range []string{"Spec.Taints", "TaintEffectNoExecute", "TaintEffectNoSchedule"} {
		if strings.Contains(src, banned) {
			t.Errorf("placement.go reads %s — a taint is the cluster administrator's "+
				"refusal, and reading it to write a toleration is a bypass, not a feature", banned)
		}
	}
	if strings.Contains(src, "Toleration{") {
		t.Error("placement.go constructs a Toleration; only an administrator authoring a " +
			"PlacementClass may do that")
	}
}

// A maintainer must not be able to express a placement that was never
// offered: the input carries a class name and nothing else, so there is no
// field through which a selector or a toleration could arrive.
func TestPlacementInputAcceptsOnlyAClassName(t *testing.T) {
	src := readSource(t, "placement.go")
	start := strings.Index(src, "type placementInput struct {")
	if start < 0 {
		t.Fatal("placementInput is gone; this test needs updating")
	}
	end := strings.Index(src[start:], "}")
	body := src[start : start+end]
	for _, banned := range []string{"NodeSelector", "Tolerations", "Key", "Value"} {
		if strings.Contains(body, banned) {
			t.Errorf("placementInput carries %s: a maintainer could then name a pool "+
				"nobody offered", banned)
		}
	}
}

func TestSelectorMatchesOnlyOnEveryLabel(t *testing.T) {
	n := &corev1.Node{}
	n.Labels = map[string]string{"pool": "build", "zone": "a"}
	if !matchesSelector(n, map[string]string{"pool": "build"}) {
		t.Error("a matching label did not match")
	}
	if matchesSelector(n, map[string]string{"pool": "build", "zone": "b"}) {
		t.Error("a selector matched although one label differs")
	}
	// An empty selector must not match everything: a class that selects
	// nothing would otherwise be reported as covering the whole cluster.
	if matchesSelector(n, nil) {
		t.Error("an empty selector matched a node")
	}
}

func TestSelectorTextIsStable(t *testing.T) {
	got := selectorText(map[string]string{"zone": "a", "pool": "build"})
	if got != "pool=build,zone=a" {
		t.Errorf("selectorText = %q, want a sorted, stable rendering", got)
	}
}

// readSource reads a file in this package, so a guarantee about what the
// code must never do can be checked against the code rather than against one
// function that could be replaced by another.
func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
