package webui

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func node(name string, labels map[string]string, taints ...corev1.Taint) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec:       corev1.NodeSpec{Taints: taints},
	}
}

// A dedicated pool is usually tainted so nothing lands on it by accident.
// Having deliberately chosen it, being kept out by its own taint is not a
// safety property — so the platform derives the tolerations rather than
// asking anyone to restate what the cluster already knows.
func TestTolerationsDerivedFromTheChosenPool(t *testing.T) {
	nodes := []*corev1.Node{
		node("gpu-1", nil, corev1.Taint{Key: "gpu", Value: "true", Effect: corev1.TaintEffectNoSchedule}),
		node("gpu-2", nil, corev1.Taint{Key: "gpu", Value: "true", Effect: corev1.TaintEffectNoSchedule}),
	}
	tol := tolerationsFor(nodes)
	if len(tol) != 1 {
		t.Fatalf("got %d tolerations, want 1: %+v", len(tol), tol)
	}
	if tol[0].Key != "gpu" || tol[0].Value != "true" || tol[0].Effect != corev1.TaintEffectNoSchedule {
		t.Errorf("unexpected toleration %+v", tol[0])
	}
}

// A taint on one machine of a pool is that machine's business — usually an
// operator draining it. Tolerating it cluster-wide would quietly undo the
// thing the operator was doing.
func TestTaintOnASingleNodeIsNotThePoolsTaint(t *testing.T) {
	nodes := []*corev1.Node{
		node("a", nil, corev1.Taint{Key: "pool", Value: "x", Effect: corev1.TaintEffectNoSchedule}),
		node("b", nil,
			corev1.Taint{Key: "pool", Value: "x", Effect: corev1.TaintEffectNoSchedule},
			corev1.Taint{Key: "node.kubernetes.io/unreachable", Effect: corev1.TaintEffectNoExecute},
		),
	}
	for _, tol := range tolerationsFor(nodes) {
		if tol.Key == "node.kubernetes.io/unreachable" {
			t.Error("tolerated a taint only one node carries")
		}
	}
}

// PreferNoSchedule does not stop anything, so tolerating it says nothing and
// would only add noise to the spec.
func TestSoftTaintsAreIgnored(t *testing.T) {
	nodes := []*corev1.Node{
		node("a", nil, corev1.Taint{Key: "soft", Effect: corev1.TaintEffectPreferNoSchedule}),
	}
	if len(tolerationsFor(nodes)) != 0 {
		t.Error("PreferNoSchedule should not produce a toleration")
	}
}

// A valueless taint needs Exists, not Equal to "": the two are not the same
// match, and the wrong one silently fails to tolerate.
func TestValuelessTaintUsesExists(t *testing.T) {
	nodes := []*corev1.Node{
		node("a", nil, corev1.Taint{Key: "dedicated", Effect: corev1.TaintEffectNoSchedule}),
	}
	tol := tolerationsFor(nodes)
	if len(tol) != 1 || tol[0].Operator != corev1.TolerationOpExists {
		t.Errorf("want Exists for a valueless taint, got %+v", tol)
	}
}

// The pool name should be the most meaningful label a node carries, and
// hostname is a legitimate last resort — on a one-machine cluster "this
// machine" is a real answer, not a failure to classify.
func TestPoolKeyPrefersTheMostMeaningfulLabel(t *testing.T) {
	cases := []struct {
		labels map[string]string
		want   string
	}{
		{map[string]string{"kubernetes.io/hostname": "n1"}, "kubernetes.io/hostname"},
		{map[string]string{
			"kubernetes.io/hostname":           "n1",
			"node.kubernetes.io/instance-type": "ecs.g7.large",
		}, "node.kubernetes.io/instance-type"},
		{map[string]string{
			"kubernetes.io/hostname":           "n1",
			"node.kubernetes.io/instance-type": "ecs.g7.large",
			"agentcell.io/pool":                "build",
		}, "agentcell.io/pool"},
		{nil, "kubernetes.io/hostname"},
	}
	for _, c := range cases {
		if got := poolKeyFor(node("n", c.labels)); got != c.want {
			t.Errorf("labels %v: got %q, want %q", c.labels, got, c.want)
		}
	}
}
