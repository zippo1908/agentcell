package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/identity"
)

// putMemberAs runs the handler as somebody, against a Cell named "shop".
func putMemberAs(t *testing.T, h *Handler, as identity.Principal, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := asUser(httptest.NewRequest(http.MethodPut, "/api/cells/shop/members", strings.NewReader(body)), as)
	req.SetPathValue("cell", "shop")
	rec := httptest.NewRecorder()
	h.putMember(rec, req)
	return rec
}

func cellNamed(t *testing.T, h *Handler) *acv1.Cell {
	t.Helper()
	var cell acv1.Cell
	if err := h.Client.Get(t.Context(), types.NamespacedName{Namespace: ns, Name: "shop"}, &cell); err != nil {
		t.Fatal(err)
	}
	return &cell
}

// An address must never be stored as if it were a user id.
//
// This happened: the console sent people's addresses through the `userID`
// field, they were written verbatim, and the authorization check compares
// against a HASHED id — so the member list read correctly and granted
// nothing at all. And because naming a member closes the project, adding two
// colleagues made the project vanish for everybody, the person who added
// them included.
func TestAnAddressIsResolvedNotStoredVerbatim(t *testing.T) {
	h := lendingFixture(t)
	ctx := t.Context()
	for _, e := range []string{"boss@tinci.com", "li@tinci.com"} {
		if err := h.Auth.Accounts.DB.CreateUser(ctx, idOf(e), e, "", "h", false, false); err != nil {
			t.Fatal(err)
		}
	}
	boss := identity.Principal{Subject: identity.UserSubject("boss@tinci.com"), Kind: identity.KindUser}

	// The shape the console used to send.
	if rec := putMemberAs(t, h, boss, `{"userID":"li@tinci.com","role":"member"}`); rec.Code != 200 {
		t.Fatalf("put = %d: %s", rec.Code, rec.Body)
	}

	cell := cellNamed(t, h)
	for _, m := range cell.Spec.Members {
		if strings.Contains(m.UserID, "@") {
			t.Fatalf("an address was stored as an id: %q — it can never match a principal", m.UserID)
		}
	}
	if !isMember(cell.Spec.Members, idOf("li@tinci.com")) {
		t.Error("the address did not resolve to the person it names")
	}
}

// Closing a project must not lock out the person closing it.
//
// On an open project everybody is a maintainer, so whoever adds the first
// member is spending access they are about to withdraw from themselves. The
// way back needs cluster access — which is exactly what this API exists to
// avoid needing.
func TestAddingTheFirstMemberDoesNotLockOutTheAdder(t *testing.T) {
	h := lendingFixture(t)
	ctx := t.Context()
	for _, e := range []string{"boss@tinci.com", "li@tinci.com"} {
		if err := h.Auth.Accounts.DB.CreateUser(ctx, idOf(e), e, "", "h", false, false); err != nil {
			t.Fatal(err)
		}
	}
	boss := identity.Principal{Subject: identity.UserSubject("boss@tinci.com"), Kind: identity.KindUser}

	if rec := putMemberAs(t, h, boss, `{"email":"li@tinci.com","role":"member"}`); rec.Code != 200 {
		t.Fatalf("put = %d: %s", rec.Code, rec.Body)
	}

	cell := cellNamed(t, h)
	if effectiveAccess(cell) != acv1.AccessRestricted {
		t.Fatal("naming a member should close the project")
	}
	// The whole point: the person who did it can still get in, as a
	// maintainer — otherwise nobody can undo it.
	if !can(boss, cell, ActionSettings) {
		t.Fatalf("the adder lost access to the project they just closed: %v", cell.Spec.Members)
	}
}

// Adding to an ALREADY closed project must not quietly grant the adder
// anything — they are only there because they already were.
func TestAddingToAClosedProjectDoesNotAddTheAdder(t *testing.T) {
	h := lendingFixture(t)
	ctx := t.Context()
	for _, e := range []string{"boss@tinci.com", "li@tinci.com", "wang@tinci.com"} {
		if err := h.Auth.Accounts.DB.CreateUser(ctx, idOf(e), e, "", "h", false, false); err != nil {
			t.Fatal(err)
		}
	}
	// Already restricted, with boss on it.
	cell := cellNamed(t, h)
	cell.Spec.Access = acv1.AccessRestricted
	cell.Spec.Members = []acv1.Member{{UserID: idOf("boss@tinci.com"), Role: acv1.RoleMaintainer}}
	if err := h.Client.Update(ctx, cell); err != nil {
		t.Fatal(err)
	}
	boss := identity.Principal{Subject: identity.UserSubject("boss@tinci.com"), Kind: identity.KindUser}

	if rec := putMemberAs(t, h, boss, `{"email":"li@tinci.com","role":"member"}`); rec.Code != 200 {
		t.Fatalf("put = %d: %s", rec.Code, rec.Body)
	}

	got := cellNamed(t, h)
	if len(got.Spec.Members) != 2 {
		t.Fatalf("want boss and li, got %v", got.Spec.Members)
	}
}
