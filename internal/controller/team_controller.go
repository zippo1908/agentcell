package controller

import (
	"context"
	"sort"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
)

// TeamReconciler records what a team actually governs.
//
// It creates nothing. A Team is a membership list, and the only thing the
// control plane can add is the answer to the question a person asks right
// before they change one: which projects does this affect? Without it, the
// blast radius of removing somebody is invisible until somebody else can no
// longer open a Cell.
type TeamReconciler struct {
	client.Client
}

func (r *TeamReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var team acv1.Team
	if err := r.Get(ctx, req.NamespacedName, &team); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	var cells acv1.CellList
	if err := r.List(ctx, &cells, client.InNamespace(team.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	names := []string{}
	for i := range cells.Items {
		if cells.Items[i].Spec.Team == team.Name {
			names = append(names, cells.Items[i].Name)
		}
	}
	sort.Strings(names)
	team.Status.CellNames = names
	team.Status.Cells = len(names)
	team.Status.Members = len(team.Spec.Members)
	team.Status.ObservedGeneration = team.Generation
	return ctrl.Result{}, r.Status().Update(ctx, &team)
}

func (r *TeamReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&acv1.Team{}).
		// A Cell changing teams changes two teams' answers — the one it left
		// and the one it joined — and neither of them changed themselves.
		Watches(&acv1.Cell{}, handler.EnqueueRequestsFromMapFunc(
			func(ctx context.Context, obj client.Object) []ctrl.Request {
				var list acv1.TeamList
				if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
					return nil
				}
				out := make([]ctrl.Request, 0, len(list.Items))
				for i := range list.Items {
					out = append(out, ctrl.Request{
						NamespacedName: client.ObjectKeyFromObject(&list.Items[i]),
					})
				}
				return out
			})).
		Named("team").
		Complete(r)
}
