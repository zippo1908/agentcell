package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/identity"
)

func sharedSession(name, cell, owner string) *acv1.Session {
	s := &acv1.Session{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	s.Spec.Cell = cell
	s.Spec.OwnerUserID = owner
	s.Spec.Board = cell
	s.Spec.CredentialSecret = "owners-key"
	s.Status.Phase = acv1.SessionRunning
	return s
}

func openCell(name string) *acv1.Cell {
	c := &acv1.Cell{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	return c
}

// A shared session may be driven by any member — and driving it changes
// nothing about who pays for it.
//
// These are two different decisions and the product makes only one of them
// here: the keyboard is shared, the bill is not. An operator who silently
// became the sponsor would be spending somebody else's budget without ever
// agreeing to it; an owner swapped out mid-session would find their
// credential funding work they never saw.
func TestSharedSessionIsOperableWithoutChangingWhoPays(t *testing.T) {
	sess := sharedSession("sess-board", "shop", alice.ID())
	h := &Handler{
		Client:    fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(sess, openCell("shop")).Build(),
		Namespace: ns,
	}

	// Somebody who is not the owner may drive it.
	req := asUser(httptest.NewRequest(http.MethodGet, "/x", nil), bob)
	if !h.maySession(req, sess) {
		t.Error("a project member could not drive the project's shared session")
	}
	// And the record is untouched by the check.
	if sess.Spec.OwnerUserID != alice.ID() {
		t.Errorf("owner changed to %q just by being operated", sess.Spec.OwnerUserID)
	}
	if sess.Spec.CredentialSecret != "owners-key" {
		t.Errorf("credential changed to %q", sess.Spec.CredentialSecret)
	}
}

// A PERSONAL session stays personal: a project maintainer does not get
// somebody's private keyboard just by being a maintainer.
func TestPersonalSessionIsNotShared(t *testing.T) {
	sess := sharedSession("sess-mine", "shop", alice.ID())
	sess.Spec.Board = "" // not a board conversation
	h := &Handler{
		Client:    fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(sess, openCell("shop")).Build(),
		Namespace: ns,
	}
	req := asUser(httptest.NewRequest(http.MethodGet, "/x", nil), bob)
	if h.maySession(req, sess) {
		t.Error("somebody else's private session was drivable by a project member")
	}
}

// A follow-up must not rewrite the credential: the person who lent it did so
// for this conversation, and a later turn typed by somebody else is still
// this conversation.
func TestFollowUpKeepsTheOwnersCredential(t *testing.T) {
	sess := sharedSession("sess-board", "shop", alice.ID())
	h := &Handler{
		Client:    fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(sess, openCell("shop")).Build(),
		Namespace: ns,
	}
	if err := h.queueFollowUp(t.Context(), sess, "接着改一下"); err != nil {
		t.Fatal(err)
	}
	if sess.Spec.CredentialSecret != "owners-key" {
		t.Errorf("credential = %q after a follow-up, want the owner's", sess.Spec.CredentialSecret)
	}
	if sess.Spec.OwnerUserID != alice.ID() {
		t.Errorf("owner = %q after a follow-up, want unchanged", sess.Spec.OwnerUserID)
	}
}

// The owner of a board session is a REAL person, because something has to
// pay and only an account can. A synthetic "t-<team>" principal had no
// budget behind it and hid whoever actually asked.
func TestBoardOwnerIsARealAccount(t *testing.T) {
	sess := sharedSession("sess-board", "shop", alice.ID())
	if got := sess.Spec.OwnerUserID; got == "" || got[:2] == LegacyTeamOwnerPrefix {
		t.Errorf("owner = %q, want a real user id", got)
	}
	// The legacy form is still recognised so an upgrade does not strand the
	// conversation that was already running.
	legacy := sharedSession("sess-old", "shop", LegacyTeamOwnerPrefix+"platform")
	legacy.Spec.Board = ""
	h := &Handler{
		Client:    fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(legacy, openCell("shop")).Build(),
		Namespace: ns,
	}
	req := asUser(httptest.NewRequest(http.MethodGet, "/x", nil), bob)
	if !h.maySession(req, legacy) {
		t.Error("a board session from before the change became undrivable")
	}
}

var _ = identity.Principal{}
