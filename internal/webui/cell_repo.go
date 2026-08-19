package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
)

// Attaching a repository to a project that already exists.
//
// Creating a project used to require its repository URL in the same breath.
// That is the wrong order for how the work actually starts: the project is
// agreed on first, and the GitLab repository is created — often by somebody
// else, often the next day — after there is a project to create it for.
// Forcing both at once meant either waiting, or typing a URL that did not
// resolve to anything yet and getting a Cell stuck cloning nothing.

type repoInput struct {
	URL        string `json:"url"`
	Branch     string `json:"branch"`
	SecretName string `json:"secretName"`
}

// putRepo attaches the project's repository.
//
// It attaches; it does not swap. Repointing a project that already has a
// repository would leave the anchor holding a clone of one codebase and
// every open session a worktree of another, with the branches people are
// reviewing belonging to neither. That is a migration, not a form field,
// and it deserves to be asked for explicitly rather than fallen into.
func (h *Handler) putRepo(w http.ResponseWriter, r *http.Request) {
	var cell acv1.Cell
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: r.PathValue("cell")}, &cell); err != nil {
		writeErr(w, 404, errNotFound)
		return
	}
	if !h.authorize(w, r, &cell, ActionSettings) {
		return
	}
	var in repoInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, err)
		return
	}
	in.URL = strings.TrimSpace(in.URL)
	if in.URL == "" {
		writeErr(w, 400, fmt.Errorf("要关联的仓库地址不能为空"))
		return
	}
	if cell.Spec.Repo.URL != "" {
		writeErr(w, 409, fmt.Errorf("这个项目已经关联了 %s;换仓库不是改一个字段的事,要单独迁移", cell.Spec.Repo.URL))
		return
	}
	// Same rule as creation: a credential is a Secret in the control
	// namespace, and pointing at one you do not own would borrow it.
	if in.SecretName != "" {
		if err := h.checkCredentialOwnership(r, in.SecretName); err != nil {
			writeErr(w, 404, err)
			return
		}
	}
	branch := strings.TrimSpace(in.Branch)
	if branch == "" {
		branch = "main"
	}
	cell.Spec.Repo = acv1.RepoSpec{URL: in.URL, Branch: branch, SecretName: in.SecretName}
	if err := validateRepoLayout(&cell); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := h.Client.Update(r.Context(), &cell); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"repo": cell.Spec.Repo})
}

// putRepoCredential changes WHICH credential this project uses, without
// touching which repository it points at.
//
// Separate from putRepo on purpose. Rotating a token, or moving a project
// from a hand-made shared credential onto your own, is routine and safe:
// the codebase is unchanged, so every clone and worktree stays valid. Only
// the repository URL is the thing that must not quietly move.
func (h *Handler) putRepoCredential(w http.ResponseWriter, r *http.Request) {
	var cell acv1.Cell
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: r.PathValue("cell")}, &cell); err != nil {
		writeErr(w, 404, errNotFound)
		return
	}
	if !h.authorize(w, r, &cell, ActionSettings) {
		return
	}
	var in struct {
		SecretName string `json:"secretName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, err)
		return
	}
	in.SecretName = strings.TrimSpace(in.SecretName)
	// Empty clears it, for a repository that needs no credential at all.
	if in.SecretName != "" {
		if err := h.checkCredentialOwnership(r, in.SecretName); err != nil {
			writeErr(w, 404, err)
			return
		}
	}
	cell.Spec.Repo.SecretName = in.SecretName
	if err := h.Client.Update(r.Context(), &cell); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"secretName": cell.Spec.Repo.SecretName})
}
