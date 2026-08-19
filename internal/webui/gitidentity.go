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

	"github.com/zippo1908/agentcell/internal/identity"
	"github.com/zippo1908/agentcell/internal/store"
)

// A person's own forge token.
//
// The storage for this was written and tested and then never connected to
// anything, so the console had no way to say "this is my GitLab account" and
// every project had to borrow a credential somebody else had created by hand.
//
// Two places hold it, on purpose:
//
//   - the accounts database is the truth. It belongs to the person, not to a
//     namespace, and it outlives any project they use it on.
//   - a basic-auth Secret is the PROJECTION the rest of the platform already
//     knows how to consume — the credential picker lists owned basic-auth
//     Secrets, the controller copies one into a workload namespace. Writing
//     the projection means this feature works without teaching four other
//     components a new concept.
//
// Deleting removes both. A projection that outlives its source is a token
// nobody believes they still have.

// gitProviders are the forges a token may be bound for. A closed list
// because each one has to be a name the git credential helper will match.
var gitProviders = map[string]bool{"gitlab": true, "github": true}

type gitIdentityInput struct {
	Provider string `json:"provider"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

type gitIdentityView struct {
	Provider string `json:"provider"`
	Username string `json:"username"`
	// SecretName is what the credential picker calls it, so somebody can see
	// that the entry in that list and this binding are the same thing.
	SecretName string `json:"secretName"`
}

// accountsDB returns the store, or nil on a deployment with no accounts.
func (h *Handler) accountsDB() *store.DB {
	if h.Auth == nil || h.Auth.Accounts == nil {
		return nil
	}
	return h.Auth.Accounts.DB
}

func (h *Handler) listGitIdentities(w http.ResponseWriter, r *http.Request) {
	db := h.accountsDB()
	if db == nil {
		writeJSON(w, 200, map[string]any{"identities": []gitIdentityView{}})
		return
	}
	p := identity.FromContext(r.Context())
	got, err := db.GitProviders(r.Context(), p.ID())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	out := make([]gitIdentityView, 0, len(got))
	for provider, username := range got {
		out = append(out, gitIdentityView{
			Provider: provider, Username: username,
			SecretName: gitIdentitySecretName(p.ID(), provider),
		})
	}
	writeJSON(w, 200, map[string]any{"identities": out})
}

func (h *Handler) putGitIdentity(w http.ResponseWriter, r *http.Request) {
	db := h.accountsDB()
	if db == nil {
		writeErr(w, 400, fmt.Errorf("这个部署没有开启账号体系,个人令牌无处可存"))
		return
	}
	var in gitIdentityInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, err)
		return
	}
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	in.Username = strings.TrimSpace(in.Username)
	in.Token = strings.TrimSpace(in.Token)
	if !gitProviders[in.Provider] {
		writeErr(w, 400, fmt.Errorf("只支持 gitlab 或 github"))
		return
	}
	if in.Username == "" || in.Token == "" {
		// Both, because a basic-auth credential with an empty half fails at
		// clone time with an authentication error that says nothing about
		// which half was missing.
		writeErr(w, 400, fmt.Errorf("用户名和令牌都要填"))
		return
	}
	p := identity.FromContext(r.Context())
	if err := db.SetGitIdentity(r.Context(), p.ID(),
		store.GitIdentity{Provider: in.Provider, Username: in.Username, Token: in.Token}); err != nil {
		writeErr(w, 500, err)
		return
	}
	if err := h.projectGitIdentity(r, p.ID(), in); err != nil {
		// The truth is stored; the projection is not. Say so rather than
		// reporting success for a binding no project can actually use.
		writeErr(w, 500, fmt.Errorf("令牌已存,但没能生成给项目用的凭据:%w", err))
		return
	}
	writeJSON(w, 200, gitIdentityView{
		Provider: in.Provider, Username: in.Username,
		SecretName: gitIdentitySecretName(p.ID(), in.Provider),
	})
}

func (h *Handler) deleteGitIdentity(w http.ResponseWriter, r *http.Request) {
	db := h.accountsDB()
	if db == nil {
		writeErr(w, 400, fmt.Errorf("这个部署没有开启账号体系"))
		return
	}
	provider := strings.ToLower(r.PathValue("provider"))
	p := identity.FromContext(r.Context())
	if err := db.DeleteGitIdentity(r.Context(), p.ID(), provider); err != nil {
		writeErr(w, 500, err)
		return
	}
	name := gitIdentitySecretName(p.ID(), provider)
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: h.Namespace, Name: name}}
	if err := h.Client.Delete(r.Context(), sec); err != nil && !apierrors.IsNotFound(err) {
		writeErr(w, 500, fmt.Errorf("令牌已删,但凭据 %s 还在:%w", name, err))
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": provider})
}

// projectGitIdentity writes the basic-auth Secret the rest of the platform
// consumes, owned by the person it belongs to so nobody else can point a
// project at it.
func (h *Handler) projectGitIdentity(r *http.Request, userID string, in gitIdentityInput) error {
	name := gitIdentitySecretName(userID, in.Provider)
	var sec corev1.Secret
	err := h.Client.Get(r.Context(), types.NamespacedName{Namespace: h.Namespace, Name: name}, &sec)
	switch {
	case apierrors.IsNotFound(err):
		sec = corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: h.Namespace, Name: name,
				Labels: map[string]string{OwnerLabel: userID},
			},
			Type: corev1.SecretTypeBasicAuth,
			Data: map[string][]byte{
				corev1.BasicAuthUsernameKey: []byte(in.Username),
				corev1.BasicAuthPasswordKey: []byte(in.Token),
			},
		}
		return h.Client.Create(r.Context(), &sec)
	case err != nil:
		return err
	}
	// Never adopt a Secret of this name that belongs to somebody else: the
	// name is derived from a user id, but a hand-made Secret could occupy it.
	if sec.Labels[OwnerLabel] != userID {
		return fmt.Errorf("凭据名 %s 已被别的东西占用", name)
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	sec.Type = corev1.SecretTypeBasicAuth
	sec.Data[corev1.BasicAuthUsernameKey] = []byte(in.Username)
	sec.Data[corev1.BasicAuthPasswordKey] = []byte(in.Token)
	return h.Client.Update(r.Context(), &sec)
}

// gitIdentitySecretName is derived, never chosen: two people must not be
// able to argue over one name, and the owner must be readable from it.
func gitIdentitySecretName(userID, provider string) string {
	return strings.TrimPrefix(userID, "u-") + "-" + provider
}
