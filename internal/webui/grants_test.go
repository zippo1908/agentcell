package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

// Lending a connected account has to actually hand it over.
//
// A model key is chosen per dispatch, so a grant row is enough. An account is
// never chosen — the session controller looks for a Secret named after the
// session's owner — so without a copy under the borrower's name, a grant
// lends nothing at all and does so silently.
func TestLendingAnAccountPutsItUnderTheBorrowersName(t *testing.T) {
	oauth := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: "alice-kimi",
		Labels: map[string]string{credLabel: credKindKimi, OwnerLabel: alice.ID()},
	}}
	oauth.Data = map[string][]byte{kimiCredKey: []byte("packed-credential")}
	h := lendingFixture(t, oauth)
	// Created the way Redeem creates accounts: the stored id IS the one
	// derived from the address. A fixture that stores a different id builds
	// a state production never produces — and used to pass only because
	// nothing read the id column.
	if err := h.Auth.Accounts.DB.CreateUser(t.Context(), idOf("bob@tinci.com"), "bob@tinci.com", "Bob", "h", false, false); err != nil {
		t.Fatal(err)
	}

	req := asUser(httptest.NewRequest(http.MethodPost, "/api/me/grants",
		strings.NewReader(`{"credential":"alice-kimi","email":"bob@tinci.com"}`)), alice)
	rec := httptest.NewRecorder()
	h.createGrant(rec, req)
	if rec.Code != 201 {
		t.Fatalf("lend = %d, want 201: %s", rec.Code, rec.Body)
	}

	var got corev1.Secret
	// The id an account has is derived from its address — Redeem does the
	// same — so the borrower's Secret is named after that.
	name := strings.TrimPrefix(idOf("bob@tinci.com"), "u-") + KimiCredentialSuffix
	if err := h.Client.Get(t.Context(),
		types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
		t.Fatalf("no account under the borrower's name: %v", err)
	}
	if string(got.Data[kimiCredKey]) != "packed-credential" {
		t.Errorf("the borrower's copy is not the lender's credential: %q", got.Data[kimiCredKey])
	}
	// A shared login that eventually fails is diagnosed by knowing it was
	// shared, so the copy records who lent it.
	if got.Labels["agentcell.io/lent-by"] != alice.ID() {
		t.Errorf("a borrowed login must record who lent it, got %q", got.Labels["agentcell.io/lent-by"])
	}
}

// Somebody's own connected account must never be overwritten by a borrowed
// one: they would be logged out of their own login by a colleague's gift.
func TestLendingDoesNotClobberSomebodyElsesOwnLogin(t *testing.T) {
	lender := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: "alice-kimi",
		Labels: map[string]string{credLabel: credKindKimi, OwnerLabel: alice.ID()},
	}}
	lender.Data = map[string][]byte{kimiCredKey: []byte("alice-credential")}
	own := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: strings.TrimPrefix(bob.ID(), "u-") + KimiCredentialSuffix,
		Labels: map[string]string{credLabel: credKindKimi, OwnerLabel: bob.ID()},
	}}
	own.Data = map[string][]byte{kimiCredKey: []byte("bobs-own-credential")}
	h := lendingFixture(t, lender, own)
	// Created the way Redeem creates accounts: the stored id IS the one
	// derived from the address. A fixture that stores a different id builds
	// a state production never produces — and used to pass only because
	// nothing read the id column.
	if err := h.Auth.Accounts.DB.CreateUser(t.Context(), idOf("bob@tinci.com"), "bob@tinci.com", "Bob", "h", false, false); err != nil {
		t.Fatal(err)
	}

	req := asUser(httptest.NewRequest(http.MethodPost, "/api/me/grants",
		strings.NewReader(`{"credential":"alice-kimi","email":"bob@tinci.com","acknowledge":true}`)), alice)
	rec := httptest.NewRecorder()
	h.createGrant(rec, req)

	var got corev1.Secret
	if err := h.Client.Get(t.Context(), types.NamespacedName{Namespace: ns, Name: own.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if string(got.Data[kimiCredKey]) != "bobs-own-credential" {
		t.Fatal("Bob's own login was replaced by a borrowed one")
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

// Lending follows the BINDING table, not a hash of the address.
//
// This is the one that matters after ADR-0016. A principal's id is allocated
// and stored; hashing the address is only the fallback. The two agree for
// every account that existed before that change, because each adopted its
// derived id — so a regression here is invisible today and total the moment
// somebody's id was allocated rather than adopted, which is what a first SSO
// login produces.
//
// The failure mode is silent: the credential is lent to an id that resolves
// to nobody. No error anywhere, because "no such principal" and "a principal
// who owns nothing" look identical from outside.
func TestLendingFollowsTheBindingNotTheAddressHash(t *testing.T) {
	oauth := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: "alice-kimi",
		Labels: map[string]string{credLabel: credKindKimi, OwnerLabel: alice.ID()},
	}}
	oauth.Data = map[string][]byte{kimiCredKey: []byte("packed-credential")}
	h := lendingFixture(t, oauth)
	db := h.Auth.Accounts.DB

	// A borrower whose id was ALLOCATED, not adopted — the shape every
	// principal has once an IdP is connected.
	const allocated = "u-0f0f0f0f0f0f0f0f"
	if err := db.CreateUser(t.Context(), allocated, "carol@tinci.com", "Carol", "h", false, false); err != nil {
		t.Fatal(err)
	}
	if allocated == idOf("carol@tinci.com") {
		t.Fatal("the fixture is not testing anything: the allocated id equals the derived one")
	}

	req := asUser(httptest.NewRequest(http.MethodPost, "/api/me/grants",
		strings.NewReader(`{"credential":"alice-kimi","email":"carol@tinci.com"}`)), alice)
	rec := httptest.NewRecorder()
	h.createGrant(rec, req)
	if rec.Code != 201 {
		t.Fatalf("lend = %d, want 201: %s", rec.Code, rec.Body)
	}

	// The copy must be named after the id her session will resolve to.
	want := strings.TrimPrefix(allocated, "u-") + KimiCredentialSuffix
	var got corev1.Secret
	if err := h.Client.Get(t.Context(),
		types.NamespacedName{Namespace: ns, Name: want}, &got); err != nil {
		t.Fatalf("the borrowed account is not under the id she actually resolves to (%s): %v", want, err)
	}

	// And the grant row points at the same principal, or she can see the
	// key listed and still be refused when she spends it.
	gs, err := db.GrantsTo(t.Context(), allocated, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 1 || gs[0].Credential != "alice-kimi" {
		t.Fatalf("the grant was written against a different principal: %+v", gs)
	}
}
