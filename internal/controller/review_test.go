package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/pkg/ids"
)

// settledSession builds a session already in the terminal, produced state.
func settledSession(t *testing.T, name, id string) *acv1.Session {
	t.Helper()
	s := newSession(name, "add the cart page")
	return s
}

func markSettled(t *testing.T, r *SessionReconciler, name, id string) {
	t.Helper()
	ctx := context.Background()
	var s acv1.Session
	if err := r.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	s.Status.SessionID = id
	s.Status.Phase = acv1.SessionSettled
	s.Status.Produced = true
	s.Status.Branch = ids.SessionBranch(id)
	if err := r.Status().Update(ctx, &s); err != nil {
		t.Fatal(err)
	}
}

// A produced, settled session enters the queue as Pending on reconcile.
func TestSettledSessionEntersReviewQueueAsPending(t *testing.T) {
	id := ids.NewSessionID()
	name := ids.SessionName(id)
	c := newFake(t, testCell(), credSecret("bailian-key"), settledSession(t, name, id))
	r := sessionReconciler(t, c)
	markSettled(t, r, name, id)
	reconcileSession(t, r, name, 1)

	var s acv1.Session
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: controlNS, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	if s.Status.ReviewState != acv1.ReviewPending {
		t.Errorf("reviewState = %q, want Pending", s.Status.ReviewState)
	}
}

// Rejection is terminal for automation: no PR is attempted.
func TestRejectedSessionOpensNoPR(t *testing.T) {
	id := ids.NewSessionID()
	name := ids.SessionName(id)
	c := newFake(t, testCell(), credSecret("bailian-key"), settledSession(t, name, id))
	r := sessionReconciler(t, c)
	markSettled(t, r, name, id)

	ctx := context.Background()
	var s acv1.Session
	if err := c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	s.Status.ReviewState = acv1.ReviewRejected
	s.Status.ReviewNote = "wrong approach"
	if err := c.Status().Update(ctx, &s); err != nil {
		t.Fatal(err)
	}
	reconcileSession(t, r, name, 1)

	if err := c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	if s.Status.PRNumber != 0 || s.Status.PRURL != "" {
		t.Error("a rejected session must not open a PR")
	}
	if s.Status.ReviewNote != "wrong approach" {
		t.Errorf("rejection note lost: %q", s.Status.ReviewNote)
	}
}

// Sessions that produced nothing are not reviewable at all.
func TestDiscardedSessionIsNotQueued(t *testing.T) {
	id := ids.NewSessionID()
	name := ids.SessionName(id)
	c := newFake(t, testCell(), credSecret("bailian-key"), settledSession(t, name, id))
	r := sessionReconciler(t, c)

	ctx := context.Background()
	var s acv1.Session
	if err := c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	s.Status.SessionID = id
	s.Status.Phase = acv1.SessionDiscarded
	if err := c.Status().Update(ctx, &s); err != nil {
		t.Fatal(err)
	}
	reconcileSession(t, r, name, 1)

	if err := c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	if s.Status.ReviewState != "" {
		t.Errorf("discarded session should not enter the queue, got %q", s.Status.ReviewState)
	}
}
