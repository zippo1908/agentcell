package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zippo1908/agentcell/internal/identity"
)

// Model credentials, managed by the person who spends them.
//
// They were Kubernetes Secrets created with kubectl, which means "bring your
// own key" required cluster access — so a colleague could be given a console
// and still not be able to do the one thing that makes it useful. Same shape
// of gap as membership management: documented as a user's own credential,
// reachable only by an administrator.

// credLabel marks the Secrets this API is allowed to touch. Anything without
// it — the git credential, the console's own tokens — is invisible here and
// cannot be created, overwritten or deleted through it. That is the whole
// safety property: a user managing their key must not be one name collision
// away from replacing the platform's.
const credLabel = "agentcell.io/credential"

// credKindModel is the label VALUE for a model key — the only kind this API
// manages. The label's value has always carried a kind, but every query used
// to match on the label merely EXISTING, so the first non-key credential to
// arrive (a connected Kimi account) showed up here as a nameless key with no
// hint, and made its owner look like somebody holding two keys: the board
// then refused to dispatch, because picking between keys is a question it
// will not answer for you. Connecting an account must not cost you the
// ability to dispatch.
const credKindModel = "model"

var credName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

type credView struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
	// Hint is the last four characters, so a person with three keys can tell
	// which is which. The key itself is never returned — it goes in and is
	// only ever read by a session pod.
	Hint    string `json:"hint"`
	Created string `json:"created"`
}

func (h *Handler) listCredentials(w http.ResponseWriter, r *http.Request) {
	var list corev1.SecretList
	if err := h.Client.List(r.Context(), &list,
		client.InNamespace(h.Namespace),
		client.MatchingLabels{credLabel: credKindModel}); err != nil {
		writeErr(w, 500, err)
		return
	}
	p := identity.FromContext(r.Context())
	out := []credView{}
	for i := range list.Items {
		s := &list.Items[i]
		if !p.Owns(s.Labels[OwnerLabel]) {
			continue
		}
		out = append(out, credView{
			Name:    s.Name,
			Owner:   s.Labels[OwnerLabel],
			Hint:    hint(s.Data["key"]),
			Created: s.CreationTimestamp.UTC().Format("2006-01-02 15:04"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, 200, out)
}

// hint shows just enough to tell two keys apart.
func hint(key []byte) string {
	if len(key) <= 4 {
		return "…"
	}
	return "…" + string(key[len(key)-4:])
}

func (h *Handler) putCredential(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !credName.MatchString(name) || len(name) > 63 {
		writeErr(w, 400, fmt.Errorf("name must be lowercase letters, digits and dashes"))
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.Key == "" {
		writeErr(w, 400, fmt.Errorf("key is required"))
		return
	}
	p := identity.FromContext(r.Context())

	var existing corev1.Secret
	err := h.Client.Get(r.Context(), types.NamespacedName{Namespace: h.Namespace, Name: name}, &existing)
	switch {
	case err == nil:
		// The name is taken. If it is not a credential this API manages, or
		// not this caller's, refuse — and refuse the SAME way in both cases,
		// so the response does not reveal which platform Secrets exist.
		if existing.Labels[credLabel] != credKindModel || !p.Owns(existing.Labels[OwnerLabel]) {
			writeErr(w, 409, fmt.Errorf("that name is taken"))
			return
		}
		existing.Data = map[string][]byte{"key": []byte(body.Key)}
		if err := h.Client.Update(r.Context(), &existing); err != nil {
			writeErr(w, 500, err)
			return
		}
	case apierrors.IsNotFound(err):
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Namespace: h.Namespace, Name: name,
			Labels: map[string]string{credLabel: credKindModel, OwnerLabel: p.ID()},
		}}
		sec.Data = map[string][]byte{"key": []byte(body.Key)}
		if err := h.Client.Create(r.Context(), sec); err != nil {
			writeErr(w, 500, err)
			return
		}
	default:
		writeErr(w, 500, err)
		return
	}
	// Never echo the key back, not even to the person who just sent it: it
	// would then live in a response body, a proxy log and a browser cache.
	writeJSON(w, 200, map[string]string{"name": name, "hint": hint([]byte(body.Key))})
}

func (h *Handler) deleteCredential(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var sec corev1.Secret
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: name}, &sec); err != nil {
		writeErr(w, 404, errNotFound)
		return
	}
	p := identity.FromContext(r.Context())
	if sec.Labels[credLabel] != credKindModel || !p.Owns(sec.Labels[OwnerLabel]) {
		// Not yours, or not ours to manage — indistinguishable from absent.
		writeErr(w, 404, errNotFound)
		return
	}
	if err := h.Client.Delete(r.Context(), &sec); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "deleted"})
}
