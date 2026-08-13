package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/identity"
	"github.com/zippo1908/agentcell/pkg/ids"
)

type createCellRequest struct {
	Name        string `json:"name"`
	RepoURL     string `json:"repoURL"`
	Branch      string `json:"branch"`
	SecretName  string `json:"secretName"`
	Image       string `json:"image"`
	Description string `json:"description"`
	Preview     string `json:"preview"`
	PreviewPort int32  `json:"previewPort"`
	MaxSessions int32  `json:"maxSessions"`
	// ProductionTarget: "incell" (default) runs production in this Cell;
	// "external" hands the release off to a system that owns running it.
	ProductionTarget string `json:"productionTarget"`
	ExternalURL      string `json:"externalURL"`
	WebhookURL       string `json:"webhookURL"`
	WebhookSecret    string `json:"webhookSecret"`
}

// createCell onboards a project from the console.
//
// Until now a Cell could only be created with cellctl, which meant the web
// console could show projects but never start one — an odd shape for
// something whose whole point is that a team works in it together. Anyone
// who can authenticate can create one; the creator is recorded, because
// "who brought this project in" is the first question asked later.
func (h *Handler) createCell(w http.ResponseWriter, r *http.Request) {
	var req createCellRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := ids.ValidateCellName(req.Name); err != nil {
		writeErr(w, 400, err)
		return
	}
	if strings.TrimSpace(req.RepoURL) == "" || strings.TrimSpace(req.Image) == "" {
		writeErr(w, 400, fmt.Errorf("repoURL and image are required"))
		return
	}
	// A git credential is a Secret in the control namespace, and pointing a
	// Cell at one you do not own would let you borrow it for your own repo.
	if req.SecretName != "" {
		if err := h.checkCredentialOwnership(r, req.SecretName); err != nil {
			writeErr(w, 404, err)
			return
		}
	}
	cell := &acv1.Cell{ObjectMeta: metav1.ObjectMeta{
		Namespace: h.Namespace, Name: req.Name,
		Annotations: map[string]string{
			"agentcell.io/created-by": identity.FromContext(r.Context()).ID(),
		},
	}}
	branch := req.Branch
	if branch == "" {
		branch = "main"
	}
	maxSessions := req.MaxSessions
	if maxSessions <= 0 {
		maxSessions = 2
	}
	// The creator is the first maintainer. Creating a project and then not
	// being able to release it would be a strange first experience — and
	// leaving the member list empty would make the Cell open to everyone,
	// which is not what someone who just created one expects.
	creator := identity.FromContext(r.Context())
	var members []acv1.Member
	if creator.Kind == identity.KindOIDC {
		members = []acv1.Member{{UserID: creator.ID(), Role: acv1.RoleMaintainer}}
	}
	cell.Spec = acv1.CellSpec{
		Members:     members,
		Repo:        acv1.RepoSpec{URL: req.RepoURL, Branch: branch, SecretName: req.SecretName},
		Image:       req.Image,
		Description: req.Description,
		MaxSessions: maxSessions,
	}
	if req.ProductionTarget == string(acv1.ProductionExternal) {
		cell.Spec.Production = acv1.ProductionSpec{
			Target:      acv1.ProductionExternal,
			ExternalURL: req.ExternalURL,
			Webhook:     acv1.WebhookSpec{URL: req.WebhookURL, SecretName: req.WebhookSecret},
		}
		// Same rule as the controller enforces, but said at creation time
		// where it is cheap to fix rather than at the first release.
		if req.WebhookURL != "" && req.WebhookSecret == "" {
			writeErr(w, 400, fmt.Errorf("a webhook needs a signing secret; an unsigned deploy trigger is one anybody who learns the URL can fire"))
			return
		}
		if req.WebhookSecret != "" {
			if err := h.checkCredentialOwnership(r, req.WebhookSecret); err != nil {
				writeErr(w, 404, err)
				return
			}
		}
	}
	if p := strings.Fields(req.Preview); len(p) > 0 {
		port := req.PreviewPort
		if port == 0 {
			port = 3000
		}
		cell.Spec.Preview = acv1.PreviewSpec{Command: p, Port: port}
	}
	if err := h.Client.Create(r.Context(), cell); err != nil {
		writeErr(w, 409, err)
		return
	}
	writeJSON(w, 201, map[string]string{"cell": cell.Name})
}
