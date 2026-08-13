package controller

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
)

// The handoff to an external deployer.
//
// When production is somebody else's system, a release here is an
// announcement, not a deployment: AgentCell says what shipped and gets out of
// the way. The alternative — driving a foreign cluster from this controller —
// means a second deployer competing with the real one for the same product.

// ReleaseEvent is the body an external deployer receives.
//
// Deliberately small and stable: the ref and the repository are what a
// deployer needs to build from, and everything else it can look up. Fields
// added later must be additive, because the receiver is code we do not own.
type ReleaseEvent struct {
	Cell      string `json:"cell"`
	ReleaseID string `json:"releaseID"`
	Ref       string `json:"ref"`
	RepoURL   string `json:"repoURL"`
	Branch    string `json:"branch"`
	At        string `json:"at"`
}

const (
	signatureHeader = "X-AgentCell-Signature"
	eventHeader     = "X-AgentCell-Event"
	deliveryHeader  = "X-AgentCell-Release"
	handoffTimeout  = 10 * time.Second
)

// notifyExternal posts a signed release event.
//
// A webhook with no secret is refused rather than sent unsigned. An unsigned
// POST tells the receiver only that *somebody* who learned the URL wants a
// deploy — which is exactly the property a deploy trigger must not have.
func (r *CellReconciler) notifyExternal(ctx context.Context, cell *acv1.Cell) error {
	w := cell.Spec.Production.Webhook
	if w.URL == "" {
		return nil
	}
	if w.SecretName == "" {
		return fmt.Errorf("production.webhook.secretName is required: an unsigned deploy trigger is one anybody who learns the URL can fire")
	}
	var sec corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: cell.Namespace, Name: w.SecretName}, &sec); err != nil {
		return fmt.Errorf("webhook secret %s: %w", w.SecretName, err)
	}
	key := sec.Data["key"]
	if len(key) == 0 {
		return fmt.Errorf("webhook secret %s has no %q entry", w.SecretName, "key")
	}

	body, err := json.Marshal(ReleaseEvent{
		Cell:      cell.Name,
		ReleaseID: cell.Spec.Production.ReleaseID,
		Ref:       prodRef(cell),
		RepoURL:   cell.Spec.Repo.URL,
		Branch:    cell.Spec.Repo.Branch,
		At:        time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(body)

	if err := checkWebhookTarget(w.URL); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(eventHeader, "release")
	req.Header.Set(signatureHeader, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	// Delivery is at-least-once: a response that never arrives is retried on
	// the next reconcile, and the receiver cannot tell that from a lost
	// request. The release id is the idempotency key, and it is also a
	// header so a receiver can dedupe without parsing the body.
	req.Header.Set(deliveryHeader, cell.Spec.Production.ReleaseID)

	client := &http.Client{Timeout: handoffTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// Surfaced on the Cell rather than retried forever: a deployer that
		// refuses a release is telling the operator something, and burying it
		// behind retries wastes the message.
		return fmt.Errorf("deployer answered %s", resp.Status)
	}
	return nil
}

// prodRef is what a release ships: the explicit ref, or the base branch.
func prodRef(cell *acv1.Cell) string {
	if cell.Spec.Production.Ref != "" {
		return cell.Spec.Production.Ref
	}
	return cell.Spec.Repo.Branch
}

// checkWebhookTarget refuses to make the control plane a request forgery
// tool.
//
// celld reaches this URL with the cluster's network position — it can talk
// to the apiserver, to other namespaces, and on a cloud node to the instance
// metadata service. A webhook target is configuration, so anyone who can
// edit a Cell could otherwise aim the control plane at 169.254.169.254 and
// read the node's credentials out of a "deploy failed" message.
//
// So: only http(s), and never a loopback, link-local or otherwise internal
// address. Operators whose deployer genuinely lives inside the cluster set
// AGENTCELL_WEBHOOK_ALLOW_INTERNAL, which is a decision they take
// deliberately rather than one the default makes for them.
func checkWebhookTarget(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("webhook url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook url must be http or https, got %q", u.Scheme)
	}
	if os.Getenv("AGENTCELL_WEBHOOK_ALLOW_INTERNAL") == "1" {
		return nil
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("webhook url has no host")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		// A name that does not resolve cannot be checked, and sending
		// anyway would be trusting DNS to be honest later.
		return fmt.Errorf("webhook host %q does not resolve: %w", host, err)
	}
	for _, ip := range ips {
		if isInternal(ip) {
			return fmt.Errorf("webhook host %q resolves to the internal address %s; "+
				"set AGENTCELL_WEBHOOK_ALLOW_INTERNAL=1 if that is deliberate", host, ip)
		}
	}
	return nil
}

// isInternal covers the addresses a request forgery would actually aim at.
func isInternal(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast()
}
