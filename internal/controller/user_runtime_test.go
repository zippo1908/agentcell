package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/useruid"
	"github.com/zippo1908/agentcell/pkg/ids"
)

// recorder captures what the control plane sends into a runtime pod.
type recorder struct {
	mu    sync.Mutex
	calls []struct {
		pod   string
		argv  []string
		stdin string
	}
}

func (rec *recorder) exec(_ context.Context, _, pod string, argv []string, stdin io.Reader) (string, error) {
	body := ""
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		body = string(b)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.calls = append(rec.calls, struct {
		pod   string
		argv  []string
		stdin string
	}{pod, argv, body})
	return "", nil
}

func residentSession(name, owner, task string) *acv1.Session {
	s := newSession(name, task)
	s.Spec.Resident = &[]bool{true}[0]
	s.Spec.OwnerUserID = owner
	return s
}

// Two sessions belonging to one user share ONE tmux server, in one pod. The
// agent CLIs manage conversations themselves, so a process per conversation
// would buy nothing and cost a pod each.
func TestOneRuntimePerUserHostsManySessions(t *testing.T) {
	a := residentSession("sess-a", "u-aaaa1111", "first")
	b := residentSession("sess-b", "u-aaaa1111", "second")
	c := newFake(t, testCell(), credSecret("bailian-key"), a, b)
	rec := &recorder{}
	r := sessionReconciler(t, c)
	r.UIDs = &useruid.Allocator{Client: c, Namespace: controlNS}
	r.Exec = rec.exec

	ctx := context.Background()
	ns := ids.WorkloadNamespace("shop")
	req := func(n string) ctrl.Request {
		return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: controlNS, Name: n}}
	}
	// First pass creates the runtime; it is not Ready yet, so no window opens.
	for _, n := range []string{"sess-a", "sess-b"} {
		if _, err := r.Reconcile(ctx, req(n)); err != nil {
			t.Fatal(err)
		}
	}
	uid, err := (&useruid.Allocator{Client: c, Namespace: controlNS}).Ensure(ctx, "u-aaaa1111")
	if err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.UserRuntimePod(uid)}, &pod); err != nil {
		t.Fatalf("no runtime pod for the user: %v", err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("a window was opened before the runtime was ready: %v", rec.calls)
	}

	// Kubelet says ready.
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "runtime", Ready: true}}
	if err := c.Status().Update(ctx, &pod); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"sess-a", "sess-b"} {
		for range 2 {
			if _, err := r.Reconcile(ctx, req(n)); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Both sessions opened a window in the SAME pod.
	if len(rec.calls) < 2 {
		t.Fatalf("expected a window per session, got %d", len(rec.calls))
	}
	for _, call := range rec.calls {
		if call.pod != ids.UserRuntimePod(uid) {
			t.Errorf("window opened in %q, want the user's runtime %q", call.pod, ids.UserRuntimePod(uid))
		}
	}
	// And no session got a pod of its own.
	for _, n := range []string{"sess-a", "sess-b"} {
		var p corev1.Pod
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: n}, &p); err == nil {
			t.Errorf("%s got its own pod; resident sessions are windows", n)
		}
	}
}

// A model key in argv is readable from /proc by every other window this user
// has open. It travels on stdin instead.
func TestModelCredentialNeverAppearsInArgv(t *testing.T) {
	s := residentSession("sess-a", "u-aaaa1111", "work")
	c := newFake(t, testCell(), credSecret("bailian-key"), s)
	rec := &recorder{}
	r := sessionReconciler(t, c)
	r.UIDs = &useruid.Allocator{Client: c, Namespace: controlNS}
	r.Exec = rec.exec
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "sess-a"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	uid, _ := (&useruid.Allocator{Client: c, Namespace: controlNS}).Ensure(ctx, "u-aaaa1111")
	var pod corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{
		Namespace: ids.WorkloadNamespace("shop"), Name: ids.UserRuntimePod(uid)}, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "runtime", Ready: true}}
	_ = c.Status().Update(ctx, &pod)
	for range 2 {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	var call *struct {
		pod   string
		argv  []string
		stdin string
	}
	for i := range rec.calls {
		if len(rec.calls[i].argv) > 1 && rec.calls[i].argv[1] == "window-open" {
			call = &rec.calls[i]
		}
	}
	if call == nil {
		t.Fatalf("no window-open among %d calls", len(rec.calls))
	}
	joined := strings.Join(call.argv, " ")
	if strings.Contains(joined, "sk-test") {
		t.Errorf("the model key leaked into argv: %s", joined)
	}
	if !strings.Contains(call.stdin, "sk-test") {
		t.Errorf("the model key did not reach the window over stdin: %q", call.stdin)
	}
	// The briefing has to cross too, or TASK.md is written blank.
	var env map[string]string
	if err := json.Unmarshal([]byte(call.stdin), &env); err != nil {
		t.Fatalf("stdin is not a JSON object: %v", err)
	}
	if env["AGENTCELL_TASK"] != "work" {
		t.Errorf("the task text did not cross the exec channel: %q", call.stdin)
	}
}

// A runtime is reclaimed when its user has nothing open — but "nothing open"
// has to include a session that is still starting. Reaping on "not Running"
// deleted the runtime out from under a session that was coming up, and the
// real cluster showed it as `Pod "runtime-100002" not found`.
func TestRuntimeIsNotReapedWhileASessionIsStarting(t *testing.T) {
	starting := residentSession("sess-a", "u-aaaa1111", "still coming up") // no phase yet
	finished := residentSession("sess-b", "u-aaaa1111", "done")
	finished.Status.Phase = acv1.SessionSettled
	c := newFake(t, testCell(), credSecret("bailian-key"), starting, finished)
	r := sessionReconciler(t, c)
	r.UIDs = &useruid.Allocator{Client: c, Namespace: controlNS}
	r.Exec = (&recorder{}).exec

	ctx := context.Background()
	ns := ids.WorkloadNamespace("shop")
	uid, err := r.UIDs.Ensure(ctx, "u-aaaa1111")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ensureUserRuntime(ctx, testCell(), ns, uid); err != nil {
		t.Fatal(err)
	}
	if err := r.reapUserRuntime(ctx, ns, uid, "shop"); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.UserRuntimePod(uid)}, &pod); err != nil {
		t.Fatalf("the runtime was reaped while a session was still starting: %v", err)
	}

	// Once every session has finished, it goes away.
	starting.Status.Phase = acv1.SessionDiscarded
	if err := c.Status().Update(ctx, starting); err != nil {
		t.Fatal(err)
	}
	if err := r.reapUserRuntime(ctx, ns, uid, "shop"); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.UserRuntimePod(uid)}, &pod); err == nil {
		t.Error("the runtime outlived every session that needed it")
	}
}

// The pod is created through a cache-backed client, so reading it straight
// back can miss. That is "not ready yet", not a failure — treating it as one
// failed the first session of every runtime.
func TestFreshRuntimeIsNotReadyRatherThanMissing(t *testing.T) {
	c := newFake(t, testCell(), credSecret("bailian-key"))
	r := sessionReconciler(t, c)
	ready, err := r.ensureUserRuntime(context.Background(), testCell(), ids.WorkloadNamespace("shop"), 100001)
	if err != nil {
		t.Fatalf("a just-created runtime reported an error: %v", err)
	}
	if ready {
		t.Error("a runtime with no ready container reported ready")
	}
}

// A resident session's host is its owner's runtime, and the status has to say
// so: every later lookup — state, follow-ups, attach — goes through PodName.
// Overwriting it with the session's own name pointed all of them at a pod
// that does not exist, and the session quietly behaved like a one-shot.
func TestResidentSessionRecordsItsRuntimeAsHost(t *testing.T) {
	s := residentSession("sess-a", "u-aaaa1111", "work")
	c := newFake(t, testCell(), credSecret("bailian-key"), s)
	r := sessionReconciler(t, c)
	r.UIDs = &useruid.Allocator{Client: c, Namespace: controlNS}
	r.Exec = (&recorder{}).exec
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "sess-a"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	uid, _ := r.UIDs.Ensure(ctx, "u-aaaa1111")
	ns := ids.WorkloadNamespace("shop")
	var pod corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.UserRuntimePod(uid)}, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "runtime", Ready: true}}
	_ = c.Status().Update(ctx, &pod)
	for range 2 {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	var got acv1.Session
	if err := c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "sess-a"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.PodName != ids.UserRuntimePod(uid) {
		t.Errorf("host = %q, want the user's runtime %q", got.Status.PodName, ids.UserRuntimePod(uid))
	}
}

// Two of a user's sessions starting at once both read "no runtime" from the
// cache and both create one. Losing that race is the runtime existing, which
// is the goal — treating it as an error failed one of the two sessions with
// `pods "runtime-100002" already exists`.
func TestConcurrentStartsDoNotFailOnTheLosingCreate(t *testing.T) {
	c := newFake(t, testCell(), credSecret("bailian-key"))
	r := sessionReconciler(t, c)
	ctx := context.Background()
	ns := ids.WorkloadNamespace("shop")
	if _, err := r.ensureUserRuntime(ctx, testCell(), ns, 100002); err != nil {
		t.Fatal(err)
	}
	// A second caller that did not see the first one's write.
	if _, err := r.ensureUserRuntime(ctx, testCell(), ns, 100002); err != nil {
		t.Errorf("the second session failed on an existing runtime: %v", err)
	}
}

// A runtime that disappears takes its windows, not the work: the worktree is
// on the volume and the CLI's conversation is in the private $HOME. Settling
// there would throw away a session its owner never ended.
func TestLostRuntimeIsRebuiltRatherThanSettled(t *testing.T) {
	s := residentSession("sess-a", "u-aaaa1111", "work")
	s.Status.Phase = acv1.SessionRunning
	s.Status.SessionID = "a"
	s.Status.PodName = "runtime-100002"
	now := metav1.Now()
	s.Status.StartTime = &now
	c := newFake(t, testCell(), credSecret("bailian-key"), s)
	rec := &recorder{}
	r := sessionReconciler(t, c)
	r.UIDs = &useruid.Allocator{Client: c, Namespace: controlNS}
	r.Exec = rec.exec
	ctx := context.Background()
	ns := ids.WorkloadNamespace("shop")
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "sess-a"}}

	// The runtime is absent: first pass creates it, and nothing settles.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var got acv1.Session
	_ = c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "sess-a"}, &got)
	if got.Status.Phase == acv1.SessionSettling || got.Status.Phase == acv1.SessionSettled {
		t.Fatalf("a lost runtime settled the session: %s", got.Status.Phase)
	}
	uid, _ := r.UIDs.Ensure(ctx, "u-aaaa1111")
	var pod corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.UserRuntimePod(uid)}, &pod); err != nil {
		t.Fatalf("the runtime was not rebuilt: %v", err)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "runtime", Ready: true}}
	_ = c.Status().Update(ctx, &pod)
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}

	// The window is restored, NOT re-run: a second agent run would duplicate
	// whatever the first had already committed to the worktree.
	var restore *struct {
		pod   string
		argv  []string
		stdin string
	}
	// What marks a restore is that the agent came back on the conversation
	// that was already here — not a flag. With an interactive agent the
	// terminal IS the agent, so "restored" and "the agent is running with
	// --continue" are the same statement.
	for i := range rec.calls {
		if len(rec.calls[i].argv) < 2 || rec.calls[i].argv[1] != "window-open" {
			continue
		}
		joined := strings.Join(rec.calls[i].argv, " ")
		if strings.Contains(joined, "-restore") || strings.Contains(joined, "--continue") {
			restore = &rec.calls[i]
		}
	}
	if restore == nil {
		t.Fatalf("no window restore among %d calls", len(rec.calls))
	}
	if !strings.Contains(restore.stdin, "sk-test") {
		t.Error("the restored window has no model credential; a follow-up could not run")
	}
	_ = c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "sess-a"}, &got)
	if got.Status.Recoveries != 1 {
		t.Errorf("recoveries = %d, want 1", got.Status.Recoveries)
	}
}

// Recovery is bounded. A node that keeps evicting must eventually settle the
// work rather than rebuild forever.
func TestRepeatedRuntimeLossEventuallySettles(t *testing.T) {
	s := residentSession("sess-a", "u-aaaa1111", "work")
	s.Status.Phase = acv1.SessionRunning
	s.Status.SessionID = "a"
	s.Status.PodName = "runtime-100002"
	s.Status.Recoveries = maxRecoveries
	now := metav1.Now()
	s.Status.StartTime = &now
	c := newFake(t, testCell(), credSecret("bailian-key"), s)
	r := sessionReconciler(t, c)
	r.UIDs = &useruid.Allocator{Client: c, Namespace: controlNS}
	r.Exec = (&recorder{}).exec
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "sess-a"}}); err != nil {
		t.Fatal(err)
	}
	var got acv1.Session
	_ = c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "sess-a"}, &got)
	// Parked, not published. A runtime that keeps dying is something to
	// look at; it is not the owner deciding their project is finished, and
	// spending their work to stop a rebuild loop is too high a price for
	// stopping a rebuild loop.
	if got.Spec.DesiredState != acv1.SessionDesiredDormant {
		t.Errorf("desiredState = %q, want the session parked after %d losses",
			got.Spec.DesiredState, maxRecoveries)
	}
	if got.Status.Phase == acv1.SessionSettling {
		t.Error("a flapping runtime ended the project instead of stopping it")
	}
}

// A runtime that will not stay up must stop being rebuilt — but stopping is
// where it ends. See the assertions below for what replaced settling.
//
// One session's TTL must not take its owner's other sessions with it. The
// pod behind a resident session is the SHARED runtime, so deleting it on
// timeout killed every window that user had open — their unpublished work
// included.
func TestTTLClosesTheWindowNotTheSharedRuntime(t *testing.T) {
	old := residentSession("sess-old", "u-aaaa1111", "expired")
	old.Spec.TTLSeconds = 60
	old.Status.Phase = acv1.SessionRunning
	old.Status.SessionID = "old"
	// PodName is set after the uid is allocated, below.
	long := metav1.NewTime(metav1.Now().Add(-2 * time.Hour))
	old.Status.StartTime = &long

	live := residentSession("sess-live", "u-aaaa1111", "still working")
	live.Status.Phase = acv1.SessionRunning
	live.Status.SessionID = "live"

	now := metav1.Now()
	live.Status.StartTime = &now

	c := newFake(t, testCell(), credSecret("bailian-key"), old, live)
	rec := &recorder{}
	r := sessionReconciler(t, c)
	r.UIDs = &useruid.Allocator{Client: c, Namespace: controlNS}
	r.Exec = rec.exec
	ctx := context.Background()
	ns := ids.WorkloadNamespace("shop")
	uid, _ := r.UIDs.Ensure(ctx, "u-aaaa1111")
	if _, err := r.ensureUserRuntime(ctx, testCell(), ns, uid); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	_ = c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.UserRuntimePod(uid)}, &pod)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "runtime", Ready: true}}
	_ = c.Status().Update(ctx, &pod)
	for _, n := range []string{"sess-old", "sess-live"} {
		var sx acv1.Session
		_ = c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: n}, &sx)
		sx.Status.PodName = ids.UserRuntimePod(uid)
		_ = c.Status().Update(ctx, &sx)
	}

	if _, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "sess-old"}}); err != nil {
		t.Fatal(err)
	}
	// The runtime survives...
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.UserRuntimePod(uid)}, &pod); err != nil {
		t.Fatalf("a TTL expiry deleted the shared runtime: %v", err)
	}
	// ...and the expired session's window was closed instead.
	closed := false
	for _, call := range rec.calls {
		if len(call.argv) > 2 && call.argv[1] == "window-close" && call.argv[2] == "old" {
			closed = true
		}
	}
	if !closed {
		t.Errorf("the expired session's window was not closed: %v", rec.calls)
	}
	// The other session is untouched.
	var other acv1.Session
	_ = c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "sess-live"}, &other)
	if other.Status.Phase != acv1.SessionRunning {
		t.Errorf("the sibling session became %s", other.Status.Phase)
	}
}

// The window is the session. A runtime that answers exec may have lost it —
// the owner closed it, or the container restarted while the pod stayed —
// and reporting on the pod called all of that Running.
func TestClosedWindowSettlesTheSession(t *testing.T) {
	s := residentSession("sess-a", "u-aaaa1111", "work")
	s.Status.Phase = acv1.SessionRunning
	s.Status.SessionID = "a"
	// PodName is set after allocation, below.
	now := metav1.Now()
	s.Status.StartTime = &now
	c := newFake(t, testCell(), credSecret("bailian-key"), s)
	r := sessionReconciler(t, c)
	r.UIDs = &useruid.Allocator{Client: c, Namespace: controlNS}
	// The runtime is up, but this session's window is gone.
	r.Exec = func(_ context.Context, _, _ string, argv []string, _ io.Reader) (string, error) {
		if len(argv) > 1 && argv[1] == "window-status" {
			return "alive=false exit=-\n", nil
		}
		return "", nil
	}
	ctx := context.Background()
	ns := ids.WorkloadNamespace("shop")
	uid, _ := r.UIDs.Ensure(ctx, "u-aaaa1111")
	if _, err := r.ensureUserRuntime(ctx, testCell(), ns, uid); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	_ = c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.UserRuntimePod(uid)}, &pod)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "runtime", Ready: true}}
	_ = c.Status().Update(ctx, &pod)
	var sx acv1.Session
	_ = c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "sess-a"}, &sx)
	sx.Status.PodName = ids.UserRuntimePod(uid)
	_ = c.Status().Update(ctx, &sx)
	if _, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "sess-a"}}); err != nil {
		t.Fatal(err)
	}
	var got acv1.Session
	_ = c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "sess-a"}, &got)
	// The window comes back. A tmux window can vanish because the CLI
	// exited, because somebody typed exit, or because the runtime was
	// replaced — none of which is the owner saying they are done with the
	// project, and all of which used to end it.
	if got.Status.Phase == acv1.SessionSettling {
		t.Error("a closed window ended the project instead of rebuilding the terminal")
	}
}

// An exec that fails is not proof the window died. Settling on a transient
// API hiccup would destroy exactly what a resident session exists to keep.
func TestTransientExecFailureDoesNotSettle(t *testing.T) {
	s := residentSession("sess-a", "u-aaaa1111", "work")
	s.Status.Phase = acv1.SessionRunning
	s.Status.SessionID = "a"
	// PodName is set after allocation, below.
	now := metav1.Now()
	s.Status.StartTime = &now
	c := newFake(t, testCell(), credSecret("bailian-key"), s)
	r := sessionReconciler(t, c)
	r.UIDs = &useruid.Allocator{Client: c, Namespace: controlNS}
	r.Exec = func(_ context.Context, _, _ string, _ []string, _ io.Reader) (string, error) {
		return "", errors.New("etcdserver: request timed out")
	}
	ctx := context.Background()
	ns := ids.WorkloadNamespace("shop")
	uid, _ := r.UIDs.Ensure(ctx, "u-aaaa1111")
	_, _ = r.ensureUserRuntime(ctx, testCell(), ns, uid)
	var pod corev1.Pod
	_ = c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.UserRuntimePod(uid)}, &pod)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "runtime", Ready: true}}
	_ = c.Status().Update(ctx, &pod)
	var sx acv1.Session
	_ = c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "sess-a"}, &sx)
	sx.Status.PodName = ids.UserRuntimePod(uid)
	_ = c.Status().Update(ctx, &sx)
	if _, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "sess-a"}}); err != nil {
		t.Fatal(err)
	}
	var got acv1.Session
	_ = c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "sess-a"}, &got)
	if got.Status.Phase != acv1.SessionRunning {
		t.Errorf("a failed exec settled the session (%s)", got.Status.Phase)
	}
}

// Two of a user's Codex sessions share a runtime and a $HOME, so "resume the
// last conversation" would resume into each other. Each session gets its own
// state directory to make "last" mean this one.
func TestRecencyResumingRunnersGetTheirOwnStateDir(t *testing.T) {
	s := residentSession("sess-a", "u-aaaa1111", "work")
	s.Spec.Runner = "codex"
	s.Spec.Provider = "deepseek"
	c := newFake(t, testCell(), credSecret("bailian-key"), s)
	rec := &recorder{}
	r := sessionReconciler(t, c)
	r.UIDs = &useruid.Allocator{Client: c, Namespace: controlNS}
	r.Exec = rec.exec
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "sess-a"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	uid, _ := r.UIDs.Ensure(ctx, "u-aaaa1111")
	ns := ids.WorkloadNamespace("shop")
	var pod corev1.Pod
	_ = c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.UserRuntimePod(uid)}, &pod)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "runtime", Ready: true}}
	_ = c.Status().Update(ctx, &pod)
	for range 2 {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	var open *struct {
		pod   string
		argv  []string
		stdin string
	}
	for i := range rec.calls {
		if len(rec.calls[i].argv) > 1 && rec.calls[i].argv[1] == "window-open" {
			open = &rec.calls[i]
		}
	}
	if open == nil {
		t.Fatal("no window opened")
	}
	var got acv1.Session
	_ = c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "sess-a"}, &got)
	var env map[string]string
	if err := json.Unmarshal([]byte(open.stdin), &env); err != nil {
		t.Fatalf("stdin is not a JSON object: %v", err)
	}
	want := ids.SessionStateDir(uid, got.Status.SessionID)
	if env["CODEX_HOME"] != want {
		t.Errorf("window state dir = %q, want %q", env["CODEX_HOME"], want)
	}
}

// A briefing is prose. Line-oriented framing forced a choice between
// mangling a task that contains a newline and refusing it outright — and
// refusing was wrong, because the console offers a multi-line box.
func TestMultilineTaskCrossesTheExecChannel(t *testing.T) {
	task := "两列布局\n第一行:间距加大\n第二行:标题=\"促销\"; 不要动价格"
	s := residentSession("sess-a", "u-aaaa1111", task)
	cellCR := testCell()
	cellCR.Spec.Description = "极简电商\n面向移动端"
	c := newFake(t, cellCR, credSecret("bailian-key"), s)
	rec := &recorder{}
	r := sessionReconciler(t, c)
	r.UIDs = &useruid.Allocator{Client: c, Namespace: controlNS}
	r.Exec = rec.exec
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "sess-a"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("a multi-line task was refused: %v", err)
	}
	uid, _ := r.UIDs.Ensure(ctx, "u-aaaa1111")
	var pod corev1.Pod
	_ = c.Get(ctx, types.NamespacedName{
		Namespace: ids.WorkloadNamespace("shop"), Name: ids.UserRuntimePod(uid)}, &pod)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "runtime", Ready: true}}
	_ = c.Status().Update(ctx, &pod)
	for range 2 {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	var open *struct {
		pod   string
		argv  []string
		stdin string
	}
	for i := range rec.calls {
		if len(rec.calls[i].argv) > 1 && rec.calls[i].argv[1] == "window-open" {
			open = &rec.calls[i]
		}
	}
	if open == nil {
		t.Fatal("no window opened")
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(open.stdin), &got); err != nil {
		t.Fatalf("stdin is not a JSON object: %v\n%s", err, open.stdin)
	}
	// Byte-for-byte, newlines and quotes included.
	if got["AGENTCELL_TASK"] != task {
		t.Errorf("task did not survive:\n got %q\nwant %q", got["AGENTCELL_TASK"], task)
	}
	if got["AGENTCELL_DESCRIPTION"] != cellCR.Spec.Description {
		t.Errorf("description did not survive: %q", got["AGENTCELL_DESCRIPTION"])
	}
	// And a newline in the prose cannot forge another variable.
	if _, forged := got["第一行:间距加大"]; forged {
		t.Error("a line of the task became an environment variable")
	}
	if got["AGENTCELL_API_KEY"] != "sk-test" {
		t.Error("the credential no longer reaches the window")
	}
}

// A tmux server dies with its container, so a restart takes every window
// with it — which looks exactly like the owner closing one. Settling on that
// destroys a session its owner never ended, and the difference is knowable:
// the container is a different one.
func TestRuntimeRestartRecoversInsteadOfSettling(t *testing.T) {
	s := residentSession("sess-a", "u-aaaa1111", "work")
	s.Status.Phase = acv1.SessionRunning
	s.Status.SessionID = "a"
	s.Status.RuntimeInstance = "containerd://OLD"
	now := metav1.Now()
	s.Status.StartTime = &now
	c := newFake(t, testCell(), credSecret("bailian-key"), s)
	rec := &recorder{}
	r := sessionReconciler(t, c)
	r.UIDs = &useruid.Allocator{Client: c, Namespace: controlNS}
	// The window is gone — as it always is after a restart.
	r.Exec = func(ctx context.Context, ns, pod string, argv []string, stdin io.Reader) (string, error) {
		if len(argv) > 1 && argv[1] == "window-status" {
			return "alive=false exit=-\n", nil
		}
		return rec.exec(ctx, ns, pod, argv, stdin)
	}
	ctx := context.Background()
	ns := ids.WorkloadNamespace("shop")
	uid, _ := r.UIDs.Ensure(ctx, "u-aaaa1111")
	if _, err := r.ensureUserRuntime(ctx, testCell(), ns, uid); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	_ = c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.UserRuntimePod(uid)}, &pod)
	// A NEW container: same pod, restarted.
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "runtime", Ready: true, ContainerID: "containerd://NEW"},
	}
	_ = c.Status().Update(ctx, &pod)
	var sx acv1.Session
	_ = c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "sess-a"}, &sx)
	sx.Status.PodName = ids.UserRuntimePod(uid)
	_ = c.Status().Update(ctx, &sx)

	if _, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "sess-a"}}); err != nil {
		t.Fatal(err)
	}
	var got acv1.Session
	_ = c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "sess-a"}, &got)
	if got.Status.Phase == acv1.SessionSettling || got.Status.Phase == acv1.SessionSettled {
		t.Fatalf("a runtime restart settled the session (%s)", got.Status.Phase)
	}
	restored := false
	for _, call := range rec.calls {
		if len(call.argv) < 2 || call.argv[1] != "window-open" {
			continue
		}
		joined := strings.Join(call.argv, " ")
		if strings.Contains(joined, "-restore") || strings.Contains(joined, "--continue") {
			restored = true
		}
	}
	if !restored {
		t.Errorf("the window was not restored after the restart: %v", rec.calls)
	}
	if got.Status.RuntimeInstance != "containerd://NEW" {
		t.Errorf("instance = %q; the next restart would not be recognisable", got.Status.RuntimeInstance)
	}
}

// The other half: same container, no window, means somebody closed it — and
// that IS the signal to settle. Without this the two cases would merge and
// closing a window would leave a session running forever.
func TestClosedWindowInTheSameContainerStillSettles(t *testing.T) {
	s := residentSession("sess-a", "u-aaaa1111", "work")
	s.Status.Phase = acv1.SessionRunning
	s.Status.SessionID = "a"
	s.Status.RuntimeInstance = "containerd://SAME"
	now := metav1.Now()
	s.Status.StartTime = &now
	c := newFake(t, testCell(), credSecret("bailian-key"), s)
	r := sessionReconciler(t, c)
	r.UIDs = &useruid.Allocator{Client: c, Namespace: controlNS}
	r.Exec = func(_ context.Context, _, _ string, argv []string, _ io.Reader) (string, error) {
		if len(argv) > 1 && argv[1] == "window-status" {
			return "alive=false exit=0\n", nil
		}
		return "", nil
	}
	ctx := context.Background()
	ns := ids.WorkloadNamespace("shop")
	uid, _ := r.UIDs.Ensure(ctx, "u-aaaa1111")
	_, _ = r.ensureUserRuntime(ctx, testCell(), ns, uid)
	var pod corev1.Pod
	_ = c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.UserRuntimePod(uid)}, &pod)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "runtime", Ready: true, ContainerID: "containerd://SAME"},
	}
	_ = c.Status().Update(ctx, &pod)
	var sx acv1.Session
	_ = c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "sess-a"}, &sx)
	sx.Status.PodName = ids.UserRuntimePod(uid)
	_ = c.Status().Update(ctx, &sx)

	if _, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "sess-a"}}); err != nil {
		t.Fatal(err)
	}
	var got acv1.Session
	_ = c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "sess-a"}, &got)
	if got.Status.Phase == acv1.SessionSettling {
		t.Error("a closed window ended the project; it should have got its terminal back")
	}
}

// A resident session idles out, it does not age out. Killing one at the
// deadline while its agent is mid-run would be the platform interrupting the
// work it exists to host.
// A resident session is reclaimed on IDLE, not on age — and reclaiming it
// means it stops holding compute, not that its work is published and its
// session ended. See dormant_test.go for why those had to stop being the
// same decision.
func TestResidentSessionIdlesOutRatherThanAgesOut(t *testing.T) {
	longAgo := metav1.NewTime(metav1.Now().Add(-3 * time.Hour))
	recent := metav1.Now()

	for _, tc := range []struct {
		name        string
		working     bool
		activity    metav1.Time
		wantDormant bool
	}{
		{"old session, agent still working, stays", true, longAgo, false},
		{"old session, recent activity, stays", false, recent, false},
		{"old session, idle for hours, sleeps", false, longAgo, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := residentSession("sess-a", "u-aaaa1111", "work")
			s.Status.Phase = acv1.SessionRunning
			s.Status.SessionID = "a"
			s.Status.RuntimeInstance = "containerd://SAME"
			started := metav1.NewTime(metav1.Now().Add(-5 * time.Hour))
			s.Status.StartTime = &started
			act := tc.activity
			s.Status.LastActivity = &act
			c := newFake(t, testCell(), credSecret("bailian-key"), s)
			r := sessionReconciler(t, c)
			r.UIDs = &useruid.Allocator{Client: c, Namespace: controlNS}
			// An interactive agent never exits, so "working" is expressed
			// the way the runtime actually reports it: how long the window
			// has been quiet.
			exit, quiet := "0", "3600"
			if tc.working {
				exit, quiet = "-", "2"
			}
			r.Exec = func(_ context.Context, _, _ string, argv []string, _ io.Reader) (string, error) {
				if len(argv) > 1 && argv[1] == "window-status" {
					return "alive=true exit=" + exit + " quiet=" + quiet + "\n", nil
				}
				return "", nil
			}
			ctx := context.Background()
			ns := ids.WorkloadNamespace("shop")
			uid, _ := r.UIDs.Ensure(ctx, "u-aaaa1111")
			_, _ = r.ensureUserRuntime(ctx, testCell(), ns, uid)
			var pod corev1.Pod
			_ = c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.UserRuntimePod(uid)}, &pod)
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{Name: "runtime", Ready: true, ContainerID: "containerd://SAME"},
			}
			_ = c.Status().Update(ctx, &pod)
			var sx acv1.Session
			_ = c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "sess-a"}, &sx)
			sx.Status.PodName = ids.UserRuntimePod(uid)
			_ = c.Status().Update(ctx, &sx)

			if _, err := r.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "sess-a"}}); err != nil {
				t.Fatal(err)
			}
			var got acv1.Session
			_ = c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "sess-a"}, &got)
			dormant := got.Status.Phase == acv1.SessionDormant
			if dormant != tc.wantDormant {
				t.Errorf("phase = %s, dormant=%v want %v", got.Status.Phase, dormant, tc.wantDormant)
			}
			if got.Status.Phase == acv1.SessionSettling || got.Status.Phase == acv1.SessionSettled {
				t.Error("an idle session was published and ended rather than put to sleep")
			}
		})
	}
}

// Opening a project with nothing to say gives you a terminal, not an agent
// run with an empty prompt.
//
// Seen in a real session: 打开项目 produced `kimi --continue -p ”`, which
// starts a conversation nobody began, spends the person's quota on it, and
// leaves a transcript whose first turn is blank. The window still gets its
// full environment, so whatever they type next runs exactly as it would
// have.
func TestOpeningWithNoTaskStartsNoAgent(t *testing.T) {
	s := residentSession("sess-a", "u-aaaa1111", "")
	c := newFake(t, testCell(), credSecret("bailian-key"), s)
	rec := &recorder{}
	r := sessionReconciler(t, c)
	r.UIDs = &useruid.Allocator{Client: c, Namespace: controlNS}
	r.Exec = rec.exec
	ctx := context.Background()
	ns := ids.WorkloadNamespace("shop")
	uid, _ := r.UIDs.Ensure(ctx, "u-aaaa1111")
	if _, err := r.ensureUserRuntime(ctx, testCell(), ns, uid); err != nil {
		t.Fatal(err)
	}
	binding, err := r.Registry.Resolve(s.Spec.Runner, s.Spec.Provider, s.Spec.Model)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.openWindow(ctx, s, testCell(), ns, "a", uid, binding,
		[]string{"kimi", "-p", ""}); err != nil {
		t.Fatal(err)
	}

	opened := false
	for _, call := range rec.calls {
		if len(call.argv) < 2 || call.argv[1] != "window-open" {
			continue
		}
		opened = true
		joined := strings.Join(call.argv, " ")
		// The interactive form, and nothing that looks like a prompt: an
		// empty -p is the exact shape this test exists to keep out.
		if !strings.Contains(joined, "claude") {
			t.Errorf("the agent was not started at all: %v", call.argv)
		}
		if strings.Contains(joined, " -p") {
			t.Errorf("a session with no task was started in print mode: %v", call.argv)
		}
	}
	if !opened {
		t.Fatalf("no window was opened at all: %v", rec.calls)
	}
	// And nothing was typed at it — there was nothing to say.
	for _, call := range rec.calls {
		if len(call.argv) > 2 && call.argv[1] == "tell" {
			t.Errorf("something was said to an agent nobody spoke to: %v", call.argv)
		}
	}
}

// A rebuilt runtime must come back with the agent in it, on the same
// conversation — not with an empty shell.
//
// Reported as "重启工作台以后啥都没有了". Restoring used to start nothing,
// which was right when the agent was a one-shot command that had already
// finished. With an interactive agent the terminal IS the agent, so a
// restore without it is a blank screen beside a worktree full of work.
func TestRestoringBringsTheAgentBackOnTheSameConversation(t *testing.T) {
	s := residentSession("sess-a", "u-aaaa1111", "some earlier task")
	c := newFake(t, testCell(), credSecret("bailian-key"), s)
	rec := &recorder{}
	r := sessionReconciler(t, c)
	r.UIDs = &useruid.Allocator{Client: c, Namespace: controlNS}
	r.Exec = rec.exec
	ctx := context.Background()
	ns := ids.WorkloadNamespace("shop")
	uid, _ := r.UIDs.Ensure(ctx, "u-aaaa1111")
	if _, err := r.ensureUserRuntime(ctx, testCell(), ns, uid); err != nil {
		t.Fatal(err)
	}
	binding, err := r.Registry.Resolve(s.Spec.Runner, s.Spec.Provider, s.Spec.Model)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.restoreWindow(ctx, s, testCell(), ns, "a", uid, binding); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range rec.calls {
		if len(call.argv) < 2 || call.argv[1] != "window-open" {
			continue
		}
		found = true
		joined := strings.Join(call.argv, " ")
		if !strings.Contains(joined, "claude") {
			t.Errorf("restored without the agent: %v", call.argv)
		}
		if !strings.Contains(joined, "--continue") {
			t.Errorf("restored on a fresh conversation instead of the one that was here: %v", call.argv)
		}
		// And never the task again: the work was done once already.
		if strings.Contains(joined, "some earlier task") {
			t.Errorf("restoring re-ran the original task: %v", call.argv)
		}
	}
	if !found {
		t.Fatalf("no window was opened: %v", rec.calls)
	}
}
