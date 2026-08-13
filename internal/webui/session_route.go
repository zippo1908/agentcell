package webui

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
)

// One live session per user per Cell.
//
// A session was a second atom beside the project, and it did not earn the
// place: the agent CLIs open and switch conversations themselves — that is
// the reason this platform gives them a private $HOME and a terminal that
// outlives a run rather than reimplementing conversation management. Stacking
// a platform-level "session" on top of that duplicated it, and the
// duplication had a cost people actually hit: with a slot cap of two, a third
// session could not be woken, so somebody could not get back into their own
// work.
//
// So the shape is: the project is the atom, and each person has ONE live
// session in it — their worktree, their branch, their terminal. Asking for
// more work continues that conversation. Finished sessions stay as history.
// The slot cap now bounds how many PEOPLE can work in a Cell at once, which
// is what it was always measuring.
func liveSessionFor(ctx context.Context, c client.Client, ns, cell, owner string) (*acv1.Session, error) {
	var list acv1.SessionList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, err
	}
	for i := range list.Items {
		s := &list.Items[i]
		if s.Spec.Cell != cell || s.Spec.OwnerUserID != owner || !s.DeletionTimestamp.IsZero() {
			continue
		}
		switch s.Status.Phase {
		case acv1.SessionSettled, acv1.SessionDiscarded, acv1.SessionError:
			continue
		}
		return s, nil
	}
	return nil, nil
}

// queueFollowUp writes the next instruction onto the session and wakes it.
//
// Written rather than typed straight in, because the session may be asleep:
// the alternatives are losing the instruction or making the caller wait for
// a pod to be scheduled. The controller delivers it when the terminal is
// back — one path, whether the session was awake or not, so an awake session
// and a sleeping one cannot behave differently.
func (h *Handler) queueFollowUp(ctx context.Context, s *acv1.Session, task string) error {
	s.Spec.PendingTask = task
	s.Spec.DesiredState = acv1.SessionDesiredRunning
	return h.Client.Update(ctx, s)
}
