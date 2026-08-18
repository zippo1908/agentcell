package webui

import (
	corev1 "k8s.io/api/core/v1"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
)

func pendingFixture(t *testing.T) (*Handler, *acv1.Session) {
	t.Helper()
	s := &acv1.Session{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "sess-1"}}
	s.Spec.Cell = "shop"
	s.Spec.OwnerUserID = alice.ID()
	s.Status.Phase = acv1.SessionDormant
	h := &Handler{
		Client:    fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(s).Build(),
		Namespace: ns,
	}
	return h, s
}

// Saying three things in a row must deliver three things, in order.
//
// The queue was a single string, so the ordinary case — type, think, type
// again before the first was delivered — overwrote the first instruction.
// Nobody was told: the sentence simply never happened.
func TestRapidFollowUpsAreAllKeptInOrder(t *testing.T) {
	h, s := pendingFixture(t)
	for _, msg := range []string{"第一句", "第二句", "第三句"} {
		if err := h.queueFollowUp(t.Context(), s, msg); err != nil {
			t.Fatal(err)
		}
	}
	var got acv1.Session
	if err := h.Client.Get(t.Context(), types.NamespacedName{Namespace: ns, Name: "sess-1"}, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"第一句", "第二句", "第三句"}
	if len(got.Spec.PendingTasks) != len(want) {
		t.Fatalf("queue = %v, want %v", got.Spec.PendingTasks, want)
	}
	for i := range want {
		if got.Spec.PendingTasks[i] != want[i] {
			t.Errorf("queue[%d] = %q, want %q (order matters: instructions build on each other)",
				i, got.Spec.PendingTasks[i], want[i])
		}
	}
}

// Two people typing at the same moment is normal. Neither may lose their
// sentence to the other's write.
func TestConcurrentFollowUpsDoNotOverwriteEachOther(t *testing.T) {
	h, s := pendingFixture(t)
	var wg sync.WaitGroup
	for _, msg := range []string{"甲说的", "乙说的"} {
		wg.Add(1)
		go func(m string) {
			defer wg.Done()
			// Each caller holds its own copy, exactly as two HTTP handlers
			// would.
			var own acv1.Session
			if err := h.Client.Get(t.Context(),
				types.NamespacedName{Namespace: ns, Name: s.Name}, &own); err != nil {
				return
			}
			_ = h.queueFollowUp(t.Context(), &own, m)
		}(msg)
	}
	wg.Wait()

	var got acv1.Session
	if err := h.Client.Get(t.Context(), types.NamespacedName{Namespace: ns, Name: "sess-1"}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.PendingTasks) != 2 {
		t.Errorf("queue = %v; one caller's instruction was lost to the other's write",
			got.Spec.PendingTasks)
	}
}

// An instruction a session was already holding in the old single-slot field
// must survive the upgrade — losing it would be the very failure the queue
// was introduced to end.
func TestLegacyPendingTaskIsNotLost(t *testing.T) {
	h, s := pendingFixture(t)
	s.Spec.PendingTask = "升级前就在等的那句"
	if err := h.Client.Update(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if err := h.queueFollowUp(t.Context(), s, "升级后新说的"); err != nil {
		t.Fatal(err)
	}
	var got acv1.Session
	if err := h.Client.Get(t.Context(), types.NamespacedName{Namespace: ns, Name: "sess-1"}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.PendingTasks) != 2 || got.Spec.PendingTasks[0] != "升级前就在等的那句" {
		t.Errorf("queue = %v; the instruction held before the upgrade was dropped", got.Spec.PendingTasks)
	}
	if got.Spec.PendingTask != "" {
		t.Error("the legacy field was left set, so it will be delivered twice")
	}
}

// A custom idle window must reach the object. The API accepted the field and
// discarded it: somebody asking for a longer window watched their session
// sleep on the default anyway, with nothing saying their setting was ignored.
func TestIdleSecondsIsPersisted(t *testing.T) {
	key := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: "k",
		Labels: map[string]string{credLabel: credKindModel, OwnerLabel: alice.ID()},
	}, Data: map[string][]byte{"key": []byte("sk-x")}}
	h := &Handler{
		Client: fake.NewClientBuilder().WithScheme(testScheme(t)).
			WithObjects(openCell("shop"), key).Build(),
		Namespace: ns,
		Registry:  testRegistry(t),
	}
	body := `{"task":"做点事","runner":"claude","provider":"anthropic","model":"claude-x","idleSeconds":7200,"credentialSecret":"k"}`
	req := asUser(httptest.NewRequest(http.MethodPost, "/api/cells/shop/dispatch",
		strings.NewReader(body)), alice)
	req.SetPathValue("cell", "shop")
	rec := httptest.NewRecorder()
	h.dispatch(rec, req)

	var list acv1.SessionList
	if err := h.Client.List(t.Context(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) == 0 {
		// Never skip here: a skipped assertion looks like a pass and this
		// is the whole point of the test.
		t.Fatalf("dispatch created no session (%d): %s", rec.Code, rec.Body)
	}
	if got := list.Items[0].Spec.IdleSeconds; got != 7200 {
		t.Errorf("idleSeconds = %d, want 7200 — the API accepted it and dropped it", got)
	}
}

// Two dispatches at the same instant must not produce two live sessions.
//
// The old check was a list followed by a create: both callers looked, both
// saw nothing, and both created — leaving one person with two sessions in
// one project, two worktrees, and a slot spent on a conversation nobody
// asked for. Looking is not claiming.
func TestConcurrentDispatchClaimsOnlyOneSession(t *testing.T) {
	h := &Handler{
		Client:    fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(openCell("shop")).Build(),
		Namespace: ns,
	}
	var wg sync.WaitGroup
	won := make([]bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w, _, err := h.claimLiveSession(t.Context(), "shop", alice.ID())
			if err == nil {
				won[i] = w
			}
		}(i)
	}
	wg.Wait()

	if won[0] && won[1] {
		t.Error("both callers won the claim; each would create its own session")
	}
	if !won[0] && !won[1] {
		t.Error("neither caller won, so nobody would create the session either")
	}
}
