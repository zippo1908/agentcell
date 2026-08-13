package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
)

// Where a project runs, chosen from what an administrator has offered.
//
// The first version of this read the cluster's own node labels, let a Cell
// maintainer pick any of them, and then DERIVED the tolerations needed to
// land there — taints included. That crossed a trust boundary. "Maintainer
// of a project" is not "administrator of the cluster", and the two are
// routinely different people; a maintainer could have named
// node-role.kubernetes.io/control-plane and the platform would have
// tolerated the taint that exists precisely to keep workloads off it, for a
// pod that runs a model's output against a repository.
//
// A taint is an administrator's refusal. Reading one and writing the matching
// toleration is not placement, it is a bypass. So the offer is now explicit:
// an administrator writes PlacementClasses, tolerations and all, and a
// maintainer can express nothing that is not on that list.

type placementClassView struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	Selector    string `json:"selector"`
	// Nodes and Free are observed, so a maintainer can tell a pool with room
	// from one without. Read-only facts about an offer somebody else made.
	Nodes int    `json:"nodes"`
	Free  string `json:"free,omitempty"`
	// Tolerated says the class carries tolerations. Shown, not editable —
	// somebody choosing a dedicated pool should know it is dedicated.
	Tolerated bool `json:"tolerated,omitempty"`
}

func (h *Handler) listPlacementClasses(w http.ResponseWriter, r *http.Request) {
	var list acv1.PlacementClassList
	if err := h.Client.List(r.Context(), &list); err != nil {
		// No classes defined is not an error: it means an administrator has
		// offered none, and the honest answer is an empty list.
		writeJSON(w, 200, []placementClassView{})
		return
	}
	var nodes corev1.NodeList
	_ = h.Client.List(r.Context(), &nodes)

	out := make([]placementClassView, 0, len(list.Items))
	for i := range list.Items {
		c := &list.Items[i]
		v := placementClassView{
			Name: c.Name, DisplayName: c.Spec.DisplayName,
			Description: c.Spec.Description, Selector: selectorText(c.Spec.NodeSelector),
			Tolerated: len(c.Spec.Tolerations) > 0,
		}
		var best resource.Quantity
		for j := range nodes.Items {
			if !matchesSelector(&nodes.Items[j], c.Spec.NodeSelector) {
				continue
			}
			v.Nodes++
			// Largest single node, because a Cell lands on ONE machine: a
			// pool of ten half-full nodes has no room for a Cell needing a
			// whole one, and a total would claim otherwise.
			if a := nodes.Items[j].Status.Allocatable.Memory(); a.Cmp(best) > 0 {
				best = *a
			}
		}
		if v.Nodes > 0 {
			v.Free = best.String()
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, 200, out)
}

func matchesSelector(n *corev1.Node, sel map[string]string) bool {
	for k, v := range sel {
		if n.Labels[k] != v {
			return false
		}
	}
	return len(sel) > 0
}

func selectorText(sel map[string]string) string {
	keys := make([]string, 0, len(sel))
	for k := range sel {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for _, k := range keys {
		if out != "" {
			out += ","
		}
		out += k + "=" + sel[k]
	}
	return out
}

type placementInput struct {
	// Class names a PlacementClass. Empty clears the placement and lets the
	// scheduler choose again. Nothing else is accepted — in particular there
	// is no way to send a raw selector or a toleration.
	Class string `json:"class"`
}

func (h *Handler) putPlacement(w http.ResponseWriter, r *http.Request) {
	var cell acv1.Cell
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: r.PathValue("cell")}, &cell); err != nil {
		writeErr(w, 404, errNotFound)
		return
	}
	if !h.authorize(w, r, &cell, ActionSettings) {
		return
	}
	var in placementInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, err)
		return
	}

	if in.Class == "" {
		cell.Spec.Placement = acv1.PlacementSpec{}
	} else {
		var pc acv1.PlacementClass
		if err := h.Client.Get(r.Context(), types.NamespacedName{Name: in.Class}, &pc); err != nil {
			writeErr(w, 400, fmt.Errorf("没有这个机器池;可选的由集群管理员定义"))
			return
		}
		// Only the name is stored. The selector and any tolerations are
		// resolved by the controller from the class, so editing a class
		// updates every Cell using it — and so that nothing a maintainer
		// sends can become a toleration.
		cell.Spec.Placement = acv1.PlacementSpec{Class: pc.Name}
	}
	if err := h.Client.Update(r.Context(), &cell); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"class": cell.Spec.Placement.Class})
}

// listNodePools is gone on purpose.
//
// It reported every node label and taint in the cluster to any authenticated
// user, which is a map of the fleet, and it was the input to a control that
// let a project maintainer pick any of them. Both halves were wrong. What an
// administrator needs to author a class is `kubectl get nodes --show-labels`,
// which they already have.
