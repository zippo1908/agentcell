package webui

import (
	"fmt"
	"net/http"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/identity"
)

// Every authorization decision in AgentCell is made here.
//
// Not because the rules are complicated — three roles and a handful of verbs
// fit on one screen — but because that is the seam. If the rules ever
// outgrow a table (organisation hierarchies, deny rules, ACLs users edit
// themselves), a policy engine goes behind this function and no handler
// learns anything about it. Scattering `if role == …` through the handlers
// is what makes that swap impossible later (ADR-0013).

// Action is a thing someone might do to a Cell.
type Action string

const (
	ActionView     Action = "view"
	ActionDispatch Action = "dispatch"
	ActionReview   Action = "review"
	// ActionRelease is the line that matters: everything else is
	// recoverable, and a release is the one action that puts code in front
	// of users.
	ActionRelease  Action = "release"
	ActionSettings Action = "settings"
)

// rank orders the roles so a check is a comparison rather than a set of
// special cases — and so an unknown role ranks below viewer instead of
// accidentally above it.
func rank(r acv1.Role) int {
	switch r {
	case acv1.RoleMaintainer:
		return 3
	case acv1.RoleMember:
		return 2
	case acv1.RoleViewer:
		return 1
	}
	return 0
}

// required is the minimum role for each action.
func required(a Action) acv1.Role {
	switch a {
	case ActionRelease, ActionSettings:
		return acv1.RoleMaintainer
	case ActionDispatch, ActionReview:
		return acv1.RoleMember
	default:
		return acv1.RoleViewer
	}
}

// roleOf reports a principal's role in a Cell.
//
// A Cell with no members is open to every authenticated user at maintainer
// level: that is exactly how every existing Cell behaves, and an upgrade
// that locked people out of their own projects would be reverted before it
// was understood. Membership is opt-in, and taking it up is what turns the
// rules on.
func roleOf(p identity.Principal, cell *acv1.Cell) acv1.Role {
	if len(cell.Spec.Members) == 0 {
		return acv1.RoleMaintainer
	}
	// A static-token deployment has exactly one principal, so it is the
	// operator of everything; making it a viewer would lock the single-user
	// story out of its own console.
	if p.Kind == identity.KindToken {
		return acv1.RoleMaintainer
	}
	id := p.ID()
	best := acv1.Role("")
	for _, m := range cell.Spec.Members {
		if m.UserID == id && rank(m.Role) > rank(best) {
			best = m.Role
		}
	}
	return best
}

// can is the decision.
func can(p identity.Principal, cell *acv1.Cell, a Action) bool {
	return rank(roleOf(p, cell)) >= rank(required(a))
}

// authorize checks a request and writes the refusal itself.
//
// Refusals are 404, not 403, for the same reason ownership failures are: a
// 403 confirms the Cell exists and that you are outside it, which over a few
// probes maps out what a team is working on. The one exception is an action
// on a Cell you CAN see — there, 403 with the required role is what lets
// someone ask the right person for access instead of guessing.
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, cell *acv1.Cell, a Action) bool {
	p := identity.FromContext(r.Context())
	if can(p, cell, a) {
		return true
	}
	if can(p, cell, ActionView) {
		writeErr(w, http.StatusForbidden,
			errRequiresRole(a, required(a)))
		return false
	}
	writeErr(w, http.StatusNotFound, errNotFound)
	return false
}

func errRequiresRole(a Action, role acv1.Role) error {
	return fmt.Errorf("%s requires the %s role on this cell", a, role)
}
