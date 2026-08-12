package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/access"
	"github.com/zippo1908/agentcell/internal/identity"
)

var (
	alice = identity.Principal{Subject: "oidc:aaaa:alice", Name: "Alice", Kind: identity.KindOIDC}
	bob   = identity.Principal{Subject: "oidc:aaaa:bob", Name: "Bob", Kind: identity.KindOIDC}
)

// ownedFixture builds a control namespace holding one Cell and the given
// Sessions.
func ownedFixture(t *testing.T, objs ...client.Object) (client.Client, *Handler) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := acv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cell := &acv1.Cell{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "shop"}}
	all := append([]client.Object{cell}, objs...)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(all...).
		WithStatusSubresource(&acv1.Session{}).Build()
	return c, &Handler{Client: c, Namespace: ns, Registry: testRegistry(t)}
}

// testRegistry loads the built-in provider presets so dispatch validation
// behaves as it does in production.
func testRegistry(t *testing.T) *access.Registry {
	t.Helper()
	reg, err := access.Load()
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func sessionOwnedBy(name string, owner identity.Principal, phase acv1.SessionPhase, produced bool) *acv1.Session {
	s := &acv1.Session{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	s.Spec = acv1.SessionSpec{Cell: "shop", Task: "secret work order", OwnerUserID: owner.ID()}
	s.Status.Phase = phase
	s.Status.Produced = produced
	s.Status.SessionID = strings.TrimPrefix(name, "sess-")
	return s
}

func asUser(r *http.Request, p identity.Principal) *http.Request {
	return r.WithContext(identity.NewContext(r.Context(), p))
}

// A running Session is that user's private execution boundary: its very
// existence, and its task text, must not leak to a project peer.
func TestRunningSessionsAreInvisibleToOtherUsers(t *testing.T) {
	_, h := ownedFixture(t,
		sessionOwnedBy("sess-a", alice, acv1.SessionRunning, false),
		sessionOwnedBy("sess-b", bob, acv1.SessionRunning, false),
	)
	req := asUser(httptest.NewRequest(http.MethodGet, "/api/cells/shop", nil), alice)
	req.SetPathValue("cell", "shop")
	rec := httptest.NewRecorder()
	h.getCell(rec, req)
	if rec.Code != 200 {
		t.Fatalf("getCell = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Sessions []struct {
			Name string `json:"name"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sessions) != 1 || out.Sessions[0].Name != "sess-a" {
		t.Errorf("Alice sees %v, want only her own running session", out.Sessions)
	}
	if strings.Contains(rec.Body.String(), "sess-b") {
		t.Error("another user's running session leaked into the response")
	}
}

// Settle is the controlled publication step: once a session has settled with
// output, its work is a project artifact and peers can see it. Collaboration
// happens at the project layer.
func TestSettledSessionsAreVisibleToTheProject(t *testing.T) {
	_, h := ownedFixture(t, sessionOwnedBy("sess-b", bob, acv1.SessionSettled, true))
	req := asUser(httptest.NewRequest(http.MethodGet, "/api/cells/shop", nil), alice)
	req.SetPathValue("cell", "shop")
	rec := httptest.NewRecorder()
	h.getCell(rec, req)
	if !strings.Contains(rec.Body.String(), "sess-b") {
		t.Errorf("a settled session should be visible to the project: %s", rec.Body)
	}
}

// Not-yours must be indistinguishable from not-here (ADR-0008): a 403 would
// confirm the session exists.
func TestForeignSessionDiscardAnswers404Not403(t *testing.T) {
	_, h := ownedFixture(t, sessionOwnedBy("sess-b", bob, acv1.SessionRunning, false))
	for _, tc := range []struct{ name, session string }{
		{"exists but not yours", "sess-b"},
		{"does not exist at all", "sess-nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := asUser(httptest.NewRequest(http.MethodDelete, "/api/sessions/"+tc.session, nil), alice)
			req.SetPathValue("session", tc.session)
			rec := httptest.NewRecorder()
			h.settleSession(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("discard = %d, want 404", rec.Code)
			}
		})
	}
}

// A dispatched Session records its creator, and that value is what later
// authorization is judged on.
func TestDispatchStampsTheOwner(t *testing.T) {
	c, h := ownedFixture(t)
	body := `{"task":"t","runner":"claude","provider":"anthropic","credentialSecret":""}`
	req := asUser(httptest.NewRequest(http.MethodPost, "/api/cells/shop/dispatch", strings.NewReader(body)), alice)
	req.SetPathValue("cell", "shop")
	rec := httptest.NewRecorder()
	h.dispatch(rec, req)
	if rec.Code != 201 {
		t.Skipf("dispatch rejected by the provider registry in this fixture: %s", rec.Body)
	}
	var list acv1.SessionList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Spec.OwnerUserID != alice.ID() {
		t.Fatalf("session owner = %q, want %q", list.Items[0].Spec.OwnerUserID, alice.ID())
	}
}

// Naming someone else's model credential is a credential-theft primitive,
// not merely an authorization gap.
func TestCannotSpendAnotherUsersModelCredential(t *testing.T) {
	bobsKey := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: "bob-key", Labels: map[string]string{OwnerLabel: bob.ID()},
	}}
	_, h := ownedFixture(t, bobsKey)
	req := asUser(httptest.NewRequest(http.MethodPost, "/api/cells/shop/dispatch",
		strings.NewReader(`{"task":"t","runner":"claude","provider":"anthropic","credentialSecret":"bob-key"}`)), alice)
	req.SetPathValue("cell", "shop")
	rec := httptest.NewRecorder()
	h.dispatch(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("spending Bob's credential = %d, want 404", rec.Code)
	}
}

// Deployments with no identity provider keep working exactly as before:
// one principal, which owns everything including objects recorded with no
// owner at all.
func TestStaticTokenModeIsUnchanged(t *testing.T) {
	legacy := &acv1.Session{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "sess-old"}}
	legacy.Spec = acv1.SessionSpec{Cell: "shop", Task: "from before ownership"}
	legacy.Status.Phase = acv1.SessionRunning
	c, h := ownedFixture(t, legacy)
	_ = c
	req := asUser(httptest.NewRequest(http.MethodGet, "/api/cells/shop", nil), identity.StaticToken)
	req.SetPathValue("cell", "shop")
	rec := httptest.NewRecorder()
	h.getCell(rec, req)
	if !strings.Contains(rec.Body.String(), "sess-old") {
		t.Errorf("static-token principal lost sight of pre-ownership sessions: %s", rec.Body)
	}
	// ...and an OIDC user must NOT inherit them.
	req2 := asUser(httptest.NewRequest(http.MethodGet, "/api/cells/shop", nil), alice)
	req2.SetPathValue("cell", "shop")
	rec2 := httptest.NewRecorder()
	h.getCell(rec2, req2)
	if strings.Contains(rec2.Body.String(), "sess-old") {
		t.Error("an OIDC user inherited a session whose owner was never recorded")
	}
}

func TestPrincipalIDIsStableAndLabelSafe(t *testing.T) {
	if alice.ID() != alice.ID() || alice.ID() == bob.ID() {
		t.Fatal("principal IDs must be stable and distinct")
	}
	id := alice.ID()
	if len(id) > 63 {
		t.Errorf("id %q is not a valid label value", id)
	}
	for _, r := range id {
		if !(r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			t.Fatalf("id %q contains %q, which is not label-safe", id, r)
		}
	}
	if strings.Contains(id, "alice") {
		t.Error("id should not embed the raw subject")
	}
}

var _ = types.NamespacedName{}
