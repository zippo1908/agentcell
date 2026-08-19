package identity

import "testing"

// An allocated id wins over the derived one.
//
// This is the seam the whole redesign turns on: authentication resolves a
// login to the principal it is bound to, and everything downstream — Cell
// membership, Secret ownership, session ownership, the Unix uid — reads
// ID(). If the derived value ever won, a person who has linked a second
// login would act as a different entity depending on how they signed in,
// which is the exact failure the change exists to remove.
func TestAnAllocatedIDWinsOverTheDerivedOne(t *testing.T) {
	p := Principal{Subject: "user:zhu@tinci.com", Kind: KindUser}
	derived := p.ID()
	if derived == "" {
		t.Fatal("a principal with a subject has no derived id")
	}

	allocated := p.WithID("u-0123456789abcdef")
	if got := allocated.ID(); got != "u-0123456789abcdef" {
		t.Fatalf("ID() = %q, want the allocated value", got)
	}
	if !allocated.HasAllocatedID() {
		t.Error("HasAllocatedID is false on a principal that has one")
	}
	// And the original is untouched: WithID returns a copy, so a resolved
	// principal cannot leak back into one that was never resolved.
	if p.ID() != derived || p.HasAllocatedID() {
		t.Error("WithID mutated its receiver")
	}
}

// Nothing resolved: fall back to hashing the subject, exactly as before.
//
// This is what keeps a deployment with no account store working, and what
// makes the rollout a no-op for everybody who already had an account —
// their allocated id was adopted from this same value.
func TestAnUnresolvedPrincipalStillDerives(t *testing.T) {
	p := Principal{Subject: "user:a@b.c", Kind: KindUser}
	if p.HasAllocatedID() {
		t.Fatal("a principal nothing resolved claims an allocated id")
	}
	if got := p.ID(); got == "" || got[:2] != "u-" {
		t.Fatalf("ID() = %q, want the derived form", got)
	}
	// Deterministic, because objects already in the cluster are keyed to it.
	if p.ID() != (Principal{Subject: "user:a@b.c"}).ID() {
		t.Error("the derived id is no longer stable for a given subject")
	}
}

// Ownership follows the allocated id.
func TestOwnsUsesTheAllocatedID(t *testing.T) {
	p := Principal{Subject: "user:a@b.c", Kind: KindUser}.WithID("u-aaaaaaaaaaaaaaaa")
	if !p.Owns("u-aaaaaaaaaaaaaaaa") {
		t.Error("a principal does not own an object labelled with its allocated id")
	}
	if p.Owns((Principal{Subject: "user:a@b.c"}).ID()) {
		t.Error("a resolved principal still owns objects keyed to its DERIVED id — " +
			"which would mean two ids for one person, the thing this replaced")
	}
}

// The zero principal has no id at all, allocated or otherwise.
func TestAnUnauthenticatedPrincipalHasNoID(t *testing.T) {
	if got := (Principal{}).ID(); got != "" {
		t.Fatalf("the zero principal has id %q", got)
	}
}

// Provider is what a binding is keyed by, and it must match the Kind
// verbatim — the store looks up (provider, subject) with these exact values.
func TestProviderMatchesKind(t *testing.T) {
	for _, k := range []Kind{KindUser, KindOIDC, KindToken} {
		if got := (Principal{Kind: k}).Provider(); got != string(k) {
			t.Errorf("Provider() = %q for kind %q", got, k)
		}
	}
}
