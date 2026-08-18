package webui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"time"

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
	// Append under conflict retry, re-reading each time.
	//
	// Two people typing at once — or one person typing twice quickly — is
	// the ordinary case, not an error. A plain write of a value read
	// earlier silently drops whichever instruction lost the race, and the
	// person who lost it is never told: their sentence simply never
	// happened. Re-reading inside the retry is what makes the append an
	// append rather than a last-writer-wins overwrite.
	key := types.NamespacedName{Namespace: s.Namespace, Name: s.Name}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh acv1.Session
		if err := h.Client.Get(ctx, key, &fresh); err != nil {
			return err
		}
		fresh.Spec.PendingTasks = append(drainLegacyPending(&fresh), task)
		fresh.Spec.DesiredState = acv1.SessionDesiredRunning
		if err := h.Client.Update(ctx, &fresh); err != nil {
			return err
		}
		// Reflect the accepted state back to the caller's copy, so a handler
		// that answers from it does not describe a session that no longer
		// exists in that shape.
		s.Spec.PendingTasks = fresh.Spec.PendingTasks
		s.Spec.PendingTask = ""
		s.Spec.DesiredState = fresh.Spec.DesiredState
		s.ResourceVersion = fresh.ResourceVersion
		return nil
	})
	return err
}

// drainLegacyPending folds the old single-slot field into the queue.
//
// A session written before the queue existed may be holding one instruction
// in the old field. Delivering the queue and ignoring that would lose it —
// quietly, which is the failure mode the queue exists to end.
func drainLegacyPending(s *acv1.Session) []string {
	out := s.Spec.PendingTasks
	if s.Spec.PendingTask != "" {
		out = append([]string{s.Spec.PendingTask}, out...)
		s.Spec.PendingTask = ""
	}
	return out
}

// claimLiveSession makes "one live session per person per project" hold even
// when two requests arrive at the same instant.
//
// The check was a list followed by a create: two dispatches from the same
// person both looked, both saw nothing, and both created — leaving that
// person with two live sessions in one project, two worktrees, and a slot
// spent on a conversation nobody asked for. Looking is not claiming.
//
// So the claim is a Create, which the apiserver makes atomic: exactly one
// caller can create a given name. The winner goes on to make the session and
// writes its name onto the claim; anybody who lost the race reads that name
// and continues the conversation that now exists, which is what they wanted
// in the first place.
//
// The claim is owned by the session it names, so it disappears with it. A
// claim that somehow outlives its session is not fatal either: it names a
// session that no longer resolves, and the next caller clears it.
func (h *Handler) claimLiveSession(ctx context.Context, cell, owner string) (won bool, existing string, err error) {
	sum := sha256.Sum256([]byte(cell + "|" + owner))
	name := "sessclaim-" + hex.EncodeToString(sum[:8])
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Namespace: h.Namespace, Name: name,
		Labels: map[string]string{"agentcell.io/claim": "session"},
	}}
	err = h.Client.Create(ctx, cm)
	if err == nil {
		return true, "", nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return false, "", err
	}
	// Somebody else is creating it, or already has. Wait for them to write
	// the name down — briefly, because the alternative is answering "try
	// again" to a person who did nothing wrong.
	for attempt := 0; attempt < 20; attempt++ {
		var live corev1.ConfigMap
		if err := h.Client.Get(ctx, types.NamespacedName{
			Namespace: h.Namespace, Name: name}, &live); err != nil {
			if apierrors.IsNotFound(err) {
				// The winner failed and cleaned up; take the claim.
				return h.claimLiveSession(ctx, cell, owner)
			}
			return false, "", err
		}
		if s := live.Data["session"]; s != "" {
			return false, s, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false, "", fmt.Errorf("这个项目里你的会话正在创建,稍后再说一次")
}

// nameTheClaim records which session won a claim, and ties the claim's life
// to it.
func (h *Handler) nameTheClaim(ctx context.Context, cell, owner string, sess *acv1.Session) error {
	sum := sha256.Sum256([]byte(cell + "|" + owner))
	var cm corev1.ConfigMap
	key := types.NamespacedName{Namespace: h.Namespace, Name: "sessclaim-" + hex.EncodeToString(sum[:8])}
	if err := h.Client.Get(ctx, key, &cm); err != nil {
		return err
	}
	cm.Data = map[string]string{"session": sess.Name}
	cm.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: acv1.GroupVersion.String(), Kind: "Session",
		Name: sess.Name, UID: sess.UID,
	}}
	return h.Client.Update(ctx, &cm)
}

// releaseClaim drops a claim whose session was never created, so the next
// caller is not told to wait for something that will never arrive.
func (h *Handler) releaseClaim(ctx context.Context, cell, owner string) {
	sum := sha256.Sum256([]byte(cell + "|" + owner))
	_ = h.Client.Delete(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Namespace: h.Namespace, Name: "sessclaim-" + hex.EncodeToString(sum[:8]),
	}})
}
