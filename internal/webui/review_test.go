package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
)

const ns = "agentcell-system"

func reviewFixture(t *testing.T, mutate func(*acv1.Session)) (client.Client, *Handler) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := acv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	sess := &acv1.Session{}
	sess.Name, sess.Namespace = "sess-abc", ns
	sess.Spec = acv1.SessionSpec{Cell: "shop", Task: "t"}
	sess.Status.Phase = acv1.SessionSettled
	sess.Status.Produced = true
	sess.Status.SessionID = "abc"
	sess.Status.Branch = "session/abc"
	sess.Status.ReviewState = acv1.ReviewPending
	if mutate != nil {
		mutate(sess)
	}
	// Reviewing is governed by the Cell (ADR-0013), so the fixture needs one.
	cell := &acv1.Cell{}
	cell.Name, cell.Namespace = "shop", ns
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(sess, cell, &corev1.Namespace{}).
		WithStatusSubresource(&acv1.Session{}).Build()
	return c, &Handler{Client: c, Namespace: ns}
}

func postReview(h *Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-abc/review", strings.NewReader(body))
	req.SetPathValue("session", "sess-abc")
	rec := httptest.NewRecorder()
	h.reviewSession(rec, req)
	return rec
}

func TestReviewTransitions(t *testing.T) {
	t.Run("pending → approved", func(t *testing.T) {
		c, h := reviewFixture(t, nil)
		if rec := postReview(h, `{"decision":"approve"}`); rec.Code != 200 {
			t.Fatalf("approve = %d: %s", rec.Code, rec.Body)
		}
		var s acv1.Session
		_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "sess-abc"}, &s)
		if s.Status.ReviewState != acv1.ReviewApproved {
			t.Errorf("state = %q", s.Status.ReviewState)
		}
	})

	t.Run("pending → rejected with a reason", func(t *testing.T) {
		c, h := reviewFixture(t, nil)
		if rec := postReview(h, `{"decision":"reject","note":"wrong approach"}`); rec.Code != 200 {
			t.Fatalf("reject = %d: %s", rec.Code, rec.Body)
		}
		var s acv1.Session
		_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "sess-abc"}, &s)
		if s.Status.ReviewState != acv1.ReviewRejected || s.Status.ReviewNote != "wrong approach" {
			t.Errorf("state=%q note=%q", s.Status.ReviewState, s.Status.ReviewNote)
		}
	})

	t.Run("rejection without a reason is refused", func(t *testing.T) {
		_, h := reviewFixture(t, nil)
		for _, body := range []string{`{"decision":"reject"}`, `{"decision":"reject","note":"   "}`} {
			if rec := postReview(h, body); rec.Code != 400 {
				t.Errorf("reject %s = %d, want 400", body, rec.Code)
			}
		}
	})

	t.Run("an already-decided session cannot be re-reviewed", func(t *testing.T) {
		for _, state := range []acv1.ReviewState{acv1.ReviewApproved, acv1.ReviewRejected} {
			_, h := reviewFixture(t, func(s *acv1.Session) { s.Status.ReviewState = state })
			rec := postReview(h, `{"decision":"approve"}`)
			if rec.Code != http.StatusConflict {
				t.Errorf("re-review of %s = %d, want 409", state, rec.Code)
			}
		}
	})

	t.Run("a session with a PR cannot be reversed", func(t *testing.T) {
		_, h := reviewFixture(t, func(s *acv1.Session) {
			s.Status.ReviewState = acv1.ReviewPending
			s.Status.PRNumber = 7
		})
		if rec := postReview(h, `{"decision":"reject","note":"changed my mind"}`); rec.Code != http.StatusConflict {
			t.Errorf("reject with an open PR = %d, want 409", rec.Code)
		}
	})

	t.Run("a session without output is not reviewable", func(t *testing.T) {
		_, h := reviewFixture(t, func(s *acv1.Session) {
			s.Status.Produced = false
			s.Status.Phase = acv1.SessionDiscarded
		})
		if rec := postReview(h, `{"decision":"approve"}`); rec.Code != 400 {
			t.Errorf("review of a discarded session = %d, want 400", rec.Code)
		}
	})
}
