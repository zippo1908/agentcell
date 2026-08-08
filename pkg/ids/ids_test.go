package ids

import (
	"strings"
	"testing"
)

func TestValidateCellName(t *testing.T) {
	for _, ok := range []string{"shop", "my-app-2", "a"} {
		if err := ValidateCellName(ok); err != nil {
			t.Errorf("ValidateCellName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "-lead", "trail-", "Has_Upper", "Ünïcode", strings.Repeat("a", MaxCellName+1)} {
		if err := ValidateCellName(bad); err == nil {
			t.Errorf("ValidateCellName(%q) = nil, want error", bad)
		}
	}
}

func TestSessionDerivations(t *testing.T) {
	id := NewSessionID()
	if id != strings.ToLower(id) {
		t.Fatalf("session id %q not lowercase", id)
	}
	if got := SessionName(id); got != "sess-"+id {
		t.Errorf("SessionName = %q", got)
	}
	if got := SessionBranch(id); got != "session/"+id {
		t.Errorf("SessionBranch = %q", got)
	}
	if got := WorktreePath(id); got != "/workspace/.cells/"+id {
		t.Errorf("WorktreePath = %q", got)
	}
	if len(SessionName(id)) > 63 {
		t.Errorf("session pod name %q exceeds DNS label limit", SessionName(id))
	}
	if NewSessionID() == NewSessionID() {
		t.Error("two session ids collided")
	}
}

func TestWorkloadNamespaceFitsDNS(t *testing.T) {
	long := strings.Repeat("a", MaxCellName)
	if ns := WorkloadNamespace(long); len(ns) > 63 {
		t.Errorf("namespace %q exceeds 63 chars", ns)
	}
}
