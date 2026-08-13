package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/zippo1908/agentcell/internal/identity"
)

func credFixture(t *testing.T, objs ...*corev1.Secret) (*Handler, struct{}) {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(testScheme(t))
	for _, o := range objs {
		b = b.WithObjects(o)
	}
	return &Handler{Client: b.Build(), Namespace: ns}, struct{}{}
}

// A key must go in and never come back out — not even to the person who
// just sent it, or it lives in a response body, a proxy log and a cache.
func TestCredentialKeyIsWriteOnly(t *testing.T) {
	h, _ := credFixture(t)
	req := asUser(httptest.NewRequest(http.MethodPut, "/api/credentials/mine",
		strings.NewReader(`{"key":"sk-super-secret-value"}`)), alice)
	req.SetPathValue("name", "mine")
	rec := httptest.NewRecorder()
	h.putCredential(rec, req)
	if rec.Code != 200 {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "sk-super-secret-value") {
		t.Errorf("the key came back in the response: %s", rec.Body)
	}
	// Listing shows enough to tell two keys apart, and no more.
	lr := httptest.NewRecorder()
	h.listCredentials(lr, asUser(httptest.NewRequest(http.MethodGet, "/api/credentials", nil), alice))
	if strings.Contains(lr.Body.String(), "sk-super-secret-value") {
		t.Errorf("the key leaked through the list: %s", lr.Body)
	}
	if !strings.Contains(lr.Body.String(), "alue") {
		t.Errorf("no hint to tell keys apart: %s", lr.Body)
	}
}

// The platform's own Secrets must be untouchable here. A user managing their
// key must not be one name collision away from replacing the console's
// tokens or the forge credential.
func TestPlatformSecretsAreNotManageable(t *testing.T) {
	platform := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "celld-tokens"}}
	platform.Data = map[string][]byte{"tokens": []byte("operator-token")}
	h, _ := credFixture(t, platform)

	req := asUser(httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"key":"mine"}`)), alice)
	req.SetPathValue("name", "celld-tokens")
	rec := httptest.NewRecorder()
	h.putCredential(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("overwriting a platform Secret = %d, want 409", rec.Code)
	}

	del := asUser(httptest.NewRequest(http.MethodDelete, "/x", nil), alice)
	del.SetPathValue("name", "celld-tokens")
	drec := httptest.NewRecorder()
	h.deleteCredential(drec, del)
	if drec.Code != http.StatusNotFound {
		t.Errorf("deleting a platform Secret = %d, want 404", drec.Code)
	}
}

// Somebody else's credential is invisible, and not overwritable.
func TestCredentialsAreOwned(t *testing.T) {
	bobs := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: "bob-key",
		Labels: map[string]string{credLabel: "model", OwnerLabel: bob.ID()},
	}}
	bobs.Data = map[string][]byte{"key": []byte("sk-bob")}
	h, _ := credFixture(t, bobs)

	rec := httptest.NewRecorder()
	h.listCredentials(rec, asUser(httptest.NewRequest(http.MethodGet, "/x", nil), alice))
	var out []credView
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out) != 0 {
		t.Errorf("Alice sees Bob's credentials: %v", out)
	}

	put := asUser(httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"key":"stolen"}`)), alice)
	put.SetPathValue("name", "bob-key")
	prec := httptest.NewRecorder()
	h.putCredential(prec, put)
	if prec.Code != http.StatusConflict {
		t.Errorf("Alice overwrote Bob's credential: %d", prec.Code)
	}
}

func TestCredentialNamesAreValidated(t *testing.T) {
	h, _ := credFixture(t)
	for _, bad := range []string{"", "UPPER", "has space", "../escape", strings.Repeat("x", 70)} {
		req := asUser(httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"key":"k"}`)), alice)
		req.SetPathValue("name", bad)
		rec := httptest.NewRecorder()
		h.putCredential(rec, req)
		if rec.Code != 400 {
			t.Errorf("name %q = %d, want 400", bad, rec.Code)
		}
	}
	// And an empty key is refused rather than stored as a working-looking one.
	req := asUser(httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"key":""}`)), alice)
	req.SetPathValue("name", "ok")
	rec := httptest.NewRecorder()
	h.putCredential(rec, req)
	if rec.Code != 400 {
		t.Errorf("empty key = %d, want 400", rec.Code)
	}
}

var _ = identity.StaticToken
