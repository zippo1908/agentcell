package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zippo1908/agentcell/internal/identity"
	"github.com/zippo1908/agentcell/internal/store"
)

// lendingFixture is ownedFixture plus an accounts database, which is where
// grants live.
func lendingFixture(t *testing.T, objs ...client.Object) *Handler {
	t.Helper()
	_, h := ownedFixture(t, objs...)
	h.Auth = &Authenticator{Accounts: accountsFixture(t)}
	return h
}

func lend(t *testing.T, h *Handler, credential, granter, grantee string) {
	t.Helper()
	if err := h.Auth.Accounts.DB.CreateGrant(t.Context(), store.Grant{
		GranterID: granter, GranteeKind: store.GranteeUser,
		GranteeID: grantee, Credential: credential,
	}); err != nil {
		t.Fatal(err)
	}
}

// The point of lending: somebody who owns no key can still work.
//
// Before this, a colleague handed a project could not start anything in it —
// dispatch refused on the credential check before it even looked at the
// project, and the only ways out were "add your own key" or "connect your own
// account". Both fine; both a wall on somebody's first afternoon.
func TestALentKeyIsSpendable(t *testing.T) {
	h := lendingFixture(t, ownedCredential("alice-key", alice.ID()))
	lend(t, h, "alice-key", alice.ID(), bob.ID())

	if err := h.mayUseCredential(
		asUser(httptest.NewRequest(http.MethodGet, "/", nil), bob), "alice-key"); err != nil {
		t.Fatalf("a lent key must be spendable by the borrower: %v", err)
	}
}

// And only by the person it was lent to.
func TestALentKeyReachesNobodyElse(t *testing.T) {
	h := lendingFixture(t, ownedCredential("alice-key", alice.ID()))
	lend(t, h, "alice-key", alice.ID(), bob.ID())

	carol := identity.Principal{Subject: identity.UserSubject("carol@tinci.com"), Kind: identity.KindUser}
	if err := h.mayUseCredential(
		asUser(httptest.NewRequest(http.MethodGet, "/", nil), carol), "alice-key"); err == nil {
		t.Fatal("a grant to Bob must not let Carol spend it")
	}
}

// A borrower with nothing of their own gets the lent key chosen for them —
// this is what makes "开一个终端" work on day one.
func TestTheSoleKeyMayBeABorrowedOne(t *testing.T) {
	h := lendingFixture(t, ownedCredential("alice-key", alice.ID()))
	lend(t, h, "alice-key", alice.ID(), bob.ID())

	got, err := h.soleCredential(t.Context(), bob)
	if err != nil {
		t.Fatalf("a borrower with exactly one usable key must be able to dispatch: %v", err)
	}
	if got != "alice-key" {
		t.Fatalf("got %q, want the lent key", got)
	}
}

// A grant pointing at a Secret that has since been deleted is a dangling
// row, not a key. Offering it would produce a session that cannot reach a
// model and fails somewhere deeper, where the cause is no longer visible.
func TestAGrantForADeletedKeyIsNotOffered(t *testing.T) {
	h := lendingFixture(t) // no Secret at all
	lend(t, h, "alice-key", alice.ID(), bob.ID())

	if got := h.spendableCredentials(t.Context(), bob); len(got) != 0 {
		t.Fatalf("a grant naming nothing must not be spendable, got %v", got)
	}
}

// A connected OAuth account must not be lendable.
//
// Its refresh token rotates: using it mints a new one and invalidates the
// old, so two runtimes holding the same account kill each other's login.
// That is the bug internal/controller/account_sync.go exists to stop, and
// lending would reintroduce it deliberately.
func TestAConnectedAccountCannotBeLent(t *testing.T) {
	oauth := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: "alice-kimi",
		Labels: map[string]string{credLabel: credKindKimi, OwnerLabel: alice.ID()},
	}}
	h := lendingFixture(t, oauth)
	if err := h.Auth.Accounts.DB.CreateUser(t.Context(), bob.ID(), "bob@tinci.com", "Bob", "h", false, false); err != nil {
		t.Fatal(err)
	}

	body := `{"credential":"alice-kimi","email":"bob@tinci.com"}`
	req := asUser(httptest.NewRequest(http.MethodPost, "/api/me/grants", strings.NewReader(body)), alice)
	rec := httptest.NewRecorder()
	h.createGrant(rec, req)

	if rec.Code != 400 {
		t.Fatalf("lending a rotating account = %d, want a refusal: %s", rec.Code, rec.Body)
	}
	// The refusal has to explain, not just refuse: somebody asking this is
	// trying to solve a real problem and needs to be pointed somewhere.
	if !strings.Contains(rec.Body.String(), "轮换") {
		t.Errorf("the refusal does not say why: %s", rec.Body)
	}
}

// You cannot lend what is not yours.
func TestLendingSomebodyElsesKeyIsRefused(t *testing.T) {
	h := lendingFixture(t, ownedCredential("alice-key", alice.ID()))
	if err := h.Auth.Accounts.DB.CreateUser(t.Context(), "u-carol", "carol@tinci.com", "C", "h", false, false); err != nil {
		t.Fatal(err)
	}

	body := `{"credential":"alice-key","email":"carol@tinci.com"}`
	req := asUser(httptest.NewRequest(http.MethodPost, "/api/me/grants", strings.NewReader(body)), bob)
	rec := httptest.NewRecorder()
	h.createGrant(rec, req)

	if rec.Code != 404 {
		t.Fatalf("Bob lending Alice's key = %d, want 404: %s", rec.Code, rec.Body)
	}
}
