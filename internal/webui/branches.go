package webui

import (
	"net/http"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

// The branch tree, read from the project's own checkout.
//
// From the repository rather than from a forge API, because the forge is
// whatever a deployment happens to use — GitHub, GitLab, something
// self-hosted behind a tunnel — and "what branches does this project have"
// should not depend on which. The anchor already holds a real clone.
//
// Ahead/behind are measured against the base branch, because that is the
// question people actually have about a session branch: is it merged, and
// how far has it drifted since.

type branchView struct {
	Name   string `json:"name"`
	Ahead  int    `json:"ahead"`
	Behind int    `json:"behind"`
	When   string `json:"when"`
	// Subject is the last commit's summary line.
	Subject string `json:"subject"`
	// Base marks the branch everything else is compared against.
	Base bool `json:"base,omitempty"`
	// Session is the session id when this is a session/<id> branch, so the
	// tree can link back to the work that produced it.
	Session string `json:"session,omitempty"`
	// Merged is a branch with nothing the base does not already have.
	Merged bool `json:"merged,omitempty"`
}

func (h *Handler) listBranches(w http.ResponseWriter, r *http.Request) {
	var cell acv1.Cell
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: r.PathValue("cell")}, &cell); err != nil {
		writeErr(w, 404, errNotFound)
		return
	}
	if !h.authorize(w, r, &cell, ActionView) {
		return
	}
	if h.RESTConfig == nil || h.Kube == nil {
		writeErr(w, 501, errNotFound)
		return
	}
	base := cell.Spec.Repo.Branch
	if base == "" {
		base = "main"
	}
	ns := ids.WorkloadNamespace(cell.Name)
	out, err := h.execInPod(r.Context(), ns, ids.AnchorStatefulSet+"-0",
		[]string{runtimeapi.RuntimeBin, "branches", base})
	if err != nil {
		// A Cell whose anchor is not up yet has no answer, and an empty list
		// is a truthful one — better than an error the console has to
		// special-case on every project that is still starting.
		writeJSON(w, 200, []branchView{})
		return
	}
	views := []branchView{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 5 || f[0] == "" {
			continue
		}
		v := branchView{Name: f[0], When: f[3], Subject: f[4], Base: f[0] == base}
		v.Ahead, _ = strconv.Atoi(f[1])
		v.Behind, _ = strconv.Atoi(f[2])
		// Nothing of its own that the base lacks: safe to delete, and the
		// most useful thing the tree can tell somebody.
		v.Merged = !v.Base && v.Ahead == 0
		if id, ok := strings.CutPrefix(f[0], "session/"); ok {
			v.Session = id
		}
		views = append(views, v)
	}
	writeJSON(w, 200, views)
}
