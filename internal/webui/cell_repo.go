package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/identity"
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
		// Or type one here. A project page that can only CHOOSE a credential
		// is useless to the person who has none — and telling them to go to
		// another page, create one, and come back is the shape this platform
		// keeps trying to remove.
		Username string `json:"username"`
		Token    string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, err)
		return
	}
	in.SecretName = strings.TrimSpace(in.SecretName)
	in.Username = strings.TrimSpace(in.Username)
	in.Token = strings.TrimSpace(in.Token)

	if in.Token != "" {
		if in.Username == "" {
			// Both, or the clone fails later with an authentication error
			// that says nothing about which half was missing.
			writeErr(w, 400, fmt.Errorf("用户名和令牌都要填"))
			return
		}
		name, err := h.putProjectCredential(r, cell.Name, in.Username, in.Token)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		in.SecretName = name
	} else if in.SecretName != "" {
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

// putProjectCredential stores a token entered on a project's own page.
//
// Named after the project rather than the person, because that is what it
// is: the credential THIS project clones and pushes with. A personal forge
// identity (bound in 我的凭据) is a different thing and keeps its own name —
// somebody may well want a deploy token here and their own identity there.
//
// Owned by whoever entered it, so the same ownership rule that governs
// choosing a credential governs this one.
func (h *Handler) putProjectCredential(r *http.Request, cell, username, token string) (string, error) {
	name := cell + "-git"
	p := identity.FromContext(r.Context())
	var sec corev1.Secret
	err := h.Client.Get(r.Context(), types.NamespacedName{Namespace: h.Namespace, Name: name}, &sec)
	switch {
	case apierrors.IsNotFound(err):
		return name, h.Client.Create(r.Context(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: h.Namespace, Name: name,
				Labels: map[string]string{OwnerLabel: p.ID()},
			},
			Type: corev1.SecretTypeBasicAuth,
			Data: map[string][]byte{
				corev1.BasicAuthUsernameKey: []byte(username),
				corev1.BasicAuthPasswordKey: []byte(token),
			},
		})
	case err != nil:
		return "", err
	}
	// Never adopt a Secret of this name that belongs to somebody else: the
	// name is derived from the project, and a hand-made Secret could be
	// sitting on it.
	if !p.Owns(sec.Labels[OwnerLabel]) {
		return "", fmt.Errorf("凭据名 %s 已被别的东西占用", name)
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	sec.Type = corev1.SecretTypeBasicAuth
	sec.Data[corev1.BasicAuthUsernameKey] = []byte(username)
	sec.Data[corev1.BasicAuthPasswordKey] = []byte(token)
	return name, h.Client.Update(r.Context(), &sec)
}
