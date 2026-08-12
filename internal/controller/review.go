package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/forge"
)

// ADR-0006: after a session settles with output, it enters the review queue.
// Approval opens a PR through the broker (celld holds no forge credential);
// the PR's merge state is then reconciled with backoff until terminal.

const prPollInterval = 2 * time.Minute

// ForgeClient is the slice of the broker client the review loop needs. An
// interface so the idempotency paths (create succeeded but the status write
// was lost) can be exercised without a forge.
type ForgeClient interface {
	Enabled() bool
	FindPull(ctx context.Context, cell, sessionID string) (*forge.Result, error)
	CreatePull(ctx context.Context, cell, sessionID, title, body string) (*forge.Result, error)
	GetPull(ctx context.Context, cell string, number int) (*forge.Result, error)
}

// reviewReconcile advances the review side of a terminal session: it opens
// the PR for a freshly approved session and refreshes an open PR's state.
// Returns a requeue delay (0 = nothing pending).
func (r *SessionReconciler) reviewReconcile(ctx context.Context, sess *acv1.Session) (ctrl.Result, error) {
	// Only produced sessions are reviewable.
	if !sess.Status.Produced || sess.Status.Phase != acv1.SessionSettled {
		return ctrl.Result{}, nil
	}
	// Default a produced session into the queue.
	if sess.Status.ReviewState == "" {
		sess.Status.ReviewState = acv1.ReviewPending
		if err := r.Status().Update(ctx, sess); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	if sess.Status.ReviewState != acv1.ReviewApproved {
		return ctrl.Result{}, nil // Pending or Rejected: nothing automatic
	}
	if r.Forge == nil || !r.Forge.Enabled() {
		return ctrl.Result{}, nil
	}

	// Approved but no PR recorded → adopt or open one. This must be truly
	// idempotent: a create that succeeded but whose status write was lost
	// (controller crash / conflict) would otherwise be retried forever
	// against a forge that answers "already exists". So: look first, create
	// only if absent, and fall back to looking again if create fails.
	if sess.Status.PRNumber == 0 {
		if found, err := r.Forge.FindPull(ctx, sess.Spec.Cell, sess.Status.SessionID); err == nil && found.Number != 0 {
			return r.recordPR(ctx, sess, found.URL, found.Number, found.State)
		}
		title := fmt.Sprintf("AgentCell: %s", firstLine(sess.Spec.Task))
		body := fmt.Sprintf(
			"Opened by AgentCell after review approval.\n\n**Task**\n\n%s\n\n**Session** `%s`\n**Branch** `%s`\n",
			sess.Spec.Task, sess.Status.SessionID, sess.Status.Branch)
		res, err := r.Forge.CreatePull(ctx, sess.Spec.Cell, sess.Status.SessionID, title, body)
		if err != nil {
			// The create may have actually landed (e.g. "already exists", or
			// a lost response). Re-check before reporting failure.
			if found, ferr := r.Forge.FindPull(ctx, sess.Spec.Cell, sess.Status.SessionID); ferr == nil && found.Number != 0 {
				return r.recordPR(ctx, sess, found.URL, found.Number, found.State)
			}
			sess.Status.ReviewNote = "PR creation failed: " + err.Error()
			if uerr := r.Status().Update(ctx, sess); uerr != nil {
				return ctrl.Result{}, uerr
			}
			return ctrl.Result{RequeueAfter: prPollInterval}, nil
		}
		return r.recordPR(ctx, sess, res.URL, res.Number, res.State)
	}

	// Track the merge state until terminal.
	if sess.Status.PRState == "merged" || sess.Status.PRState == "closed" {
		return ctrl.Result{}, nil
	}
	res, err := r.Forge.GetPull(ctx, sess.Spec.Cell, sess.Status.PRNumber)
	if err != nil {
		return ctrl.Result{RequeueAfter: prPollInterval}, nil
	}
	if res.State != sess.Status.PRState {
		sess.Status.PRState = res.State
		if err := r.Status().Update(ctx, sess); err != nil {
			return ctrl.Result{}, err
		}
	}
	if res.State == "merged" || res.State == "closed" {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: prPollInterval}, nil
}

// recordPR writes the PR coordinates and clears any earlier failure note.
func (r *SessionReconciler) recordPR(ctx context.Context, sess *acv1.Session, url string, number int, state string) (ctrl.Result, error) {
	sess.Status.PRURL, sess.Status.PRNumber, sess.Status.PRState = url, number, state
	if strings.HasPrefix(sess.Status.ReviewNote, "PR creation failed:") {
		sess.Status.ReviewNote = ""
	}
	if err := r.Status().Update(ctx, sess); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: prPollInterval}, nil
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	if len(s) > 72 {
		return s[:72]
	}
	return s
}
