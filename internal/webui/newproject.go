package webui

import (
	"github.com/zippo1908/agentcell/internal/identity"
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
	// DefaultRunner and DefaultProvider are what a new project starts with.
	DefaultRunner string `json:"defaultRunner,omitempty"`
	// DefaultPlacementClass is the pool to preselect when an administrator
	// has described exactly one: with a single pool there is nothing to
	// choose, and leaving it unset put projects nowhere in particular.
	DefaultPlacementClass string `json:"defaultPlacementClass,omitempty"`
	DefaultProvider       string `json:"defaultProvider,omitempty"`
}

func (h *Handler) newProjectOptions(w http.ResponseWriter, r *http.Request) {
	out := newProjectOptions{GitCredentials: []string{}, PlacementClasses: []placementClassView{}}

	// From the handler, not from the built-in table: whatever the operator
	// overlaid at startup is what gets offered here.
	out.Devboxes = h.Devboxes
	out.Runners, out.Providers = h.Registry.Catalogue()
	// The deployment's default pairing, offered preselected. It is a
	// deployment-wide choice rather than a hardcoded one: a team that has a
	// Kimi Code account should not have to pick the same two things on every
	// project, and a team that does not must be able to change it in one
	// place.
	out.DefaultRunner, out.DefaultProvider = h.DefaultRunner, h.DefaultProvider

	// Forge credentials this caller may actually use.
	//
	// It used to list every basic-auth Secret in the control namespace,
	// which handed one person the NAMES of everybody else's forge
	// credentials — and a name is not nothing: it says who works with which
	// host, and it is what somebody would try to reference. Ownership is the
	// same rule that governs using one, so listing follows it.
	p := identity.FromContext(r.Context())
	var secrets corev1.SecretList
	if err := h.Client.List(r.Context(), &secrets, client.InNamespace(h.Namespace)); err == nil {
		for i := range secrets.Items {
			sec := &secrets.Items[i]
			if sec.Type != corev1.SecretTypeBasicAuth {
				continue
			}
			// An unowned credential is the platform's own, offered to
			// everyone; anything with an owner is offered only to them.
			if owner := sec.Labels[OwnerLabel]; owner != "" && !p.Owns(owner) {
				continue
			}
			out.GitCredentials = append(out.GitCredentials, sec.Name)
		}
		sort.Strings(out.GitCredentials)
	}

	// Machine pools, only when there is a choice to make.
	// One class is not "no choice to make" — it is the choice, already made.
	//
	// Hiding the selector when only one pool exists also stopped it being
	// APPLIED, so projects landed with no placement at all on a cluster
	// whose administrator had gone to the trouble of describing exactly one
	// pool they should land on. Returning it lets the console preselect it.
	var classes acv1.PlacementClassList
	if err := h.Client.List(r.Context(), &classes); err == nil && len(classes.Items) == 1 {
		out.DefaultPlacementClass = classes.Items[0].Name
	}
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
