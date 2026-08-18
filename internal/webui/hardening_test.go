package webui

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
)

// The login form must stop being a way to spend the server's CPU.
//
// Verifying a password is an argon2id derivation on purpose — that is what
// makes a stolen hash expensive — which also makes an unauthenticated client
// able to buy 64 MB and milliseconds of CPU per request. Both counters are
// checked because the two attacks differ: one address guessed from many
// places, or one place grinding through many addresses.
func TestLoginIsRateLimited(t *testing.T) {
	a := &Authenticator{logins: newRateLimiter()}
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = "10.0.0.9:5000"

	for i := 0; i < loginBurst; i++ {
		if a.tooManyLogins(r, "someone@example.com") {
			t.Fatalf("refused attempt %d, which is inside the burst a person mistyping needs", i+1)
		}
	}
	if !a.tooManyLogins(r, "someone@example.com") {
		t.Error("the attempt past the burst was allowed through to the password work")
	}

	// A different client attacking the SAME address is still refused: the
	// per-account counter is what bounds guessing one person's password
	// from everywhere at once.
	other := httptest.NewRequest(http.MethodPost, "/login", nil)
	other.RemoteAddr = "10.0.0.10:5000"
	if !a.tooManyLogins(other, "someone@example.com") {
		t.Error("a second source could keep guessing the same account")
	}

	// And a different account from a fresh client is unaffected.
	fresh := httptest.NewRequest(http.MethodPost, "/login", nil)
	fresh.RemoteAddr = "10.0.0.11:5000"
	if a.tooManyLogins(fresh, "nobody@example.com") {
		t.Error("an unrelated person was locked out by somebody else's attempts")
	}
}

// A forwarded header may only set the bucket where the operator said proxy
// headers can be trusted. Otherwise a client picks its own bucket and the
// limiter is decoration.
func TestRateLimitKeyIsNotClientChosen(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = "10.0.0.9:5000"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")

	untrusting := &Authenticator{logins: newRateLimiter()}
	if got := untrusting.clientKey(r); got != "10.0.0.9" {
		t.Errorf("key = %q; a client-supplied header chose its own rate-limit bucket", got)
	}
	trusting := &Authenticator{logins: newRateLimiter(), TrustForwardedHeaders: true}
	if got := trusting.clientKey(r); got != "1.2.3.4" {
		t.Errorf("key = %q; behind a trusted proxy the real client must be used", got)
	}
}

// Many small parts that each decompress to the per-part maximum are a zip
// bomb; the bound that matters is the sum, not the part.
func TestOfficeExtractionIsBoundedInTotal(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// Each part is highly compressible and declares a large size.
	chunk := bytes.Repeat([]byte("<w:t>"+"A"+"</w:t>"), 40000)
	for i := 0; i < 80; i++ {
		f, err := zw.Create(fmt.Sprintf("word/document%d.xml", i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	done := make(chan int, 1)
	go func() { done <- len(officeText(buf.Bytes(), "word/")) }()
	select {
	case n := <-done:
		if n > maxExtracted {
			t.Errorf("extracted %d bytes, over the %d cap", n, maxExtracted)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("extraction did not finish; an upload can hold a worker indefinitely")
	}
}

// One person must not learn the NAMES of everybody else's forge credentials.
// A name says who works with which host, and it is what somebody would try
// to reference.
func TestNewProjectOptionsHideOtherPeoplesCredentials(t *testing.T) {
	mine := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: "alice-gitlab",
		Labels: map[string]string{OwnerLabel: alice.ID()}}, Type: corev1.SecretTypeBasicAuth}
	theirs := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: "bob-github",
		Labels: map[string]string{OwnerLabel: bob.ID()}}, Type: corev1.SecretTypeBasicAuth}
	shared := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: "platform-git"}, Type: corev1.SecretTypeBasicAuth}

	h := &Handler{
		Client: fake.NewClientBuilder().WithScheme(testScheme(t)).
			WithObjects(mine, theirs, shared).Build(),
		Namespace: ns,
		Registry:  testRegistry(t),
	}
	rec := httptest.NewRecorder()
	h.newProjectOptions(rec, asUser(httptest.NewRequest(http.MethodGet, "/x", nil), alice))
	body := rec.Body.String()

	if !strings.Contains(body, "alice-gitlab") {
		t.Error("the caller's own credential was hidden from them")
	}
	if !strings.Contains(body, "platform-git") {
		t.Error("an unowned platform credential was hidden; nobody could pick it")
	}
	if strings.Contains(body, "bob-github") {
		t.Errorf("somebody else's credential name was disclosed: %s", body)
	}
}

// One pool is the choice, already made. Hiding the selector also stopped it
// being applied, so projects landed with no placement at all on a cluster
// whose administrator had described exactly where they should go.
func TestSinglePlacementClassIsApplied(t *testing.T) {
	pc := &acv1.PlacementClass{ObjectMeta: metav1.ObjectMeta{Name: "only-pool"}}
	h := &Handler{
		Client:    fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(pc).Build(),
		Namespace: ns,
		Registry:  testRegistry(t),
	}
	rec := httptest.NewRecorder()
	h.newProjectOptions(rec, asUser(httptest.NewRequest(http.MethodGet, "/x", nil), alice))
	if !strings.Contains(rec.Body.String(), "only-pool") {
		t.Errorf("the single pool was not offered at all: %s", rec.Body)
	}
}
