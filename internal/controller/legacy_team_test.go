package controller

import (
	"context"
	"strings"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
)

// An upgrade must never lock people out of their own project.
//
// Teams were removed. A Cell written before that carries spec.team and,
// typically, no member list of its own — the team WAS its member list. The
// access rule used to read "a named team is an inside", so after the
// upgrade such a project reported itself restricted while naming nobody:
// closed to everybody, administrators included, recoverable only by editing
// the CR with cluster access. That is the exact failure an upgrade must not
// have.
func TestLegacyTeamCellStaysReachableAfterUpgrade(t *testing.T) {
	legacy := &acv1.Cell{}
	legacy.Name = "shop"
	legacy.Spec.Team = "platform"
	// No members: the team was the list, and the team is gone.

	if got := legacy.EffectiveAccess(); got != acv1.AccessOpen {
		t.Fatalf("access = %q; a project nobody is named on became unreachable", got)
	}

	// And a project that DOES name people keeps its gate.
	named := &acv1.Cell{}
	named.Name = "shop"
	named.Spec.Team = "platform"
	named.Spec.Members = []acv1.Member{{UserID: "u-aaaa1111", Role: acv1.RoleMaintainer}}
	if got := named.EffectiveAccess(); got != acv1.AccessRestricted {
		t.Errorf("access = %q; a project with a member list must stay restricted", got)
	}
}

// The dead field is cleared, and the reason is left where somebody reads it.
//
// Leaving spec.team in place would be quiet in the wrong way: it reads like
// it still grants access, and the next person to look would reasonably
// assume the people it named can still get in. They cannot — nothing
// resolves a team any more.
func TestLegacyTeamIsClearedAndExplained(t *testing.T) {
	cell := testCell()
	cell.Spec.Team = "platform"
	cell.Spec.Members = nil
	c := newFake(t, cell)
	r := &CellReconciler{Client: c}

	if err := r.retireLegacyTeam(context.Background(), cell); err != nil {
		t.Fatal(err)
	}

	var got acv1.Cell
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(cell), &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Team != "" {
		t.Errorf("spec.team = %q, want cleared", got.Spec.Team)
	}
	// The message has to say what happened AND what to do — an operator
	// reading "team removed" alone cannot tell whether their project is now
	// open to the whole company.
	for _, want := range []string{"团队", "成员"} {
		if !strings.Contains(got.Status.Message, want) {
			t.Errorf("status message does not mention %q: %q", want, got.Status.Message)
		}
	}
	if got.EffectiveAccess() != acv1.AccessOpen {
		t.Errorf("access = %q; a cleared legacy Cell with no members must be open", got.EffectiveAccess())
	}

	// Idempotent: a second pass has nothing to do and must not churn the
	// object or overwrite a message somebody else wrote since.
	before := got.ResourceVersion
	if err := r.retireLegacyTeam(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ResourceVersion != before {
		t.Error("a second reconcile rewrote a Cell that needed no change")
	}
}

// A legacy Cell must reconcile end to end rather than erroring on a field
// the controller no longer understands.
func TestLegacyCellReconcilesWithoutTeam(t *testing.T) {
	cell := testCell()
	cell.Spec.Team = "platform"
	c := newFake(t, cell)
	r := &CellReconciler{Client: c}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKeyFromObject(cell)}); err != nil {
		t.Fatalf("a Cell from before the team removal failed to reconcile: %v", err)
	}
}
