package webui

import (
	"fmt"
	"k8s.io/apimachinery/pkg/types"
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
// The team is passed in rather than fetched here so this stays a pure
// function of (who, what, which team) — the property that makes the whole
// rule set readable on one screen and exhaustively testable without a
// cluster. nil means the Cell names no team, or the team is gone.
func roleOf(p identity.Principal, cell *acv1.Cell, team *acv1.Team) acv1.Role {
	if effectiveAccess(cell) == acv1.AccessOpen {
		return acv1.RoleMaintainer
	}
	// A static-token deployment has exactly one principal, so it is the
	// operator of everything; making it a viewer would lock the single-user
	// story out of its own console.
	if p.Kind == identity.KindToken {
		return acv1.RoleMaintainer
	}
	id := p.ID()
	// An explicit entry on the Cell WINS over the team — in both directions.
	//
	// Taking the higher of the two would look generous and be wrong: it
	// would make "this person is a viewer on this one project" unsayable,
	// which is precisely the exception a team exists to have. So a name on
	// the Cell is the answer, and the team is the default for everyone not
	// named.
	for _, m := range cell.Spec.Members {
		if m.UserID == id {
			return m.Role
		}
	}
	if team != nil {
		return team.RoleOf(id)
	}
	return ""
}

// can is the decision.
func can(p identity.Principal, cell *acv1.Cell, team *acv1.Team, a Action) bool {
	return rank(roleOf(p, cell, team)) >= rank(required(a))
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
	team := h.teamFor(r, cell)
	if can(p, cell, team, a) {
		return true
	}
	if can(p, cell, team, ActionView) {
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

// effectiveAccess resolves the mode actually in force.
//
// Unset means open when there are no members — the pre-roles behaviour — and
// restricted the moment somebody is named, because adding a member is an
// unambiguous statement that this project has an inside and an outside.
// effectiveAccess delegates to the type, so the rule the controller records
// in status and the rule enforced here cannot drift apart.
func effectiveAccess(cell *acv1.Cell) acv1.AccessMode { return cell.EffectiveAccess() }

// teamFor loads the Team a Cell names, or nil.
//
// A missing team is nil rather than an error on purpose: a Cell whose team
// was deleted must not become unreachable to everyone including the people
// who could fix it. It falls back to the Cell's own member list, which is
// the state it would have had without a team at all.
func (h *Handler) teamFor(r *http.Request, cell *acv1.Cell) *acv1.Team {
	if cell.Spec.Team == "" {
		return nil
	}
	var t acv1.Team
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: cell.Spec.Team}, &t); err != nil {
		return nil
	}
	return &t
}
