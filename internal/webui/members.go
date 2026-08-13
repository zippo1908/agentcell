package webui

import (
	"encoding/json"
	"fmt"
	"net/http"

	"k8s.io/apimachinery/pkg/types"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
)

// Membership management.
//
// It existed as a documented capability with no way to exercise it — "the
// maintainer manages members" was true only if you counted editing the CR
// with kubectl, which is not a thing a maintainer of a project necessarily
// has. Either the docs were wrong or this was missing; this was missing.

type memberRequest struct {
	UserID string    `json:"userID"`
	Role   acv1.Role `json:"role"`
}

// putMember adds or changes a member.
func (h *Handler) putMember(w http.ResponseWriter, r *http.Request) {
	var req memberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	switch req.Role {
	case acv1.RoleViewer, acv1.RoleMember, acv1.RoleMaintainer:
	default:
		writeErr(w, 400, fmt.Errorf("role must be viewer, member or maintainer"))
		return
	}
	if req.UserID == "" {
		writeErr(w, 400, fmt.Errorf("userID is required"))
		return
	}
	h.updateMembers(w, r, func(cell *acv1.Cell) error {
		for i := range cell.Spec.Members {
			if cell.Spec.Members[i].UserID == req.UserID {
				cell.Spec.Members[i].Role = req.Role
				return nil
			}
		}
		cell.Spec.Members = append(cell.Spec.Members, acv1.Member{UserID: req.UserID, Role: req.Role})
		// Naming somebody is an unambiguous statement that this project has
		// an inside and an outside, so it closes an open Cell rather than
		// leaving it open with a member list nobody enforces.
		if effectiveAccess(cell) == acv1.AccessOpen {
			cell.Spec.Access = acv1.AccessRestricted
		}
		return nil
	})
}

// deleteMember removes one.
func (h *Handler) deleteMember(w http.ResponseWriter, r *http.Request) {
	user := r.PathValue("user")
	h.updateMembers(w, r, func(cell *acv1.Cell) error {
		out := cell.Spec.Members[:0]
		found := false
		for _, m := range cell.Spec.Members {
			if m.UserID == user {
				found = true
				continue
			}
			out = append(out, m)
		}
		if !found {
			return fmt.Errorf("not a member")
		}
		// Removing the last maintainer would leave a restricted Cell that
		// nobody can release or administer — recoverable only with cluster
		// access, which is precisely what this API exists to avoid needing.
		if !hasMaintainer(out) && effectiveAccess(cell) == acv1.AccessRestricted {
			return fmt.Errorf("that is the last maintainer; promote someone else first")
		}
		cell.Spec.Members = out
		return nil
	})
}

func hasMaintainer(ms []acv1.Member) bool {
	for _, m := range ms {
		if m.Role == acv1.RoleMaintainer {
			return true
		}
	}
	return false
}

func (h *Handler) updateMembers(w http.ResponseWriter, r *http.Request, mutate func(*acv1.Cell) error) {
	name := r.PathValue("cell")
	var cell acv1.Cell
	if err := h.Client.Get(r.Context(), types.NamespacedName{Namespace: h.Namespace, Name: name}, &cell); err != nil {
		writeErr(w, 404, err)
		return
	}
	if !h.authorize(w, r, &cell, ActionSettings) {
		return
	}
	if err := mutate(&cell); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := h.Client.Update(r.Context(), &cell); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"members": cell.Spec.Members, "access": effectiveAccess(&cell)})
}
