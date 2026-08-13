package webui

import (
	"net/http"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/access"
)

// Everything the create-a-project form needs, so it can offer choices
// instead of demanding typing.
//
// The form used to be six free-text boxes, one of which was a container
// image path. Every one of those is a question the platform can already
// answer — which devboxes an operator offers, which git credentials exist,
// which machine pools were defined, which runners and providers are
// configured — and asking a person to retype an answer the system has is how
// a project ends up in ImagePullBackOff because of a typo two layers away.
type newProjectOptions struct {
	Devboxes []access.Devbox `json:"devboxes"`
	// GitCredentials are the names of forge credentials that exist. Names
	// only: what is inside them is nobody's business here.
	GitCredentials []string `json:"gitCredentials"`
	// PlacementClasses is empty on a single-node cluster, and the form then
	// omits the whole control rather than offering a choice of one. An
	// option that cannot change anything is worse than no option: it implies
	// there is something to decide.
	PlacementClasses []placementClassView `json:"placementClasses"`
	// Runners and Providers come from the same catalogue the dispatch form
	// uses, so a project's default pairing is chosen from what can actually
	// be driven.
	Runners   []access.RunnerInfo   `json:"runners"`
	Providers []access.ProviderInfo `json:"providers"`
}

func (h *Handler) newProjectOptions(w http.ResponseWriter, r *http.Request) {
	out := newProjectOptions{GitCredentials: []string{}, PlacementClasses: []placementClassView{}}

	if d, err := access.LoadDevboxes(); err == nil {
		out.Devboxes = d
	}
	out.Runners, out.Providers = h.Registry.Catalogue()

	// Forge credentials are basic-auth Secrets in the control namespace.
	var secrets corev1.SecretList
	if err := h.Client.List(r.Context(), &secrets, client.InNamespace(h.Namespace)); err == nil {
		for i := range secrets.Items {
			if secrets.Items[i].Type == corev1.SecretTypeBasicAuth {
				out.GitCredentials = append(out.GitCredentials, secrets.Items[i].Name)
			}
		}
		sort.Strings(out.GitCredentials)
	}

	// Machine pools, only when there is a choice to make.
	var classes acv1.PlacementClassList
	if err := h.Client.List(r.Context(), &classes); err == nil && len(classes.Items) > 1 {
		var nodes corev1.NodeList
		_ = h.Client.List(r.Context(), &nodes)
		for i := range classes.Items {
			c := &classes.Items[i]
			v := placementClassView{
				Name: c.Name, DisplayName: c.Spec.DisplayName,
				Description: c.Spec.Description, Selector: selectorText(c.Spec.NodeSelector),
				Tolerated: len(c.Spec.Tolerations) > 0,
			}
			for j := range nodes.Items {
				if matchesSelector(&nodes.Items[j], c.Spec.NodeSelector) {
					v.Nodes++
				}
			}
			out.PlacementClasses = append(out.PlacementClasses, v)
		}
		sort.Slice(out.PlacementClasses, func(i, j int) bool {
			return out.PlacementClasses[i].Name < out.PlacementClasses[j].Name
		})
	}
	writeJSON(w, 200, out)
}
