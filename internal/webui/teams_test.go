package webui

import (
	"testing"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/identity"
)

func team(members ...acv1.Member) *acv1.Team {
	t := &acv1.Team{}
	t.Name = "platform"
	t.Spec.Members = members
	return t
}

func cellInTeam(members ...acv1.Member) *acv1.Cell {
	c := cellWithMembers(members...)
	c.Spec.Team = "platform"
	return c
}

// The point of a team: somebody joins once and can work on every project the
// team owns, without being added to each of them.
func TestTeamMembershipReachesEveryCellTheTeamOwns(t *testing.T) {
	tm := team(acv1.Member{UserID: alice.ID(), Role: acv1.RoleMember})
	cell := cellInTeam() // named nobody directly

	if got := roleOf(alice, cell, tm); got != acv1.RoleMember {
		t.Fatalf("role = %q, want member: a team member must reach the team's Cells", got)
	}
	if !can(alice, cell, tm, ActionDispatch) {
		t.Error("a team member cannot dispatch")
	}
	if can(alice, cell, tm, ActionRelease) {
		t.Error("a team MEMBER can release; release needs maintainer wherever the role came from")
	}
}

// A Cell naming a team has an inside, so it must not also be open to
// everyone who can log in.
func TestNamingATeamClosesTheCell(t *testing.T) {
	cell := cellInTeam()
	if got := effectiveAccess(cell); got != acv1.AccessRestricted {
		t.Fatalf("access = %q, want restricted", got)
	}
	if can(bob, cell, team(), ActionView) {
		t.Error("somebody outside the team can see a Cell that belongs to it")
	}
}

// An entry on the Cell wins over the team — in BOTH directions. Taking the
// higher of the two would look generous and would make "a viewer on this one
// project" unsayable, which is exactly the exception teams exist to have.
func TestCellMembershipOverridesTheTeamBothWays(t *testing.T) {
	tm := team(acv1.Member{UserID: alice.ID(), Role: acv1.RoleMaintainer})

	lowered := cellInTeam(acv1.Member{UserID: alice.ID(), Role: acv1.RoleViewer})
	if got := roleOf(alice, lowered, tm); got != acv1.RoleViewer {
		t.Errorf("role = %q, want viewer: a Cell must be able to lower a team role", got)
	}
	if can(alice, lowered, tm, ActionRelease) {
		t.Error("a team maintainer lowered to viewer on one Cell can still release it")
	}

	tmViewers := team(acv1.Member{UserID: alice.ID(), Role: acv1.RoleViewer})
	raised := cellInTeam(acv1.Member{UserID: alice.ID(), Role: acv1.RoleMaintainer})
	if got := roleOf(alice, raised, tmViewers); got != acv1.RoleMaintainer {
		t.Errorf("role = %q, want maintainer: a Cell must be able to raise a team role", got)
	}
}

// A deleted team must not lock everyone out of the projects it governed —
// including the people who could put it right.
func TestAMissingTeamFallsBackToTheCellsOwnList(t *testing.T) {
	cell := cellInTeam(acv1.Member{UserID: alice.ID(), Role: acv1.RoleMaintainer})
	if !can(alice, cell, nil, ActionSettings) {
		t.Error("a Cell whose team is gone became unreachable to its own maintainer")
	}
	if can(bob, cell, nil, ActionView) {
		t.Error("a Cell whose team is gone fell open to everybody")
	}
}

// Whoever creates a team administers it; a static-token deployment has one
// principal who operates everything, as it does for Cells.
func TestTeamRoleOf(t *testing.T) {
	tm := team(acv1.Member{UserID: alice.ID(), Role: acv1.RoleMaintainer})
	if got := teamRoleOf(alice, tm); got != acv1.RoleMaintainer {
		t.Errorf("alice = %q, want maintainer", got)
	}
	if got := teamRoleOf(bob, tm); got != "" {
		t.Errorf("bob = %q, want no role at all", got)
	}
	if got := teamRoleOf(identity.StaticToken, tm); got != acv1.RoleMaintainer {
		t.Errorf("static token = %q, want maintainer", got)
	}
}
