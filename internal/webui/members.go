package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/identity"
)

// Membership management.
//
// It existed as a documented capability with no way to exercise it — "the
// maintainer manages members" was true only if you counted editing the CR
// with kubectl, which is not a thing a maintainer of a project necessarily
// has. Either the docs were wrong or this was missing; this was missing.

type memberRequest struct {
	// Email is how a person is named now that the platform has accounts.
	// UserID stays accepted because it is what the CR stores and what
	// scripts written before accounts existed already send.
	Email  string    `json:"email"`
	UserID string    `json:"userID"`
	Role   acv1.Role `json:"role"`
}

// memberView is one row of the access list, in the form a person reads.
type memberView struct {
	Email string    `json:"email"`
	Name  string    `json:"name,omitempty"`
	Role  acv1.Role `json:"role"`
	// Unknown marks an id no account matches — somebody removed from the
	// platform, or a project from before accounts existed. Shown rather
	// than hidden: a silent gap in an access list is what nobody notices.
	Unknown bool `json:"unknown,omitempty"`
}

// listMembers answers "who is on this project", with names rather than the
// hashes the CR stores — an access list nobody can read is not one anybody
// checks.
func (h *Handler) listMembers(w http.ResponseWriter, r *http.Request) {
	var cell acv1.Cell
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: r.PathValue("cell")}, &cell); err != nil {
		writeErr(w, 404, errNotFound)
		return
	}
	if !h.authorize(w, r, &cell, ActionView) {
		return
	}
	byID := h.accountsByID(r)
	out := make([]memberView, 0, len(cell.Spec.Members))
	for _, m := range cell.Spec.Members {
		v := memberView{Role: m.Role}
		if u, ok := byID[m.UserID]; ok {
			v.Email, v.Name = u.email, u.name
		} else {
			v.Email, v.Unknown = m.UserID, true
		}
		out = append(out, v)
	}
	writeJSON(w, 200, map[string]any{
		"members": out,
		// An open project has no gate at all; saying so beats an empty list
		// that reads as "nobody has access".
		"open": effectiveAccess(&cell) == acv1.AccessOpen,
	})
}

type accountLite struct{ email, name string }

// accountsByID maps hashed ids back to people. Built per request from a
// table with one row per colleague — small enough that a cache would only
// be a staleness bug.
func (h *Handler) accountsByID(r *http.Request) map[string]accountLite {
	out := map[string]accountLite{}
	if h.Auth == nil || h.Auth.Accounts == nil {
		return out
	}
	users, err := h.Auth.Accounts.DB.Users(r.Context())
	if err != nil {
		return out
	}
	for _, u := range users {
		out[identity.Principal{Subject: identity.UserSubject(u.Email)}.ID()] = accountLite{u.Email, u.Name}
	}
	return out
}

// memberID resolves whichever form the caller used, refusing an address
// that belongs to nobody: adding one would look like it worked, grant
// nothing, and close the project to everyone else while doing it.
func (h *Handler) memberID(r *http.Request, req memberRequest) (string, error) {
	if req.UserID != "" {
		return req.UserID, nil
	}
	if req.Email == "" {
		return "", fmt.Errorf("要么给 email,要么给 userID")
	}
	if h.Auth == nil || h.Auth.Accounts == nil {
		return "", fmt.Errorf("这个部署没有开启账号体系,只能用 userID")
	}
	if _, _, err := h.Auth.Accounts.DB.UserByEmail(r.Context(), req.Email); err != nil {
		return "", fmt.Errorf("平台上没有这个人——先邀请 %s", req.Email)
	}
	return identity.Principal{Subject: identity.UserSubject(req.Email)}.ID(), nil
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
	id, err := h.memberID(r, req)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	h.updateMembers(w, r, func(cell *acv1.Cell) error {
		for i := range cell.Spec.Members {
			if cell.Spec.Members[i].UserID == id {
				cell.Spec.Members[i].Role = req.Role
				return nil
			}
		}
		cell.Spec.Members = append(cell.Spec.Members, acv1.Member{UserID: id, Role: req.Role})
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
	// The path may carry an email now. An id is hex with a "u-" prefix, so
	// an "@" is unambiguous.
	if strings.Contains(user, "@") {
		user = identity.Principal{Subject: identity.UserSubject(user)}.ID()
	}
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
