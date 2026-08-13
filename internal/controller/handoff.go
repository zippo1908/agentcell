package controller

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(eventHeader, "release")
	req.Header.Set(signatureHeader, "sha256="+hex.EncodeToString(mac.Sum(nil)))

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
