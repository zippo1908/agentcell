package webui

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func ticketClient(t *testing.T) client.Client {
	t.Helper()
	sc := runtime.NewScheme()
	if err := corev1.AddToScheme(sc); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(sc).Build()
}

// The bug leader election makes reachable: with the guard in one process,
// a ticket captured from a URL could be redeemed once on EVERY replica, and
// nothing anywhere would say so.
func TestATicketRedeemedOnOneReplicaIsRefusedOnTheOther(t *testing.T) {
	c := ticketClient(t)
	exp := time.Now().Add(time.Minute)
	replicaA := &sharedTickets{client: c, namespace: "agentcell-system"}
	replicaB := &sharedTickets{client: c, namespace: "agentcell-system"}

	if !replicaA.consume(context.Background(), "nonce-1", exp) {
		t.Fatal("first redemption refused")
	}
	if replicaB.consume(context.Background(), "nonce-1", exp) {
		t.Error("a second replica accepted a ticket that was already used — " +
			"single-use degraded to once-per-replica")
	}
}

func TestDistinctTicketsDoNotCollide(t *testing.T) {
	c := ticketClient(t)
	s := &sharedTickets{client: c, namespace: "agentcell-system"}
	exp := time.Now().Add(time.Minute)
	for _, n := range []string{"a", "b", "c"} {
		if !s.consume(context.Background(), n, exp) {
			t.Errorf("ticket %q refused although unused", n)
		}
	}
}

// The nonce is a capability. Anyone who can list ConfigMaps in the control
// namespace must not be able to read live tickets out of their names.
func TestTheNonceIsNotStoredInTheObjectName(t *testing.T) {
	c := ticketClient(t)
	s := &sharedTickets{client: c, namespace: "agentcell-system"}
	s.consume(context.Background(), "super-secret-nonce", time.Now().Add(time.Minute))

	var list corev1.ConfigMapList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("got %d records, want 1", len(list.Items))
	}
	if got := list.Items[0].Name; contains(got, "super-secret-nonce") {
		t.Errorf("the nonce is readable in the object name: %q", got)
	}
}

// Records have no TTL of their own, so without a sweeper they accumulate one
// per preview open, forever.
func TestSweepRemovesExpiredRecordsAndKeepsLiveOnes(t *testing.T) {
	c := ticketClient(t)
	s := &sharedTickets{client: c, namespace: "agentcell-system"}
	ctx := context.Background()
	s.consume(ctx, "dead", time.Now().Add(-time.Minute))
	s.consume(ctx, "alive", time.Now().Add(time.Hour))

	if err := s.sweep(ctx); err != nil {
		t.Fatal(err)
	}
	var list corev1.ConfigMapList
	if err := c.List(ctx, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("after sweep: %d records, want 1 (the live one)", len(list.Items))
	}
	// And the live one still blocks a replay.
	if s.consume(ctx, "alive", time.Now().Add(time.Hour)) {
		t.Error("sweep removed a record that was still guarding a live ticket")
	}
}

// No client wired (tests, and any deployment that never got one) must not
// mean "no guard at all" — it means the weaker, process-local one.
func TestWithoutAClientTheLocalGuardStillWorks(t *testing.T) {
	s := &sharedTickets{}
	exp := time.Now().Add(time.Minute)
	if !s.consume(context.Background(), "n", exp) {
		t.Fatal("first redemption refused")
	}
	if s.consume(context.Background(), "n", exp) {
		t.Error("replay accepted with the fallback guard")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
