package controller

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
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
	s.Spec.Resident = true
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
	if !strings.Contains(call.stdin, "AGENTCELL_TASK=work") {
		t.Errorf("the task text did not cross the exec channel: %q", call.stdin)
	}
}
