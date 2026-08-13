package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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
	if !can(alice, cell, nil, ActionRelease) {
		t.Error("a maintainer cannot release")
	}
	if can(bob, cell, nil, ActionRelease) {
		t.Error("a member can release; that is the one action that must not be open")
	}
	// A member can still do the work.
	if !can(bob, cell, nil, ActionDispatch) || !can(bob, cell, nil, ActionReview) {
		t.Error("a member cannot dispatch or review")
	}
}

func TestRolesAreOrdered(t *testing.T) {
	viewer := cellWithMembers(acv1.Member{UserID: alice.ID(), Role: acv1.RoleViewer})
	for _, a := range []Action{ActionDispatch, ActionReview, ActionRelease, ActionSettings} {
		if can(alice, viewer, nil, a) {
			t.Errorf("a viewer can %s", a)
		}
	}
	if !can(alice, viewer, nil, ActionView) {
		t.Error("a viewer cannot view")
	}
	// An unknown role must rank BELOW viewer, not accidentally above it.
	weird := cellWithMembers(acv1.Member{UserID: alice.ID(), Role: acv1.Role("superuser")})
	if can(alice, weird, nil, ActionView) {
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
			if !can(p, open, nil, a) {
				t.Errorf("%s cannot %s on a Cell with no members", p.Display(), a)
			}
		}
	}
}

// With static tokens there is one principal; making it a viewer would lock
// the single-user story out of its own console.
func TestStaticTokenIsMaintainerEverywhere(t *testing.T) {
	cell := cellWithMembers(acv1.Member{UserID: alice.ID(), Role: acv1.RoleViewer})
	if !can(identity.StaticToken, cell, nil, ActionRelease) {
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

// The read paths, as a matrix. Write authorization was closed first, which
// made it easy to believe the Cell was closed — it was not: an outsider
// could list every project, read a settled session's diff, and be handed a
// preview ticket for a Cell they have no role in.
func TestOutsidersSeeNothing(t *testing.T) {
	scheme := testScheme(t)
	cell := &acv1.Cell{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "shop"}}
	cell.Spec.Members = []acv1.Member{{UserID: bob.ID(), Role: acv1.RoleMaintainer}}
	cell.Status.PreviewPath = "/preview/shop/"
	cell.Status.ProductionPath = "/app/shop/"

	sess := &acv1.Session{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "sess-b"}}
	sess.Spec = acv1.SessionSpec{Cell: "shop", Task: "bob's work", OwnerUserID: bob.ID()}
	sess.Status.Phase, sess.Status.Produced = acv1.SessionSettled, true
	sess.Status.SessionID, sess.Status.Branch = "b", "session/b"

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cell, sess, &corev1.Namespace{}).
		WithStatusSubresource(&acv1.Session{}).Build()
	h := &Handler{Client: c, Namespace: ns, Auth: NewAuthenticator("t")}

	get := func(path string, fn http.HandlerFunc, p identity.Principal, vals map[string]string) *httptest.ResponseRecorder {
		req := asUser(httptest.NewRequest(http.MethodGet, path, nil), p)
		for k, v := range vals {
			req.SetPathValue(k, v)
		}
		rec := httptest.NewRecorder()
		fn(rec, req)
		return rec
	}

	t.Run("an outsider's cell list is empty and mints no ticket", func(t *testing.T) {
		rec := get("/api/cells", h.listCells, alice, nil)
		body := rec.Body.String()
		if strings.Contains(body, "shop") {
			t.Errorf("the Cell name leaked: %s", body)
		}
		// A ticket is a capability, not a label.
		if strings.Contains(body, previewTicketQS+"=") {
			t.Errorf("a preview ticket was minted for a Cell the caller cannot see: %s", body)
		}
	})

	t.Run("a member's list contains it", func(t *testing.T) {
		if !strings.Contains(get("/api/cells", h.listCells, bob, nil).Body.String(), "shop") {
			t.Error("a maintainer cannot see their own Cell")
		}
	})

	t.Run("getCell is 404 for an outsider", func(t *testing.T) {
		rec := get("/api/cells/shop", h.getCell, alice, map[string]string{"cell": "shop"})
		if rec.Code != http.StatusNotFound {
			t.Errorf("= %d, want 404", rec.Code)
		}
	})

	t.Run("the review queue is filtered", func(t *testing.T) {
		out := get("/api/reviews", h.listReviews, alice, nil).Body.String()
		if strings.Contains(out, "bob's work") || strings.Contains(out, "session/b") {
			t.Errorf("another Cell's review queue leaked: %s", out)
		}
		if !strings.Contains(get("/api/reviews", h.listReviews, bob, nil).Body.String(), "session/b") {
			t.Error("a maintainer cannot see their own queue")
		}
	})

	t.Run("a diff needs a role on the Cell, not a session name", func(t *testing.T) {
		rec := get("/api/sessions/sess-b/diff", h.sessionDiff, alice, map[string]string{"session": "sess-b"})
		if rec.Code != http.StatusNotFound {
			t.Errorf("diff = %d for an outsider, want 404: %s", rec.Code, rec.Body)
		}
	})

	t.Run("a viewer reads but cannot act", func(t *testing.T) {
		var fresh acv1.Cell
		_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "shop"}, &fresh)
		fresh.Spec.Members = append(fresh.Spec.Members,
			acv1.Member{UserID: alice.ID(), Role: acv1.RoleViewer})
		_ = c.Update(context.Background(), &fresh)

		if get("/api/cells/shop", h.getCell, alice, map[string]string{"cell": "shop"}).Code != 200 {
			t.Error("a viewer cannot read the Cell they were added to")
		}
		req := asUser(httptest.NewRequest(http.MethodPost, "/api/cells/shop/release", strings.NewReader("{}")), alice)
		req.SetPathValue("cell", "shop")
		rec := httptest.NewRecorder()
		h.release(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("a viewer releasing = %d, want 403", rec.Code)
		}
	})
}

// testScheme keeps the fixtures above readable.
func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := acv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// "The maintainer manages members" was documented and not implemented —
// true only if you counted editing the CR with kubectl, which a maintainer
// of a project does not necessarily have.
func TestMemberManagement(t *testing.T) {
	scheme := testScheme(t)
	cell := &acv1.Cell{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "shop"}}
	cell.Spec.Members = []acv1.Member{{UserID: alice.ID(), Role: acv1.RoleMaintainer}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cell).Build()
	h := &Handler{Client: c, Namespace: ns}

	put := func(p identity.Principal, body string) *httptest.ResponseRecorder {
		req := asUser(httptest.NewRequest(http.MethodPut, "/api/cells/shop/members", strings.NewReader(body)), p)
		req.SetPathValue("cell", "shop")
		rec := httptest.NewRecorder()
		h.putMember(rec, req)
		return rec
	}

	if rec := put(bob, `{"userID":"`+bob.ID()+`","role":"maintainer"}`); rec.Code != http.StatusNotFound {
		t.Errorf("an outsider added themselves: %d", rec.Code)
	}
	if rec := put(alice, `{"userID":"`+bob.ID()+`","role":"member"}`); rec.Code != 200 {
		t.Fatalf("a maintainer could not add a member: %d %s", rec.Code, rec.Body)
	}
	if rec := put(alice, `{"userID":"x","role":"admin"}`); rec.Code != 400 {
		t.Errorf("an unknown role was accepted: %d", rec.Code)
	}

	// Removing the last maintainer would leave a restricted Cell nobody can
	// administer, recoverable only with cluster access — which is exactly
	// what this API exists to avoid needing.
	del := func(user string) *httptest.ResponseRecorder {
		req := asUser(httptest.NewRequest(http.MethodDelete, "/x", nil), alice)
		req.SetPathValue("cell", "shop")
		req.SetPathValue("user", user)
		rec := httptest.NewRecorder()
		h.deleteMember(rec, req)
		return rec
	}
	if rec := del(alice.ID()); rec.Code != 400 {
		t.Errorf("the last maintainer removed themselves: %d %s", rec.Code, rec.Body)
	}
	if rec := del(bob.ID()); rec.Code != 200 {
		t.Errorf("removing a member failed: %d %s", rec.Code, rec.Body)
	}
}

// Adding the first member closes an open Cell: naming somebody is an
// unambiguous statement that this project has an inside and an outside.
func TestAddingAMemberClosesAnOpenCell(t *testing.T) {
	scheme := testScheme(t)
	cell := &acv1.Cell{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "shop"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cell).Build()
	h := &Handler{Client: c, Namespace: ns}
	req := asUser(httptest.NewRequest(http.MethodPut, "/x",
		strings.NewReader(`{"userID":"`+alice.ID()+`","role":"maintainer"}`)), alice)
	req.SetPathValue("cell", "shop")
	rec := httptest.NewRecorder()
	h.putMember(rec, req)
	if rec.Code != 200 {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var got acv1.Cell
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "shop"}, &got)
	if effectiveAccess(&got) != acv1.AccessRestricted {
		t.Error("the Cell stayed open with a member list nobody enforces")
	}
	if can(bob, &got, nil, ActionView) {
		t.Error("an outsider can still see a Cell that now has members")
	}
}
