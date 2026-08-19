package webui

import (
	"context"

	"github.com/zippo1908/agentcell/internal/identity"
	"github.com/zippo1908/agentcell/internal/store"
)

// principalIDFor answers "which principal is this address" — once.
//
// It used to be answered by hashing, in seven places: the member list, adding
// a member, resolving a member, three mention paths on the board, and
// lending a credential. That was correct while a principal id WAS the hash
// of its subject. It stopped being correct when the id became something
// allocated and stored (ADR-0016), and the two answers still agree today
// only because every existing principal ADOPTED its derived id.
//
// They stop agreeing the moment anybody holds an id that was allocated
// rather than adopted — which is what happens as soon as an enterprise IdP
// is connected, since a person's first SSO login mints a fresh one. On that
// day, hashing would name a principal that does not exist: a credential lent
// to nobody, a member added who never appears, an @ that reaches no one.
// None of it would raise an error, because "no such principal" and "a
// principal with nothing" look identical from the outside.
//
// So: one function. The binding table is the answer; hashing is the fallback
// for a deployment with no account store, where it is also still correct.
func principalIDFor(ctx context.Context, db *store.DB, email string) string {
	subject := identity.UserSubject(email)
	derived := identity.Principal{Subject: subject}.ID()
	if db == nil {
		return derived
	}
	id, err := db.PrincipalFor(ctx, string(identity.KindUser), subject)
	if err != nil || id == "" {
		// No binding yet: an account that predates the backfill, or a
		// deployment mid-upgrade. The derived id is what that person's
		// objects are keyed to, which is the right answer for them.
		return derived
	}
	return id
}
