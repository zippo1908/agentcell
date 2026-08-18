package webui

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/identity"
)

// OwnerLabel records which principal a control-namespace object belongs to.
const OwnerLabel = "agentcell.io/owner"

// errNotFound is what every ownership failure becomes.
//
// Deliberately not 403 (ADR-0008): 403 confirms the object exists, and a
// handful of probes then map out the shape of other people's work. A user
// asking about something they do not own must not be able to tell it apart
// from something that was never there.
var errNotFound = fmt.Errorf("not found")

// ownedSession loads a Session the caller is entitled to see.
func (h *Handler) ownedSession(r *http.Request, name string) (*acv1.Session, error) {
	var sess acv1.Session
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: name}, &sess); err != nil {
		return nil, errNotFound
	}
	if !h.maySession(r, &sess) {
		return nil, errNotFound
	}
	return &sess, nil
}

// LegacyTeamOwnerPrefix is what board sessions used to be owned by: a
// synthetic principal, "t-<team>", standing in for a group.
//
// It is kept only to recognise sessions written before that changed. A
// synthetic owner cannot pay for anything — there is no account behind it,
// so "whose budget funded this" had no answer — and it made the person who
// asked first invisible. Board sessions now belong to the real user who
// opened them and are marked shared instead.
const LegacyTeamOwnerPrefix = "t-"

// maySession decides who may drive a session's terminal and follow-ups.
//
// A personal session: its owner, nobody else — the tmux socket lives in that
// user's private tree and a Cell maintainer does not get somebody's keyboard.
// A team session: any member of that team, because the conversation is
// theirs collectively and a team whose shared agent only one person can talk
// to is not shared.
func (h *Handler) maySession(r *http.Request, sess *acv1.Session) bool {
	p := identity.FromContext(r.Context())
	owner := sess.Spec.OwnerUserID
	if p.Owns(owner) {
		return true
	}
	// A SHARED session — the board's conversation with a project — may be
	// driven by anyone who may dispatch in that project. That is what makes
	// it shared: a conversation only one person can answer in is not a
	// conversation the project is having.
	//
	// Operating it does not transfer it. The owner still pays, and nothing
	// here rewrites the session.
	if sess.Spec.Board != "" || strings.HasPrefix(owner, LegacyTeamOwnerPrefix) {
		name := sess.Spec.Cell
		if name == "" {
			name = strings.TrimPrefix(owner, LegacyTeamOwnerPrefix)
		}
		var c acv1.Cell
		if err := h.Client.Get(r.Context(),
			types.NamespacedName{Namespace: h.Namespace, Name: name}, &c); err == nil {
			return can(p, &c, ActionDispatch)
		}
	}
	return false
}

// visible reports whether a principal may see a Session at all.
//
// This is where the collaboration model lives. A Session is private while it
// runs — it is that user's execution and memory boundary, and its task text,
// worktree and transcript are nobody else's business. Settle is the
// controlled publication step: once a session has settled with output, its
// branch and diff are project-layer artifacts and every project member can
// see and review them. Collaboration happens at the project layer, not at
// the process layer (ADR-0008).
func visible(ctx context.Context, s *acv1.Session) bool {
	if identity.FromContext(ctx).Owns(s.Spec.OwnerUserID) {
		return true
	}
	return s.Status.Produced && s.Status.Phase == acv1.SessionSettled
}

// checkCredentialOwnership refuses to spend a model credential the caller
// does not own.
//
// Without this, any authenticated user could name someone else's Secret and
// have it injected into a session they control — which is a credential theft
// primitive, not merely an authorization gap.
//
// An unlabelled Secret is treated as belonging to the operator: it predates
// ownership, and only the static-token principal may use it.
func (h *Handler) checkCredentialOwnership(r *http.Request, secretName string) error {
	if secretName == "" {
		return nil
	}
	var sec corev1.Secret
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: secretName}, &sec); err != nil {
		return errNotFound
	}
	if !identity.FromContext(r.Context()).Owns(sec.Labels[OwnerLabel]) {
		return errNotFound
	}
	return nil
}
