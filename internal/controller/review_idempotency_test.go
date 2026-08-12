package controller

import (
	"context"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/forge"
	"github.com/zippo1908/agentcell/pkg/ids"
)

// fakeForge models a forge where a PR may already exist for a session.
type fakeForge struct {
	existing    *forge.Result // what FindPull returns
	creates     int
	createErr   error
	createdOnce bool
}

func (f *fakeForge) Enabled() bool { return true }

func (f *fakeForge) FindPull(_ context.Context, _, _ string) (*forge.Result, error) {
	if f.existing == nil {
		return &forge.Result{}, nil
	}
	return f.existing, nil
}

func (f *fakeForge) CreatePull(_ context.Context, _, _, _, _ string) (*forge.Result, error) {
	f.creates++
	if f.createErr != nil {
		// Model "create landed but the caller didn't learn about it": the PR
		// now exists even though the call reports an error.
		if !f.createdOnce {
			f.createdOnce = true
			f.existing = &forge.Result{URL: "https://forge/pr/7", Number: 7, State: "open"}
		}
		return nil, f.createErr
	}
	f.existing = &forge.Result{URL: "https://forge/pr/7", Number: 7, State: "open"}
	return f.existing, nil
}

func (f *fakeForge) GetPull(_ context.Context, _ string, _ int) (*forge.Result, error) {
	if f.existing == nil {
		return &forge.Result{}, nil
	}
	return f.existing, nil
}

func approvedSession(t *testing.T, r *SessionReconciler, name, id string) {
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
	s.Status.ReviewState = acv1.ReviewApproved
	if err := r.Status().Update(ctx, &s); err != nil {
		t.Fatal(err)
	}
}

// A PR that already exists for this session must be adopted, never
// re-created — this is the crash window where the create succeeded but the
// status write was lost.
func TestApprovalAdoptsExistingPRInsteadOfRecreating(t *testing.T) {
	id := ids.NewSessionID()
	name := ids.SessionName(id)
	c := newFake(t, testCell(), credSecret("bailian-key"), newSession(name, "t"))
	ff := &fakeForge{existing: &forge.Result{URL: "https://forge/pr/42", Number: 42, State: "open"}}
	r := sessionReconciler(t, c)
	r.Forge = ff
	approvedSession(t, r, name, id)

	reconcileSession(t, r, name, 1)

	if ff.creates != 0 {
		t.Errorf("created %d PRs; an existing PR must be adopted, not re-created", ff.creates)
	}
	var s acv1.Session
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: controlNS, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	if s.Status.PRNumber != 42 || s.Status.PRURL != "https://forge/pr/42" {
		t.Errorf("existing PR not adopted: %+v", s.Status)
	}
}

// If the create call errors but the PR actually landed, the next pass must
// recover it rather than retrying forever against "already exists".
func TestFailedCreateRecoversLandedPR(t *testing.T) {
	id := ids.NewSessionID()
	name := ids.SessionName(id)
	c := newFake(t, testCell(), credSecret("bailian-key"), newSession(name, "t"))
	ff := &fakeForge{createErr: fmt.Errorf("forge returned 422: A pull request already exists")}
	r := sessionReconciler(t, c)
	r.Forge = ff
	approvedSession(t, r, name, id)

	reconcileSession(t, r, name, 1)

	var s acv1.Session
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: controlNS, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	if s.Status.PRNumber != 7 {
		t.Fatalf("landed PR not recovered after a failing create: %+v", s.Status)
	}
	if s.Status.ReviewNote != "" {
		t.Errorf("stale failure note kept after recovery: %q", s.Status.ReviewNote)
	}
}

// Repeated reconciles of an approved session create exactly one PR.
func TestApprovalCreatesExactlyOnePR(t *testing.T) {
	id := ids.NewSessionID()
	name := ids.SessionName(id)
	c := newFake(t, testCell(), credSecret("bailian-key"), newSession(name, "t"))
	ff := &fakeForge{}
	r := sessionReconciler(t, c)
	r.Forge = ff
	approvedSession(t, r, name, id)

	reconcileSession(t, r, name, 4)

	if ff.creates != 1 {
		t.Errorf("PR created %d times across repeated reconciles, want exactly 1", ff.creates)
	}
}
