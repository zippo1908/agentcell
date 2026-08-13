package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/identity"
)

func cellWithMembers(ms ...acv1.Member) *acv1.Cell {
	c := &acv1.Cell{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "shop"}}
	c.Spec.Members = ms
	return c
}

// Release is the line that matters: everything else is recoverable, and a
// release is the one action that puts code in front of users. Before roles,
// ANY authenticated user could ship any Cell.
func TestReleaseRequiresMaintainer(t *testing.T) {
	cell := cellWithMembers(
		acv1.Member{UserID: alice.ID(), Role: acv1.RoleMaintainer},
		acv1.Member{UserID: bob.ID(), Role: acv1.RoleMember},
	)
	if !can(alice, cell, ActionRelease) {
		t.Error("a maintainer cannot release")
	}
	if can(bob, cell, ActionRelease) {
		t.Error("a member can release; that is the one action that must not be open")
	}
	// A member can still do the work.
	if !can(bob, cell, ActionDispatch) || !can(bob, cell, ActionReview) {
		t.Error("a member cannot dispatch or review")
	}
}

func TestRolesAreOrdered(t *testing.T) {
	viewer := cellWithMembers(acv1.Member{UserID: alice.ID(), Role: acv1.RoleViewer})
	for _, a := range []Action{ActionDispatch, ActionReview, ActionRelease, ActionSettings} {
		if can(alice, viewer, a) {
			t.Errorf("a viewer can %s", a)
		}
	}
	if !can(alice, viewer, ActionView) {
		t.Error("a viewer cannot view")
	}
	// An unknown role must rank BELOW viewer, not accidentally above it.
	weird := cellWithMembers(acv1.Member{UserID: alice.ID(), Role: acv1.Role("superuser")})
	if can(alice, weird, ActionView) {
		t.Error("an unrecognised role granted access; unknown must fail closed")
	}
}

// An upgrade that locked people out of their own projects would be reverted
// before it was understood. A Cell with no members behaves exactly as every
// Cell did before roles existed.
func TestACellWithoutMembersIsUnchanged(t *testing.T) {
	open := cellWithMembers()
	for _, p := range []identity.Principal{alice, bob, identity.StaticToken} {
		for _, a := range []Action{ActionView, ActionDispatch, ActionReview, ActionRelease, ActionSettings} {
			if !can(p, open, a) {
				t.Errorf("%s cannot %s on a Cell with no members", p.Display(), a)
			}
		}
	}
}

// With static tokens there is one principal; making it a viewer would lock
// the single-user story out of its own console.
func TestStaticTokenIsMaintainerEverywhere(t *testing.T) {
	cell := cellWithMembers(acv1.Member{UserID: alice.ID(), Role: acv1.RoleViewer})
	if !can(identity.StaticToken, cell, ActionRelease) {
		t.Error("the static-token principal lost control of a Cell")
	}
}

// A refusal must not confirm what it is refusing. Outside the Cell: 404.
// Inside it but under-privileged: 403 naming the role, so the reader can ask
// the right person instead of guessing.
func TestRefusalsDoNotLeakExistence(t *testing.T) {
	cell := cellWithMembers(acv1.Member{UserID: bob.ID(), Role: acv1.RoleMaintainer})
	h := &Handler{Namespace: ns}

	req := asUser(httptest.NewRequest(http.MethodPost, "/x", nil), alice) // not a member
	rec := httptest.NewRecorder()
	if h.authorize(rec, req, cell, ActionRelease) {
		t.Fatal("a non-member was authorised")
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("outsider got %d; 403 would confirm the cell exists", rec.Code)
	}

	viewer := cellWithMembers(acv1.Member{UserID: alice.ID(), Role: acv1.RoleViewer})
	rec2 := httptest.NewRecorder()
	if h.authorize(rec2, asUser(httptest.NewRequest(http.MethodPost, "/x", nil), alice), viewer, ActionRelease) {
		t.Fatal("a viewer was authorised to release")
	}
	if rec2.Code != http.StatusForbidden {
		t.Errorf("viewer got %d, want 403", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), string(acv1.RoleMaintainer)) {
		t.Errorf("the refusal does not say which role is needed: %s", rec2.Body)
	}
}
