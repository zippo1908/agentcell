package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zippo1908/agentcell/internal/identity"
	"github.com/zippo1908/agentcell/internal/store"
)

// Lending a key.
//
// A new colleague cannot do anything at all until they can pay for a turn:
// dispatch refuses before it even looks at the project, with "你还没有配模型
// key". The two answers were "add your own key" and "connect your own
// account" — both fine, and both a wall on somebody's first afternoon.
//
// So a key can be lent. The `grants` table has existed since the first
// release with full CRUD and no callers; this is what it was for.
//
// A static API key is the easy case: the same string every time, so a grant
// row is all it takes and a borrower spends it exactly as an owner would.
//
// A connected OAuth account needs one more step: the session controller
// looks for a Secret named after the SESSION'S OWNER, so the borrower needs
// their own copy under their own name. A grant row alone lends nothing.
//
// This was gated behind a scary warning on the theory that a rotating
// refresh token would make the two copies knock each other offline. MEASURED
// on 2026-08-19 against api.kimi.com: it does not. Two runtimes refreshed
// from the same old token minutes apart and both succeeded — the provider
// issues a new refresh token without invalidating the old. The copies simply
// drift into separate lineages, which is waste rather than breakage.
//
// The copy is still labelled with who lent it. n=1 is enough to drop a
// warning nobody could act on; it is not enough to make a shared login
// untraceable when somebody eventually is logged out.

type grantInput struct {
	// Credential is the Secret's name — which key, not which vendor.
	Credential string `json:"credential"`
	// Email names the person. A user id is accepted too, because that is
	// what scripts written before accounts existed already send.
	Email  string `json:"email"`
	UserID string `json:"userID"`
	// Acknowledge is required to lend a connected account, whose refresh
	// token rotates. It exists so that sharing something not designed to be
	// shared is a decision somebody made, not one they fell into.
	Acknowledge bool `json:"acknowledge"`
}

type lentView struct {
	Credential string `json:"credential"`
	Email      string `json:"email"`
	Name       string `json:"name,omitempty"`
	// Unknown marks a grantee no account matches — somebody removed from the
	// platform. Shown rather than hidden: a lent key nobody can name is
	// exactly the one that should be taken back.
	Unknown bool `json:"unknown,omitempty"`
}

type borrowedView struct {
	Credential string `json:"credential"`
	// From is the lender, by address, so "whose budget am I spending" has an
	// answer on the page rather than in somebody's memory.
	From string `json:"from"`
	Hint string `json:"hint,omitempty"`
}

func (h *Handler) listGrants(w http.ResponseWriter, r *http.Request) {
	db := h.accountsDB()
	if db == nil {
		writeJSON(w, 200, map[string]any{"lent": []lentView{}, "borrowed": []borrowedView{}})
		return
	}
	p := identity.FromContext(r.Context())
	byID := h.accountsByID(r)
	emailOf := map[string]accountLite{}
	for id, a := range byID {
		emailOf[id] = a
	}

	lent := []lentView{}
	if gs, err := db.GrantsBy(r.Context(), p.ID()); err == nil {
		for _, g := range gs {
			v := lentView{Credential: g.Credential}
			if a, ok := emailOf[g.GranteeID]; ok {
				v.Email, v.Name = a.email, a.name
			} else {
				v.Email, v.Unknown = g.GranteeID, true
			}
			lent = append(lent, v)
		}
	}

	borrowed := []borrowedView{}
	for _, g := range h.grantsToMe(r.Context(), p) {
		v := borrowedView{Credential: g.Credential}
		if a, ok := emailOf[g.GranterID]; ok {
			v.From = a.email
		} else {
			v.From = g.GranterID
		}
		var sec corev1.Secret
		if err := h.Client.Get(r.Context(),
			types.NamespacedName{Namespace: h.Namespace, Name: g.Credential}, &sec); err == nil {
			v.Hint = hint(sec.Data["key"])
		}
		borrowed = append(borrowed, v)
	}
	sort.Slice(lent, func(i, j int) bool { return lent[i].Credential < lent[j].Credential })
	sort.Slice(borrowed, func(i, j int) bool { return borrowed[i].Credential < borrowed[j].Credential })
	writeJSON(w, 200, map[string]any{
		"lent": lent, "borrowed": borrowed, "lendable": h.lendable(r, p),
	})
}

type lendableView struct {
	Name string `json:"name"`
	// Kind is "model" or "kimi-oauth". The UI needs it because lending the
	// second kind is a different decision, not a different button.
	Kind string `json:"kind"`
	Hint string `json:"hint,omitempty"`
}

// lendable lists what this person could hand to a colleague.
//
// The connected account is included rather than hidden. Hiding it would make
// the commonest question — "can I just share my login?" — unanswerable from
// the page, and people would go and copy the Secret by hand instead, which
// is the same hazard with none of the warning.
func (h *Handler) lendable(r *http.Request, p identity.Principal) []lendableView {
	var list corev1.SecretList
	out := []lendableView{}
	if err := h.Client.List(r.Context(), &list, client.InNamespace(h.Namespace)); err != nil {
		return out
	}
	for i := range list.Items {
		s := &list.Items[i]
		kind := s.Labels[credLabel]
		if kind != credKindModel && kind != credKindKimi {
			continue
		}
		if !p.Owns(s.Labels[OwnerLabel]) {
			continue
		}
		// A borrowed account is not yours to lend on.
		if s.Labels["agentcell.io/lent-by"] != "" {
			continue
		}
		v := lendableView{Name: s.Name, Kind: kind}
		if kind == credKindModel {
			v.Hint = hint(s.Data["key"])
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// grantsToMe returns the credentials lent to this caller, or nothing on a
// deployment without accounts.
func (h *Handler) grantsToMe(ctx context.Context, p identity.Principal) []store.Grant {
	db := h.accountsDB()
	if db == nil {
		return nil
	}
	// Teams are gone as a scope; only personal grants are read.
	gs, err := db.GrantsTo(ctx, p.ID(), nil)
	if err != nil {
		return nil
	}
	return gs
}

func (h *Handler) createGrant(w http.ResponseWriter, r *http.Request) {
	db := h.accountsDB()
	if db == nil {
		writeErr(w, 400, fmt.Errorf("这个部署没有开启账号体系,没法把 key 借给谁"))
		return
	}
	var in grantInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, err)
		return
	}
	in.Credential = strings.TrimSpace(in.Credential)
	if in.Credential == "" {
		writeErr(w, 400, fmt.Errorf("要借哪一把 key"))
		return
	}
	p := identity.FromContext(r.Context())

	// You may only lend what is yours, and only a key.
	var sec corev1.Secret
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: in.Credential}, &sec); err != nil {
		writeErr(w, 404, errNotFound)
		return
	}
	if !p.Owns(sec.Labels[OwnerLabel]) {
		writeErr(w, 404, errNotFound)
		return
	}
	kind := sec.Labels[credLabel]
	if kind != credKindModel && kind != credKindKimi {
		writeErr(w, 404, errNotFound)
		return
	}
	// Acknowledge is no longer a gate. It stays in the request shape because
	// callers written against the gated version still send it, and because a
	// deployment that wants the confirmation back can reinstate it in one
	// line — but the hazard it warned about was measured and did not exist.
	_ = in.Acknowledge

	to, err := h.granteeID(r, in)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if to == p.ID() {
		writeErr(w, 400, fmt.Errorf("这把 key 本来就是你的"))
		return
	}
	if err := db.CreateGrant(r.Context(), store.Grant{
		GranterID: p.ID(), GranteeKind: store.GranteeUser, GranteeID: to, Credential: in.Credential,
	}); err != nil {
		writeErr(w, 500, err)
		return
	}
	// A model key is chosen per dispatch, so a grant row is enough. A
	// connected account is not chosen at all — the session controller looks
	// for a Secret named after the SESSION'S OWNER — so for the borrower to
	// have one, they need their own copy under their own name.
	if kind == credKindKimi {
		if err := h.projectAccount(r, sec, to, p.ID()); err != nil {
			writeErr(w, 500, fmt.Errorf("借出记下了,但没能把账号交到对方名下:%w", err))
			return
		}
	}
	writeJSON(w, 201, map[string]any{"credential": in.Credential, "grantee": to, "kind": kind})
}

func (h *Handler) deleteGrant(w http.ResponseWriter, r *http.Request) {
	db := h.accountsDB()
	if db == nil {
		writeErr(w, 400, fmt.Errorf("这个部署没有开启账号体系"))
		return
	}
	p := identity.FromContext(r.Context())
	to, err := h.granteeID(r, grantInput{Email: r.PathValue("who")})
	if err != nil {
		// Taking a grant back must work even for somebody whose account is
		// gone — otherwise the one grant most worth revoking is the one that
		// cannot be. Fall back to the raw id in the path.
		to = r.PathValue("who")
	}
	if err := db.DeleteGrant(r.Context(), store.Grant{
		GranterID: p.ID(), GranteeKind: store.GranteeUser,
		GranteeID: to, Credential: r.PathValue("credential"),
	}); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revoked": r.PathValue("credential")})
}

func (h *Handler) granteeID(r *http.Request, in grantInput) (string, error) {
	if in.UserID != "" {
		return in.UserID, nil
	}
	who := strings.TrimSpace(in.Email)
	if who == "" {
		return "", fmt.Errorf("借给谁?给个邮箱")
	}
	if strings.Contains(who, "@") {
		db := h.accountsDB()
		if db == nil {
			return "", fmt.Errorf("这个部署没有开启账号体系,只能用 userID")
		}
		if _, _, err := db.UserByEmail(r.Context(), who); err != nil {
			return "", fmt.Errorf("平台上没有这个人——先邀请 %s", who)
		}
		return identity.Principal{Subject: identity.UserSubject(who)}.ID(), nil
	}
	return who, nil
}

// mayUseCredential reports whether this caller may SPEND a credential —
// because it is theirs, or because somebody lent it to them.
//
// Ownership alone was the rule, which is why a colleague could be handed a
// project and still not be able to start anything in it.
func (h *Handler) mayUseCredential(r *http.Request, name string) error {
	if name == "" {
		return nil
	}
	if err := h.checkCredentialOwnership(r, name); err == nil {
		return nil
	}
	p := identity.FromContext(r.Context())
	for _, g := range h.grantsToMe(r.Context(), p) {
		if g.Credential == name {
			return nil
		}
	}
	return errNotFound
}

// spendableCredentials lists every key this caller may spend, owned first.
// Used where the platform has to pick one without asking.
func (h *Handler) spendableCredentials(ctx context.Context, p identity.Principal) []string {
	var list corev1.SecretList
	out := []string{}
	if err := h.Client.List(ctx, &list,
		client.InNamespace(h.Namespace),
		client.MatchingLabels{credLabel: credKindModel}); err == nil {
		for i := range list.Items {
			if p.Owns(list.Items[i].Labels[OwnerLabel]) {
				out = append(out, list.Items[i].Name)
			}
		}
	}
	sort.Strings(out)
	seen := map[string]bool{}
	for _, n := range out {
		seen[n] = true
	}
	for _, g := range h.grantsToMe(ctx, p) {
		if seen[g.Credential] {
			continue
		}
		// A grant naming a Secret that has since been deleted is a dangling
		// row, not a usable key.
		var sec corev1.Secret
		if err := h.Client.Get(ctx,
			types.NamespacedName{Namespace: h.Namespace, Name: g.Credential}, &sec); err != nil {
			continue
		}
		if sec.Labels[credLabel] != credKindModel {
			continue
		}
		seen[g.Credential] = true
		out = append(out, g.Credential)
	}
	return out
}

// projectAccount copies a connected account under the borrower's own name,
// which is where the session controller looks for it.
//
// It is a COPY, and that is the whole hazard: the provider rotates the
// refresh token, so the two copies drift apart the moment either side
// refreshes, and the loser is told only "re-login required". The copy is
// labelled with who lent it so that when this happens, the reason is
// readable from the object rather than reconstructed from memory.
func (h *Handler) projectAccount(r *http.Request, from corev1.Secret, toUserID, lender string) error {
	name := strings.TrimPrefix(toUserID, "u-") + KimiCredentialSuffix
	var existing corev1.Secret
	err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: name}, &existing)
	labels := map[string]string{
		credLabel:  credKindKimi,
		OwnerLabel: toUserID,
		// Not decoration: a borrowed login that stops working is diagnosed by
		// knowing it was borrowed.
		"agentcell.io/lent-by": lender,
	}
	if apierrors.IsNotFound(err) {
		return h.Client.Create(r.Context(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: h.Namespace, Name: name, Labels: labels},
			Data:       map[string][]byte{kimiCredKey: from.Data[kimiCredKey]},
		})
	}
	if err != nil {
		return err
	}
	// Never overwrite somebody's OWN connected account with a borrowed one:
	// they would be logged out of their own login by a colleague's gift.
	if existing.Labels["agentcell.io/lent-by"] == "" {
		return fmt.Errorf("对方已经自己连了账号,不覆盖")
	}
	existing.Labels = labels
	if existing.Data == nil {
		existing.Data = map[string][]byte{}
	}
	existing.Data[kimiCredKey] = from.Data[kimiCredKey]
	return h.Client.Update(r.Context(), &existing)
}
