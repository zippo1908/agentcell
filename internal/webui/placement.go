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

// Where a project runs, chosen from the machines that exist.
//
// A Cell cannot span nodes — the workspace volume is ReadWriteOnce and every
// pod follows the anchor — so "which machine" is the whole of its capacity
// story, and on a mixed fleet (hyperconverged hosts beside cloud instances,
// a big-memory box, a cheap box) it is a decision somebody has to make.
//
// It is deliberately NOT a text field for a label selector. A selector that
// matches nothing is accepted by Kubernetes and then quietly schedules
// nothing; the Cell sits Pending with the reason buried in an event in
// another namespace. So the console offers the pools that are actually
// there, and this API refuses one that is not.

// poolLabels are the label keys a node pool is conventionally named by.
// Preferred order: the cloud vendors' own, then the informal one, then the
// last resort of a single machine addressed by hostname.
var poolLabels = []string{
	"agentcell.io/pool",
	"node.kubernetes.io/instance-type",
	"node-role.kubernetes.io/worker",
	"topology.kubernetes.io/zone",
	"kubernetes.io/hostname",
}

type nodePool struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// Label is what a person calls it: "pool=gpu".
	Label string `json:"label"`
	Nodes int    `json:"nodes"`
	// Taints a Cell would have to tolerate to land here. Reported rather
	// than hidden: a dedicated pool is usually tainted precisely so nothing
	// arrives by accident, and a placement that names one without tolerating
	// its taints schedules nothing.
	Taints []string `json:"taints"`
	// Free is the largest single node's unreserved capacity, because a Cell
	// lands on ONE node — a pool with ten nodes each half full has no room
	// for a Cell needing a whole one, and a total would say it did.
	FreeCPU    string `json:"freeCPU"`
	FreeMemory string `json:"freeMemory"`
	// Schedulable is false when every node here is cordoned or tainted
	// NoSchedule in a way this Cell would not tolerate.
	Schedulable bool `json:"schedulable"`
}

func (h *Handler) listNodePools(w http.ResponseWriter, r *http.Request) {
	var nodes corev1.NodeList
	if err := h.Client.List(r.Context(), &nodes); err != nil {
		// Reading nodes is a cluster-scoped permission an operator may have
		// declined to grant. Say so plainly instead of returning an empty
		// list, which would read as "this cluster has no machines".
		writeErr(w, 403, fmt.Errorf("cannot read nodes: %w", err))
		return
	}
	// Reserved per node, so "free" means what a scheduler would agree with.
	used := map[string]struct{ cpu, mem resource.Quantity }{}
	var pods corev1.PodList
	if err := h.Client.List(r.Context(), &pods); err == nil {
		for i := range pods.Items {
			p := &pods.Items[i]
			if p.Spec.NodeName == "" || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
				continue
			}
			u := used[p.Spec.NodeName]
			for j := range p.Spec.Containers {
				req := p.Spec.Containers[j].Resources.Requests
				u.cpu.Add(*req.Cpu())
				u.mem.Add(*req.Memory())
			}
			used[p.Spec.NodeName] = u
		}
	}

	pools := map[string]*nodePool{}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		key := poolKeyFor(n)
		val := n.Labels[key]
		id := key + "=" + val
		p := pools[id]
		if p == nil {
			p = &nodePool{Key: key, Value: val, Label: id}
			pools[id] = p
		}
		p.Nodes++
		for _, t := range n.Spec.Taints {
			if t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute {
				p.Taints = appendUnique(p.Taints, t.Key+"="+t.Value+":"+string(t.Effect))
			}
		}
		if n.Spec.Unschedulable {
			continue
		}
		p.Schedulable = true
		u := used[n.Name]
		cpu := n.Status.Allocatable.Cpu().DeepCopy()
		cpu.Sub(u.cpu)
		mem := n.Status.Allocatable.Memory().DeepCopy()
		mem.Sub(u.mem)
		// Largest single node wins: a Cell fits on one machine or on none.
		if p.FreeCPU == "" || cpu.Cmp(resource.MustParse(p.FreeCPU)) > 0 {
			p.FreeCPU = cpu.String()
		}
		if p.FreeMemory == "" || mem.Cmp(resource.MustParse(p.FreeMemory)) > 0 {
			p.FreeMemory = mem.String()
		}
	}
	out := make([]nodePool, 0, len(pools))
	for _, p := range pools {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	writeJSON(w, 200, out)
}

// poolKeyFor picks the most meaningful label this node carries. Falling all
// the way through to hostname is not a failure: on a small cluster "this
// machine" is a legitimate way to place a project.
func poolKeyFor(n *corev1.Node) string {
	for _, k := range poolLabels {
		if _, ok := n.Labels[k]; ok {
			return k
		}
	}
	return "kubernetes.io/hostname"
}

func appendUnique(s []string, v string) []string {
	for _, existing := range s {
		if existing == v {
			return s
		}
	}
	return append(s, v)
}

type placementInput struct {
	// Key and Value name a pool from the list above. Empty clears the
	// placement, letting the scheduler choose again.
	Key   string `json:"key"`
	Value string `json:"value"`
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

	if in.Key == "" {
		cell.Spec.Placement = acv1.PlacementSpec{}
	} else {
		// Refuse a selector that matches nothing. This is the entire point of
		// the endpoint: Kubernetes would accept it and the Cell would then
		// wait forever for a machine that does not exist.
		var nodes corev1.NodeList
		if err := h.Client.List(r.Context(), &nodes); err != nil {
			writeErr(w, 403, fmt.Errorf("cannot read nodes: %w", err))
			return
		}
		var matched []*corev1.Node
		for i := range nodes.Items {
			if nodes.Items[i].Labels[in.Key] == in.Value {
				matched = append(matched, &nodes.Items[i])
			}
		}
		if len(matched) == 0 {
			writeErr(w, 400, fmt.Errorf("no node has %s=%s", in.Key, in.Value))
			return
		}
		cell.Spec.Placement = acv1.PlacementSpec{
			NodeSelector: map[string]string{in.Key: in.Value},
			Tolerations:  tolerationsFor(matched),
		}
	}
	if err := h.Client.Update(r.Context(), &cell); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"nodeSelector": cell.Spec.Placement.NodeSelector,
		"tolerations":  len(cell.Spec.Placement.Tolerations),
	})
}

// tolerationsFor derives what a Cell must tolerate to land on these nodes.
//
// Asking a person to write tolerations would be asking them to restate, in a
// second and more error-prone form, something the cluster already knows. A
// dedicated pool exists to be chosen deliberately; having chosen it, being
// kept out by its own taint is not a safety property, it is a puzzle.
//
// Only taints shared by EVERY matched node are tolerated: one that appears
// on a single cordoned-off machine is that machine's business, not the
// pool's.
func tolerationsFor(nodes []*corev1.Node) []corev1.Toleration {
	counts := map[corev1.Taint]int{}
	for _, n := range nodes {
		seen := map[corev1.Taint]bool{}
		for _, t := range n.Spec.Taints {
			if t.Effect != corev1.TaintEffectNoSchedule && t.Effect != corev1.TaintEffectNoExecute {
				continue
			}
			key := corev1.Taint{Key: t.Key, Value: t.Value, Effect: t.Effect}
			if !seen[key] {
				counts[key]++
				seen[key] = true
			}
		}
	}
	var out []corev1.Toleration
	for t, n := range counts {
		if n != len(nodes) {
			continue
		}
		op := corev1.TolerationOpEqual
		if t.Value == "" {
			op = corev1.TolerationOpExists
		}
		out = append(out, corev1.Toleration{
			Key: t.Key, Operator: op, Value: t.Value, Effect: t.Effect,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
