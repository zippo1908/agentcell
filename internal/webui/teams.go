package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/identity"
	"github.com/zippo1908/agentcell/pkg/ids"
)

// Teams: a membership list that outlives any one project.
//
// Per-Cell membership answers "who works on this", and nothing else a real
// group needs. Somebody joining does not join one project; somebody leaving
// must not be removed from eleven projects by hand, because the twelfth is
// the one that gets missed. This is that list, and the Cell keeps its own —
// now as an exception on top of a default rather than the only thing there
// is.
//
// A team governs its own membership: whoever creates it is its first
// maintainer, and only a maintainer can change who is in it. That is the
// same rule Cells use, for the same reason — the alternative is needing
// cluster access to add a colleague.

type teamView struct {
	Name        string        `json:"name"`
	DisplayName string        `json:"displayName"`
	Description string        `json:"description"`
	Members     []acv1.Member `json:"members,omitempty"`
	// Cells is which projects this team governs. Present so that "what does
	// removing this person actually affect" is answerable BEFORE the removal.
	Cells []string `json:"cells,omitempty"`
	// Role is the caller's own role, so the console can hide controls it
	// would only be refused for.
	Role string `json:"role,omitempty"`
}

// teamRoleOf answers a principal's role in a team. Same shape as Cells: a
// static-token deployment has one principal who operates everything.
func teamRoleOf(p identity.Principal, t *acv1.Team) acv1.Role {
	if p.Kind == identity.KindToken {
		return acv1.RoleMaintainer
	}
	return t.RoleOf(p.ID())
}

func (h *Handler) listTeams(w http.ResponseWriter, r *http.Request) {
	var list acv1.TeamList
	if err := h.Client.List(r.Context(), &list, client.InNamespace(h.Namespace)); err != nil {
		writeErr(w, 500, err)
		return
	}
	p := identity.FromContext(r.Context())
	out := []teamView{}
	for i := range list.Items {
		t := &list.Items[i]
		role := teamRoleOf(p, t)
		// A team you are not in is not yours to see. Same reasoning as a Cell
		// you are outside of: a list of every team is a map of how the
		// organisation is arranged.
		if role == "" {
			continue
		}
		out = append(out, teamView{
			Name: t.Name, DisplayName: t.Spec.DisplayName,
			Description: t.Spec.Description, Members: t.Spec.Members,
			Cells: t.Status.CellNames, Role: string(role),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, 200, out)
}

func (h *Handler) createTeam(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := ids.ValidateCellName(body.Name); err != nil {
		writeErr(w, 400, err)
		return
	}
	p := identity.FromContext(r.Context())
	t := &acv1.Team{ObjectMeta: metav1.ObjectMeta{Namespace: h.Namespace, Name: body.Name}}
	t.Spec.DisplayName = body.DisplayName
	t.Spec.Description = body.Description
	// The creator is the first maintainer. A team nobody can administer is
	// not a team, and the alternative — an empty team that anyone may edit —
	// is an open door with a name on it.
	if id := p.ID(); id != "" {
		t.Spec.Members = []acv1.Member{{UserID: id, Role: acv1.RoleMaintainer}}
	}
	if err := h.Client.Create(r.Context(), t); err != nil {
		if apierrors.IsAlreadyExists(err) {
			writeErr(w, 409, fmt.Errorf("that name is taken"))
			return
		}
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 201, teamView{
		Name: t.Name, DisplayName: t.Spec.DisplayName,
		Description: t.Spec.Description, Members: t.Spec.Members,
		Role: string(acv1.RoleMaintainer),
	})
}

// teamFromRequest loads a team and checks the caller may do the thing.
// need == "" means read access (any member).
func (h *Handler) teamFromRequest(w http.ResponseWriter, r *http.Request, need acv1.Role) (*acv1.Team, bool) {
	var t acv1.Team
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: r.PathValue("team")}, &t); err != nil {
		writeErr(w, 404, errNotFound)
		return nil, false
	}
	role := teamRoleOf(identity.FromContext(r.Context()), &t)
	if role == "" {
		// Outside the team: indistinguishable from absent, so probing does
		// not map the organisation.
		writeErr(w, 404, errNotFound)
		return nil, false
	}
	if rank(role) < rank(need) {
		writeErr(w, 403, fmt.Errorf("需要 %s 才能做这件事", need))
		return nil, false
	}
	return &t, true
}

func (h *Handler) putTeamMember(w http.ResponseWriter, r *http.Request) {
	t, ok := h.teamFromRequest(w, r, acv1.RoleMaintainer)
	if !ok {
		return
	}
	var body struct {
		UserID string    `json:"userID"`
		Role   acv1.Role `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.UserID == "" {
		writeErr(w, 400, fmt.Errorf("userID is required"))
		return
	}
	switch body.Role {
	case acv1.RoleViewer, acv1.RoleMember, acv1.RoleMaintainer:
	default:
		writeErr(w, 400, fmt.Errorf("role must be viewer, member or maintainer"))
		return
	}
	replaced := false
	for i := range t.Spec.Members {
		if t.Spec.Members[i].UserID == body.UserID {
			t.Spec.Members[i].Role = body.Role
			replaced = true
			break
		}
	}
	if !replaced {
		t.Spec.Members = append(t.Spec.Members, acv1.Member{UserID: body.UserID, Role: body.Role})
	}
	if !hasMaintainer(t.Spec.Members) {
		writeErr(w, 400, fmt.Errorf("团队不能没有 maintainer"))
		return
	}
	if err := h.Client.Update(r.Context(), t); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"members": t.Spec.Members})
}

func (h *Handler) deleteTeamMember(w http.ResponseWriter, r *http.Request) {
	t, ok := h.teamFromRequest(w, r, acv1.RoleMaintainer)
	if !ok {
		return
	}
	user := r.PathValue("user")
	out := t.Spec.Members[:0]
	for _, m := range t.Spec.Members {
		if m.UserID != user {
			out = append(out, m)
		}
	}
	t.Spec.Members = out
	// Removing the last maintainer would leave a team only an operator with
	// cluster access could repair — which is exactly the dependency managing
	// membership through the API exists to remove.
	if !hasMaintainer(t.Spec.Members) {
		writeErr(w, 400, fmt.Errorf("这是最后一个 maintainer,先指定另一个"))
		return
	}
	if err := h.Client.Update(r.Context(), t); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"members": t.Spec.Members})
}

func (h *Handler) deleteTeam(w http.ResponseWriter, r *http.Request) {
	t, ok := h.teamFromRequest(w, r, acv1.RoleMaintainer)
	if !ok {
		return
	}
	// A team still governing projects is not deletable: deleting it would
	// silently drop everyone's access to those Cells except whoever is named
	// on them directly, and the symptom would appear later, somewhere else.
	if len(t.Status.CellNames) > 0 {
		writeErr(w, 409, fmt.Errorf("这个团队还在管着 %d 个工作区,先把它们改到别处",
			len(t.Status.CellNames)))
		return
	}
	if err := h.Client.Delete(r.Context(), t); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "deleted"})
}

// putCellTeam moves a Cell under a team, or out from under one.
func (h *Handler) putCellTeam(w http.ResponseWriter, r *http.Request) {
	var cell acv1.Cell
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: r.PathValue("cell")}, &cell); err != nil {
		writeErr(w, 404, errNotFound)
		return
	}
	if !h.authorize(w, r, &cell, ActionSettings) {
		return
	}
	var body struct {
		Team string `json:"team"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.Team != "" {
		var t acv1.Team
		if err := h.Client.Get(r.Context(),
			types.NamespacedName{Namespace: h.Namespace, Name: body.Team}, &t); err != nil {
			writeErr(w, 400, fmt.Errorf("没有这个团队"))
			return
		}
		// You cannot hand your project to a group you are not in. Otherwise
		// "set team" is a way to give away — or quietly take over — a
		// project's governance from outside it.
		if teamRoleOf(identity.FromContext(r.Context()), &t) == "" {
			writeErr(w, 403, fmt.Errorf("你不在这个团队里"))
			return
		}
	}
	cell.Spec.Team = body.Team
	if err := h.Client.Update(r.Context(), &cell); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"team": cell.Spec.Team})
}
