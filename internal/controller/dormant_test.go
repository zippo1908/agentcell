package controller

import (
	"context"
	"io"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/pkg/ids"
)

// Idle used to mean "publish this and end it", which made "keep it in case I
// come back" and "give the slot back" the same decision. They are not: one is
// cheap (a worktree on a volume), the other is expensive (a runtime and a
// slot). Dormancy gives back only the expensive one.

func awakeSession(name string) *acv1.Session {
	s := residentSession(name, "u-owner", "t")
	s.Spec.DesiredState = acv1.SessionDesiredRunning
	return s
}

// idleFor backdates the activity clock so the reconciler sees an idle
// session without the test waiting for one.
func idleFor(s *acv1.Session, d time.Duration) {
	t := metav1.NewTime(time.Now().Add(-d))
	s.Status.LastActivity = &t
	s.Status.StartTime = &t
}

func TestIdleResidentSessionGoesDormantInsteadOfSettling(t *testing.T) {
	sess := awakeSession("sess-01k000000000000000000dor")
	sess.Status.Phase = acv1.SessionRunning
	sess.Status.SessionID = "01k000000000000000000dor"
	sess.Status.PodName = ids.UserRuntimePod(1000)
	idleFor(sess, 30*time.Minute)

	cell := testCell()
	cell.Status.SlotLeases = []string{sess.Status.SessionID}
	c := newFake(t, cell, sess, runtimePod(cell), credSecret("bailian-key"))
	r := sessionReconciler(t, c)
	r.Exec = func(context.Context, string, string, []string, io.Reader) (string, error) {
		// quiet: nothing has come out of the window for an hour.
		return "alive=true exit=0 attached=false quiet=3600\n", nil
	}

	reconcileSession(t, r, sess.Name, 1)

	var got acv1.Session
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(sess), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != acv1.SessionDormant {
		t.Fatalf("phase = %q, want Dormant — an idle session must not be settled out from under its owner",
			got.Status.Phase)
	}
	if got.Spec.DesiredState != acv1.SessionDesiredDormant {
		t.Errorf("desiredState = %q, want dormant: the intent has to be readable, not buried in a timer",
			got.Spec.DesiredState)
	}
	if got.Status.DormantSince == nil {
		t.Error("dormantSince not recorded; the publish-eventually clock has no start")
	}

	// The slot is the expensive thing, and it goes back immediately.
	var after acv1.Cell
	if err := c.Get(context.Background(),
		types.NamespacedName{Namespace: cell.Namespace, Name: cell.Name}, &after); err != nil {
		t.Fatal(err)
	}
	for _, l := range after.Status.SlotLeases {
		if l == sess.Status.SessionID {
			t.Error("a dormant session still holds a slot: it consumes nothing, so it must not charge for one")
		}
	}
}

// An agent mid-run is not idle, however long it has been since anything else
// happened — the clock must not tick through a long build.
func TestWorkingSessionIsNeverPutToSleep(t *testing.T) {
	sess := awakeSession("sess-01k000000000000000000wrk")
	sess.Status.Phase = acv1.SessionRunning
	sess.Status.SessionID = "01k000000000000000000wrk"
	sess.Status.PodName = ids.UserRuntimePod(1000)
	idleFor(sess, 30*time.Minute)

	cell := testCell()
	c := newFake(t, cell, sess, runtimePod(cell), credSecret("bailian-key"))
	r := sessionReconciler(t, c)
	r.Exec = func(context.Context, string, string, []string, io.Reader) (string, error) {
		// quiet=2: the agent is producing output right now.
		return "alive=true exit=- attached=false quiet=2\n", nil
	}
	reconcileSession(t, r, sess.Name, 1)
	var got acv1.Session
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(sess), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase == acv1.SessionDormant {
		t.Error("a session whose agent is still working was put to sleep")
	}
}

// Somebody reading the screen is using the session, even though nothing is
// running. Without this, opening a terminal to watch a finished run would be
// interrupted by the platform reclaiming it.
func TestAttachedSessionIsNeverPutToSleep(t *testing.T) {
	sess := awakeSession("sess-01k000000000000000000att")
	sess.Status.Phase = acv1.SessionRunning
	sess.Status.SessionID = "01k000000000000000000att"
	sess.Status.PodName = ids.UserRuntimePod(1000)
	idleFor(sess, 30*time.Minute)

	cell := testCell()
	c := newFake(t, cell, sess, runtimePod(cell), credSecret("bailian-key"))
	r := sessionReconciler(t, c)
	r.Exec = func(context.Context, string, string, []string, io.Reader) (string, error) {
		return "alive=true exit=0 attached=true quiet=3600\n", nil
	}
	reconcileSession(t, r, sess.Name, 1)
	var got acv1.Session
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(sess), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase == acv1.SessionDormant {
		t.Error("a session someone was watching was put to sleep")
	}
}

func runtimePod(cell *acv1.Cell) *corev1.Pod {
	p := &corev1.Pod{}
	p.Namespace = ids.WorkloadNamespace(cell.Name)
	p.Name = ids.UserRuntimePod(1000)
	p.Status.Phase = corev1.PodRunning
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Ready: true, ContainerID: "containerd://x"}}
	return p
}
