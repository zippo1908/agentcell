package webui

import (
	"encoding/json"
	"fmt"
	"k8s.io/apimachinery/pkg/types"
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
	// Runner, Provider and Model are the pairing this project works with.
	// A property of the project — this codebase, this team's account — so
	// chosen once here rather than re-answered on every task.
	Runner   string `json:"runner"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// PlacementClass is a machine pool an administrator offered. A name
	// only: nothing sent here can become a node selector or a toleration.
	PlacementClass string `json:"placementClass"`
	// Repos are ADDITIONAL repositories, for a project made of several. The
	// first one stays in RepoURL, so a single-repo request is unchanged.
	Repos []struct {
		Name       string `json:"name"`
		Path       string `json:"path"`
		URL        string `json:"url"`
		Branch     string `json:"branch"`
		SecretName string `json:"secretName"`
	} `json:"repos"`
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
	// Any real person becomes the project's maintainer. This used to name
	// OIDC users only, so once accounts existed, somebody could create a
	// project and not be recorded as being on it — and the first person
	// added afterwards would close it in their face.
	//
	// A static token is deliberately NOT recorded: it is one shared
	// identity, so naming it would put "everyone with the token" on the
	// list and mean nothing.
	if creator.Kind == identity.KindOIDC || creator.Kind == identity.KindUser {
		members = []acv1.Member{{UserID: creator.ID(), Role: acv1.RoleMaintainer}}
	}
	// With exactly one pool, use it. An administrator who described a single
	// pool described where work goes; a project created without it lands
	// wherever the scheduler likes, which on a cluster with tainted nodes
	// means Pending with no explanation the creator can act on.
	if req.PlacementClass == "" {
		var classes acv1.PlacementClassList
		if err := h.Client.List(r.Context(), &classes); err == nil && len(classes.Items) == 1 {
			req.PlacementClass = classes.Items[0].Name
		}
	}
	if req.PlacementClass != "" {
		// Must be an offer that exists. The API never invents a placement,
		// and a name that resolves to nothing would strand the Cell Pending.
		var pc acv1.PlacementClass
		if err := h.Client.Get(r.Context(), types.NamespacedName{Name: req.PlacementClass}, &pc); err != nil {
			writeErr(w, 400, fmt.Errorf("没有这个机器池"))
			return
		}
	}
	cell.Spec = acv1.CellSpec{
		Members:     members,
		Repo:        acv1.RepoSpec{URL: req.RepoURL, Branch: branch, SecretName: req.SecretName},
		Image:       req.Image,
		Description: req.Description,
		Defaults: acv1.RunDefaults{
			Runner: req.Runner, Provider: req.Provider, Model: req.Model,
		},
		Placement:   acv1.PlacementSpec{Class: req.PlacementClass},
		MaxSessions: maxSessions,
	}
	for _, extra := range req.Repos {
		cell.Spec.Repos = append(cell.Spec.Repos, acv1.RepoSpec{
			Name: extra.Name, Path: extra.Path, URL: extra.URL,
			Branch: extra.Branch, SecretName: extra.SecretName,
		})
	}
	// After the spec is built, not before: validating an empty struct would
	// pass everything.
	if err := validateRepoLayout(cell); err != nil {
		writeErr(w, 400, err)
		return
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

// validateRepoLayout refuses a project group that cannot be laid out.
//
// Two repositories cannot share a directory, and in a group every repository
// needs one — the historical "at the workspace root" only makes sense when
// there is nothing to sit beside. Catching it here beats discovering it as a
// clone that overwrote another clone.
func validateRepoLayout(cell *acv1.Cell) error {
	all := cell.AllRepos()
	if len(all) < 2 {
		return nil
	}
	seen := map[string]string{}
	for _, r := range all {
		if r.Path == "" {
			return fmt.Errorf("项目里有多个仓库时,每个都要有自己的目录(仓库 %q 没有)", r.Name)
		}
		if strings.Contains(r.Path, "..") || strings.HasPrefix(r.Path, "/") {
			return fmt.Errorf("仓库目录 %q 必须是 /workspace 下的相对路径", r.Path)
		}
		if other, dup := seen[r.Path]; dup {
			return fmt.Errorf("仓库 %q 和 %q 都想占用目录 %q", r.Name, other, r.Path)
		}
		seen[r.Path] = r.Name
	}
	return nil
}
