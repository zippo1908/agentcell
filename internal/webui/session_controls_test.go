package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
)

func ownedSession(name string) *acv1.Session {
	s := &acv1.Session{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	s.Spec.Cell = "duo"
	s.Spec.OwnerUserID = alice.ID()
	s.Spec.Resident = new(bool)
	*s.Spec.Resident = true
	s.Status.PodName = "runtime-100002"
	return s
}

// Restarting a runtime must announce itself before the runtime disappears.
//
// The controller budgets recoveries so a runtime that keeps dying settles
// instead of flapping forever. A restart a person asked for is not a crash,
// and if it draws on that budget the third press of 重启 ends their work —
// a button doing the opposite of what it says. Marking BEFORE the delete
// also means a crash in between costs only an unspent free recovery.
func TestRestartMarksTheSessionBeforeTearingItDown(t *testing.T) {
	sess := ownedSession("sess-1")
	h := &Handler{
		Client:    crfake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(sess).Build(),
		Namespace: ns,
		Kube: fake.NewSimpleClientset(&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "cell-duo", Name: "runtime-100002"},
		}),
	}

	req := asUser(httptest.NewRequest(http.MethodPost, "/api/sessions/sess-1/restart", nil), alice)
	req.SetPathValue("session", "sess-1")
	rec := httptest.NewRecorder()
	h.restartRuntime(rec, req)
	if rec.Code != 200 {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}

	var got acv1.Session
	if err := h.Client.Get(t.Context(),
		types.NamespacedName{Namespace: ns, Name: "sess-1"}, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Annotations[acv1.RestartRequestedAnnotation]; !ok {
		t.Error("the runtime was torn down without recording that a person asked for it")
	}

	// And the runtime really is gone, so the controller rebuilds it.
	if _, err := h.Kube.CoreV1().Pods("cell-duo").
		Get(t.Context(), "runtime-100002", metav1.GetOptions{}); err == nil {
		t.Error("restart left the old runtime running")
	}
}

// Stopping is not ending: it must park the session, not settle it.
func TestSleepParksRatherThanSettles(t *testing.T) {
	sess := ownedSession("sess-2")
	h := &Handler{
		Client:    crfake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(sess).Build(),
		Namespace: ns,
	}
	req := asUser(httptest.NewRequest(http.MethodPost, "/api/sessions/sess-2/sleep", nil), alice)
	req.SetPathValue("session", "sess-2")
	rec := httptest.NewRecorder()
	h.sleepSession(rec, req)
	if rec.Code != 200 {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}

	var got acv1.Session
	if err := h.Client.Get(t.Context(),
		types.NamespacedName{Namespace: ns, Name: "sess-2"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.DesiredState != acv1.SessionDesiredDormant {
		t.Errorf("desiredState = %q, want dormant", got.Spec.DesiredState)
	}
	if !got.DeletionTimestamp.IsZero() {
		t.Error("stopping deleted the session")
	}
}

// Somebody else's session is not yours to stop or restart, and refusing
// says 404 rather than 403 so it does not confirm the session exists.
func TestControlsRefuseSomebodyElsesSession(t *testing.T) {
	sess := ownedSession("sess-3")
	sess.Spec.OwnerUserID = "u-somebody-else"
	h := &Handler{
		Client:    crfake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(sess).Build(),
		Namespace: ns,
		Kube:      fake.NewSimpleClientset(),
	}
	for _, tc := range []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
	}{{"sleep", h.sleepSession}, {"restart", h.restartRuntime}} {
		req := asUser(httptest.NewRequest(http.MethodPost, "/x", nil), alice)
		req.SetPathValue("session", "sess-3")
		rec := httptest.NewRecorder()
		tc.call(rec, req)
		if rec.Code != 404 {
			t.Errorf("%s = %d, want 404", tc.name, rec.Code)
		}
	}
	var got acv1.Session
	_ = h.Client.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: "sess-3"}, &got)
	if got.Spec.DesiredState == acv1.SessionDesiredDormant {
		t.Error("a stranger stopped somebody else's session")
	}
}
