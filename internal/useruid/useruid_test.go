package useruid

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const ns = "agentcell-system"

func newAllocator(t *testing.T, objs ...*corev1.ConfigMap) *Allocator {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	b := fake.NewClientBuilder().WithScheme(scheme)
	for _, o := range objs {
		b = b.WithObjects(o)
	}
	return &Allocator{Client: b.Build(), Namespace: ns}
}

func TestAllocationIsStableAndDistinct(t *testing.T) {
	a := newAllocator(t)
	ctx := context.Background()
	alice, err := a.Ensure(ctx, "u-aaaa1111")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := a.Ensure(ctx, "u-bbbb2222")
	if err != nil {
		t.Fatal(err)
	}
	if alice == bob {
		t.Fatalf("two users share uid %d", alice)
	}
	if alice < FirstUID || bob > LastUID {
		t.Fatalf("uids %d/%d outside the allocated range", alice, bob)
	}
	// Asking again must never move a user: their files are already theirs.
	for range 3 {
		again, err := a.Ensure(ctx, "u-aaaa1111")
		if err != nil || again != alice {
			t.Fatalf("uid drifted: %d -> %d (%v)", alice, again, err)
		}
	}
}

// A departed user keeps their number. Reissuing it would hand the next
// person every file the previous one left behind.
func TestUIDsAreNeverRecycled(t *testing.T) {
	a := newAllocator(t)
	ctx := context.Background()
	gone, _ := a.Ensure(ctx, "u-departed1")
	next, _ := a.Ensure(ctx, "u-newhire01")
	if next == gone {
		t.Fatalf("new user got the departed user's uid %d", gone)
	}
	var cm corev1.ConfigMap
	if err := a.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: ConfigMapName}, &cm); err != nil {
		t.Fatal(err)
	}
	if _, ok := cm.Data[prefixAlloc+"u-departed1"]; !ok {
		t.Error("the departed user's record was dropped; it is a tombstone, not a cache")
	}
}

// No identity provider means one principal, and the shared project identity
// — byte-for-byte the behaviour before this layer existed.
func TestNoOwnerKeepsTheProjectIdentity(t *testing.T) {
	a := newAllocator(t)
	uid, err := a.Ensure(context.Background(), "")
	if err != nil || uid != ProjectUID {
		t.Fatalf("uid = %d (%v), want the project uid %d", uid, err, ProjectUID)
	}
}

// A corrupted record must fail loudly. Falling through to "allocate a fresh
// one" would give the user a second identity while their files still belong
// to the first.
func TestCorruptRecordIsRefusedNotReallocated(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ConfigMapName},
		Data:       map[string]string{prefixAlloc + "u-aaaa1111": "not-a-number"},
	}
	a := newAllocator(t, cm)
	if _, err := a.Ensure(context.Background(), "u-aaaa1111"); err == nil {
		t.Fatal("a corrupt uid record was silently replaced")
	}
}

func TestExhaustionIsAnErrorNotAWrap(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ConfigMapName},
		Data:       map[string]string{keyNext: "165536"},
	}
	a := newAllocator(t, cm)
	_, err := a.Ensure(context.Background(), "u-aaaa1111")
	if err == nil {
		t.Fatal("allocation past the end of the range succeeded")
	}
}

func TestMalformedUserIDIsRejected(t *testing.T) {
	a := newAllocator(t)
	for _, bad := range []string{"next", "uid.u-aaaa1111", "u-../../etc", "U-AAAA", "u-a b"} {
		if _, err := a.Ensure(context.Background(), bad); err == nil {
			t.Errorf("user id %q was accepted", bad)
		}
	}
}
