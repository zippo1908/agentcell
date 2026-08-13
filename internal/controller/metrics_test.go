package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func activeSession(name string) *acv1.Session {
	s := newSession(name, "build the cart page")
	s.Status.Phase = acv1.SessionRunning
	return s
}

// TestCellMetricsReflectActiveSessionsAndTotal exercises the acceptance
// criteria directly: agentcell_cell_active_sessions{cell="shop"} tracks the
// live session count, and agentcell_cells_total tracks the fleet.
func TestCellMetricsReflectActiveSessionsAndTotal(t *testing.T) {
	c := newFake(t, testCell(), activeSession("s1"), activeSession("s2"))
	reconcileCell(t, c)

	if got := testutil.ToFloat64(cellActiveSessions.WithLabelValues("shop")); got != 2 {
		t.Errorf("agentcell_cell_active_sessions{cell=shop} = %v, want 2", got)
	}
	if got := testutil.ToFloat64(cellsTotal); got != 1 {
		t.Errorf("agentcell_cells_total = %v, want 1", got)
	}

	// A settled/terminal session drops out of the active count on the next
	// reconcile, same as Cell.Status.ActiveSessions.
	ctx := context.Background()
	var s acv1.Session
	if err := c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "s1"}, &s); err != nil {
		t.Fatal(err)
	}
	s.Status.Phase = "Succeeded"
	if err := c.Status().Update(ctx, &s); err != nil {
		t.Fatal(err)
	}
	reconcileCell(t, c)
	if got := testutil.ToFloat64(cellActiveSessions.WithLabelValues("shop")); got != 1 {
		t.Errorf("agentcell_cell_active_sessions{cell=shop} after settle = %v, want 1", got)
	}

	// Deleting the cell removes its series and the total drops to zero —
	// nothing should be left reporting load for a cell that no longer exists.
	var cell acv1.Cell
	if err := c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "shop"}, &cell); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, &cell); err != nil {
		t.Fatal(err)
	}
	r := &CellReconciler{Client: c}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "shop"}}
	for range 2 { // namespace-gone requeue, then finalizer removal
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	if got := testutil.ToFloat64(cellsTotal); got != 0 {
		t.Errorf("agentcell_cells_total after delete = %v, want 0", got)
	}
	if n := testutil.CollectAndCount(cellActiveSessions); n != 0 {
		t.Errorf("agentcell_cell_active_sessions still reports %d series for a deleted cell", n)
	}
}