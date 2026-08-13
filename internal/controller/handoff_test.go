package controller

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/pkg/ids"
)

func externalCell(url, secret string) *acv1.Cell {
	c := testCell()
	c.Spec.Production = acv1.ProductionSpec{
		Target:      acv1.ProductionExternal,
		ExternalURL: "https://shop.example.com",
		ReleaseID:   ids.NewSessionID(),
		Webhook:     acv1.WebhookSpec{URL: url, SecretName: secret},
	}
	return c
}

func hmacSecret(name string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: controlNS, Name: name}}
	s.Data = map[string][]byte{"key": []byte("shared-hmac-key")}
	return s
}

// A deploy trigger anyone who learns the URL can fire is not a deploy
// trigger. The body is signed so the receiver can tell a release from a
// stranger.
func TestReleaseHandoffIsSigned(t *testing.T) {
	// httptest listens on loopback, which the SSRF guard refuses by default.
	t.Setenv("AGENTCELL_WEBHOOK_ALLOW_INTERNAL", "1")
	var gotBody []byte
	var gotSig, gotEvent string
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotBody, _ = io.ReadAll(r.Body)
		gotSig = r.Header.Get(signatureHeader)
		gotEvent = r.Header.Get(eventHeader)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	cell := externalCell(srv.URL, "deploy-hmac")
	c := newFake(t, cell, hmacSecret("deploy-hmac"))
	r := &CellReconciler{Client: c, ControlNamespace: controlNS}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "shop"}}
	for range 2 {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() == 0 {
		t.Fatal("the external deployer was never told about the release")
	}
	if gotEvent != "release" {
		t.Errorf("event header = %q", gotEvent)
	}
	mac := hmac.New(sha256.New, []byte("shared-hmac-key"))
	mac.Write(gotBody)
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); gotSig != want {
		t.Errorf("signature = %q, want %q", gotSig, want)
	}
	var ev ReleaseEvent
	if err := json.Unmarshal(gotBody, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Cell != "shop" || ev.ReleaseID != cell.Spec.Production.ReleaseID || ev.Ref == "" {
		t.Errorf("event does not say what shipped: %+v", ev)
	}
}

// Once per release, not once per reconcile: a deploy is not idempotent from
// the receiver's point of view.
func TestHandoffFiresOncePerRelease(t *testing.T) {
	// httptest listens on loopback, which the SSRF guard refuses by default.
	t.Setenv("AGENTCELL_WEBHOOK_ALLOW_INTERNAL", "1")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	cell := externalCell(srv.URL, "deploy-hmac")
	c := newFake(t, cell, hmacSecret("deploy-hmac"))
	r := &CellReconciler{Client: c, ControlNamespace: controlNS}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "shop"}}
	for range 5 {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("deployer was called %d times for one release", n)
	}
	// A new release is a new announcement.
	var fresh acv1.Cell
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: controlNS, Name: "shop"}, &fresh)
	fresh.Spec.Production.ReleaseID = ids.NewSessionID()
	_ = c.Update(context.Background(), &fresh)
	for range 2 {
		_, _ = r.Reconcile(context.Background(), req)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("a second release produced %d total calls, want 2", n)
	}
}

// An unsigned deploy trigger is refused rather than sent: sending it would
// hand out a capability while looking like configuration.
func TestWebhookWithoutASecretIsRefused(t *testing.T) {
	// httptest listens on loopback, which the SSRF guard refuses by default.
	t.Setenv("AGENTCELL_WEBHOOK_ALLOW_INTERNAL", "1")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	cell := externalCell(srv.URL, "")
	c := newFake(t, cell)
	r := &CellReconciler{Client: c, ControlNamespace: controlNS}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "shop"}}
	for range 2 {
		_, _ = r.Reconcile(context.Background(), req)
	}
	if calls.Load() != 0 {
		t.Error("an unsigned release was sent")
	}
	var got acv1.Cell
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: controlNS, Name: "shop"}, &got)
	if got.Status.HandoffMessage == "" {
		t.Error("the refusal was not surfaced on the Cell")
	}
}

// Switching to external must REMOVE the in-Cell production zone. Leaving it
// running would serve a stale build on a URL the console used to advertise.
func TestSwitchingToExternalRemovesTheInCellZone(t *testing.T) {
	cell := testCell()
	cell.Spec.Production = acv1.ProductionSpec{ReleaseID: ids.NewSessionID()}
	c := newFake(t, cell)
	r := &CellReconciler{Client: c, ControlNamespace: controlNS}
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "shop"}}
	for range 2 {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	ns := ids.WorkloadNamespace("shop")
	var dep appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.ProdDeployment}, &dep); err != nil {
		t.Fatalf("in-cell production was never created: %v", err)
	}

	var fresh acv1.Cell
	_ = c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "shop"}, &fresh)
	fresh.Spec.Production.Target = acv1.ProductionExternal
	fresh.Spec.Production.ExternalURL = "https://shop.example.com"
	if err := c.Update(ctx, &fresh); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.ProdDeployment}, &dep); err == nil {
		t.Error("a zombie production deployment survived the switch to external")
	}
	var svc corev1.Service
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.ProdService}, &svc); err == nil {
		t.Error("the production Service survived the switch")
	}
}

// celld reaches a webhook with the cluster's network position: it can talk
// to the apiserver, to other namespaces, and on a cloud node to the instance
// metadata service. A webhook target is configuration, so without a check
// anyone who can edit a Cell could aim the control plane at 169.254.169.254
// and read the node's credentials out of a "deploy failed" message.
func TestWebhookTargetCannotPointInward(t *testing.T) {
	for _, bad := range []string{
		"http://127.0.0.1:8080/hook",
		"http://localhost/hook",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5/hook",
		"http://192.168.1.10/hook",
		"http://[::1]/hook",
		"file:///etc/passwd",
		"gopher://example.com/",
	} {
		if _, err := checkWebhookTarget(bad); err == nil {
			t.Errorf("%s was accepted; the control plane is a request forgery tool", bad)
		}
	}
	// A public target is fine.
	if _, err := checkWebhookTarget("https://example.com/hooks/agentcell"); err != nil {
		t.Errorf("a public https target was refused: %v", err)
	}
	// An operator can opt in deliberately — for a name that resolves. A name
	// that does not is still refused either way: sending anyway would be
	// trusting DNS to be honest later, which is exactly the attack.
	t.Setenv("AGENTCELL_WEBHOOK_ALLOW_INTERNAL", "1")
	if _, err := checkWebhookTarget("http://localhost:9000/hook"); err != nil {
		t.Errorf("the opt-in did not take effect: %v", err)
	}
	if _, err := checkWebhookTarget("http://no-such-host.invalid/hook"); err == nil {
		t.Error("an unresolvable host was accepted; DNS would decide later what we contact")
	}
}

// Delivery is at-least-once, so the receiver needs an idempotency key it can
// read without parsing the body.
func TestReleaseIDTravelsAsAnIdempotencyKey(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get(deliveryHeader)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	cell := externalCell(srv.URL, "deploy-hmac")
	c := newFake(t, cell, hmacSecret("deploy-hmac"))
	r := &CellReconciler{Client: c, ControlNamespace: controlNS}
	t.Setenv("AGENTCELL_WEBHOOK_ALLOW_INTERNAL", "1") // httptest is loopback
	for range 2 {
		_, _ = r.Reconcile(context.Background(),
			ctrl.Request{NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "shop"}})
	}
	if gotKey != cell.Spec.Production.ReleaseID {
		t.Errorf("idempotency key = %q, want the release id %q", gotKey, cell.Spec.Production.ReleaseID)
	}
}

// Validating a hostname and then handing it to http.Client is not enough:
// the client resolves AGAIN when it dials, so a name that answered with a
// public address during the check can answer with a link-local one a
// millisecond later. And a 302 would walk past a check performed on the
// original URL.
func TestWebhookClientPinsAndRefusesRedirects(t *testing.T) {
	// A receiver that redirects — a real one would not, which is why
	// refusing outright is affordable.
	var reached atomic.Int32
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(200)
	}))
	defer inner.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, inner.URL, http.StatusFound)
	}))
	defer redirector.Close()

	t.Setenv("AGENTCELL_WEBHOOK_ALLOW_INTERNAL", "1") // httptest is loopback
	c, err := webhookClient(redirector.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Post(redirector.URL, "application/json", strings.NewReader("{}")); err == nil {
		t.Error("a redirect was followed; the target check applies only to the first hop")
	}
	if reached.Load() != 0 {
		t.Error("the redirect target was contacted")
	}

	// The pinned dialer only reaches what was approved: a client built for
	// one host must not connect to another address, even if asked.
	c2, err := webhookClient(inner.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c2.Post(inner.URL, "application/json", strings.NewReader("{}")); err != nil {
		t.Errorf("the approved target became unreachable: %v", err)
	}
}
