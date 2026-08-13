package crdcheck

import (
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"
)

// The CRDs are the schema the API server stores objects against. A field
// that exists in Go but not here is accepted by a client, dropped by the
// apiserver, and read back missing — which is exactly how previewResources
// and the member list shipped with nowhere to live.
//
// This walks the committed schema rather than the Go types on purpose: the
// question is not "did we write the struct" but "will the cluster keep it".

func loadCRD(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", name))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// props walks spec.versions[0].schema...properties along a path.
func props(t *testing.T, doc map[string]any, path ...string) map[string]any {
	t.Helper()
	spec := doc["spec"].(map[string]any)
	versions := spec["versions"].([]any)
	schema := versions[0].(map[string]any)["schema"].(map[string]any)
	node := schema["openAPIV3Schema"].(map[string]any)
	for _, p := range path {
		ps, ok := node["properties"].(map[string]any)
		if !ok {
			t.Fatalf("no properties at %v", path)
		}
		next, ok := ps[p].(map[string]any)
		if !ok {
			t.Fatalf("field %q is missing from the schema (path %v) — the apiserver would drop it", p, path)
		}
		node = next
	}
	return node
}

func TestEveryCellFieldHasSomewhereToLive(t *testing.T) {
	doc := loadCRD(t, "agentcell.io_cells.yaml")
	for _, f := range []string{
		"repo", "image", "description", "maxSessions", "sessionResources",
		"previewResources", "workspaceSize", "preview", "production",
		"access", "members",
	} {
		props(t, doc, "spec", f)
	}
	for _, f := range []string{"phase", "previewPath", "productionPath", "access", "handedOffRelease"} {
		props(t, doc, "status", f)
	}
	// Members must be a list of objects with both fields, or a member is
	// stored as an empty entry.
	m := props(t, doc, "spec", "members")
	items, ok := m["items"].(map[string]any)
	if !ok {
		t.Fatal("members has no item schema")
	}
	ip, ok := items["properties"].(map[string]any)
	if !ok || ip["userID"] == nil || ip["role"] == nil {
		t.Errorf("a member would lose its fields: %v", items)
	}
}

func TestEverySessionFieldHasSomewhereToLive(t *testing.T) {
	doc := loadCRD(t, "agentcell.io_sessions.yaml")
	for _, f := range []string{
		"cell", "task", "runner", "provider", "model", "credentialSecret",
		"ttlSeconds", "followPreview", "resident", "ownerUserID",
	} {
		props(t, doc, "spec", f)
	}
	for _, f := range []string{
		"phase", "sessionID", "podName", "branch", "reviewState",
		"runnerSessionID", "recoveries", "runtimeInstance", "lastActivity",
	} {
		props(t, doc, "status", f)
	}
}

// ADR-0008 rests on this rule holding for kubectl edits, not just for writes
// through the API. It lives in the schema, so it is the schema that must be
// checked — a marker in Go that failed to render would look fine in review.
func TestOwnerIsImmutableInTheSchema(t *testing.T) {
	doc := loadCRD(t, "agentcell.io_sessions.yaml")
	owner := props(t, doc, "spec", "ownerUserID")
	rules, ok := owner["x-kubernetes-validations"].([]any)
	if !ok || len(rules) == 0 {
		t.Fatal("ownerUserID has no validation: it could be reassigned with kubectl edit")
	}
	rule := rules[0].(map[string]any)["rule"].(string)
	if rule != "oldSelf == '' || self == oldSelf" {
		t.Errorf("immutability rule is %q", rule)
	}
}

// Both copies are outputs of one generator. They had already drifted once.
func TestTheTwoCRDCopiesAreIdentical(t *testing.T) {
	for _, name := range []string{"agentcell.io_cells.yaml", "agentcell.io_sessions.yaml"} {
		a, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", name))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join("..", "..", "deploy", "charts", "agentcell", "crds", name))
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Errorf("%s differs between config/crd and the chart; a chart install would use a different schema", name)
		}
	}
}
