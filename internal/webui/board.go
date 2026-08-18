package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/identity"
	"github.com/zippo1908/agentcell/pkg/ids"
)

// The board: ask for work and get it back in the same place.
//
// `@cell 把商品卡片改成两列` dispatches a session to that Cell and posts the
// acknowledgement inline; when the session settles, the agent posts its
// branch back. `@u-…` addresses a person and shows up as unread for them.
//
// The rule inherited from AIP and worth keeping: an `@` that matches nothing
// NEVER silently does nothing. It answers, in the stream, saying what it
// could not find and what it would have accepted.

// mentionRe matches @token where token is a Cell name or a u- id.
var mentionRe = regexp.MustCompile(`@([a-z0-9][-a-z0-9]{0,62})`)

// botAliases are what people type when they mean "the agent" without
// knowing, or caring, which Cell that is.
var botAliases = []string{"@机器人", "@bot", "@agent", "@ai"}

type postView struct {
	ID       int64    `json:"id"`
	Kind     string   `json:"kind"`
	Author   string   `json:"author,omitempty"`
	Body     string   `json:"body"`
	Cell     string   `json:"cell,omitempty"`
	Session  string   `json:"session,omitempty"`
	At       string   `json:"at"`
	Mentions []string `json:"mentions,omitempty"`
	// Mine lets the UI align a post without knowing who the reader is.
	Mine bool `json:"mine,omitempty"`
}

func (h *Handler) boardName(cell string) types.NamespacedName {
	return types.NamespacedName{Namespace: h.Namespace, Name: "board-" + cell}
}

// boardFor loads a PROJECT's board, creating it on first use, after checking
// the caller may see that project.
//
// The board used to belong to a team, which meant this platform carried two
// membership models: the project's member list decided who gets a terminal,
// a preview and a release, while a separate team list decided who sees the
// conversation about that same work. Two answers to one question is how
// they drift apart — somebody in the team but off the project could read
// about work they cannot open. The project is the atom here, so it owns its
// conversation too.
func (h *Handler) boardFor(w http.ResponseWriter, r *http.Request) (*acv1.Cell, *acv1.Board, bool) {
	name := r.PathValue("cell")
	if name == "" {
		name = r.URL.Query().Get("cell")
	}
	if name == "" {
		writeErr(w, 400, fmt.Errorf("要先选一个项目"))
		return nil, nil, false
	}
	var t acv1.Cell
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: name}, &t); err != nil {
		writeErr(w, 404, errNotFound)
		return nil, nil, false
	}
	if !h.authorize(w, r, &t, ActionView) {
		return nil, nil, false
	}
	var b acv1.Board
	key := h.boardName(t.Name)
	err := h.Client.Get(r.Context(), key, &b)
	switch {
	case err == nil:
	case apierrors.IsNotFound(err):
		b = acv1.Board{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}
		b.Spec.Cell = t.Name
		if err := h.Client.Create(r.Context(), &b); err != nil && !apierrors.IsAlreadyExists(err) {
			writeErr(w, 500, err)
			return nil, nil, false
		}
		// Re-read: on AlreadyExists somebody else created it, and our local
		// copy would be the empty one we just tried to write.
		if err := h.Client.Get(r.Context(), key, &b); err != nil {
			writeErr(w, 500, err)
			return nil, nil, false
		}
	default:
		writeErr(w, 500, err)
		return nil, nil, false
	}
	return &t, &b, true
}

func (h *Handler) listBoard(w http.ResponseWriter, r *http.Request) {
	_, b, ok := h.boardFor(w, r)
	if !ok {
		return
	}
	var after int64
	if v := r.URL.Query().Get("after"); v != "" {
		after, _ = strconv.ParseInt(v, 10, 64)
	}
	me := identity.FromContext(r.Context()).ID()
	out := []postView{}
	for _, p := range b.Spec.Posts {
		if p.ID <= after {
			continue
		}
		out = append(out, postView{
			ID: p.ID, Kind: string(p.Kind), Author: p.Author, Body: p.Body,
			Cell: p.Cell, Session: p.Session, At: p.At.UTC().Format("2006-01-02 15:04"),
			Mentions: p.Mentions, Mine: p.Author == me && p.Kind == acv1.PostUser,
		})
	}
	// Reading the stream IS reading it. A separate "mark read" is a button
	// people forget, and then a badge that lies.
	h.markBoardRead(r.Context(), b.Name, me)
	writeJSON(w, 200, map[string]any{"posts": out, "latest": b.Spec.NextID - 1})
}

func (h *Handler) markBoardRead(ctx context.Context, name, userID string) {
	if userID == "" {
		return
	}
	_ = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var b acv1.Board
		if err := h.Client.Get(ctx, types.NamespacedName{Namespace: h.Namespace, Name: name}, &b); err != nil {
			return nil
		}
		before := b.Spec.Read[userID]
		b.MarkRead(userID)
		if b.Spec.Read[userID] == before {
			return nil
		}
		return h.Client.Update(ctx, &b)
	})
}

func (h *Handler) postToBoard(w http.ResponseWriter, r *http.Request) {
	t, _, ok := h.boardFor(w, r)
	if !ok {
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	text := strings.TrimSpace(body.Body)
	if text == "" {
		writeErr(w, 400, fmt.Errorf("说点什么"))
		return
	}
	if len(text) > 4096 {
		writeErr(w, 400, fmt.Errorf("一条最多 4KB(现在 %d 字节);长内容发到会话里", len(text)))
		return
	}
	p := identity.FromContext(r.Context())

	users := h.resolveMentions(t, text)
	post := acv1.Post{
		Kind: acv1.PostUser, Author: p.ID(), Body: text, Mentions: users,
	}
	if err := h.appendPost(r.Context(), t.Name, &post); err != nil {
		writeErr(w, 500, err)
		return
	}

	// Addressing the agent no longer needs a name. One board, one project,
	// one agent — so calling it is just saying so.
	if hasBotAlias(text) {
		h.dispatchFromBoard(r.Context(), t.Name, t.Name, text, p)
	}
	writeJSON(w, 201, map[string]any{"id": post.ID})
}

// appendPost adds a post under optimistic concurrency. Two people typing at
// once is normal, not an error.
func (h *Handler) appendPost(ctx context.Context, team string, p *acv1.Post) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var b acv1.Board
		key := h.boardName(team)
		if err := h.Client.Get(ctx, key, &b); err != nil {
			return err
		}
		*p = b.Append(*p)
		if p.Author != "" {
			// You have obviously seen your own post.
			b.MarkRead(p.Author)
		}
		return h.Client.Update(ctx, &b)
	})
}

func (h *Handler) systemPost(ctx context.Context, team, body, cell string) {
	p := acv1.Post{Kind: acv1.PostSystem, Body: body, Cell: cell}
	_ = h.appendPost(ctx, team, &p)
}

// teamCells lists the Cells a team governs.
func (h *Handler) teamCells(ctx context.Context, team string) []string {
	var list acv1.CellList
	if err := h.Client.List(ctx, &list, client.InNamespace(h.Namespace)); err != nil {
		return nil
	}
	out := []string{}
	for i := range list.Items {
		if list.Items[i].Spec.Team == team {
			out = append(out, list.Items[i].Name)
		}
	}
	sort.Strings(out)
	return out
}

// resolveMentions scans the body against two small known sets — the team's
// Cells and the team's members — rather than parsing freely. An unknown
// token is not a mention; it is just text with an @ in it.
// resolveMentions finds the people named in a post.
//
// Only people: the board belongs to one project now, so there is no second
// project it could be addressed to. Naming which agent to talk to was a
// question a team-wide board had to ask; here the answer is the project the
// board is on.
func (h *Handler) resolveMentions(t *acv1.Cell, body string) (users []string) {
	member := map[string]bool{}
	for _, m := range t.Spec.Members {
		member[m.UserID] = true
	}
	seen := map[string]bool{}
	for _, m := range mentionRe.FindAllStringSubmatch(body, -1) {
		tok := m[1]
		if member[tok] && !seen[tok] {
			seen[tok] = true
			users = append(users, tok)
		}
	}
	return users
}

func hasBotAlias(body string) bool {
	low := strings.ToLower(body)
	for _, a := range botAliases {
		if strings.Contains(low, a) {
			return true
		}
	}
	return false
}

// dispatchFromBoard turns "@cell do the thing" into a session.
//
// It answers in the stream whatever happens. A dispatch that cannot start —
// no credential, no permission, the Cell full — is a thing the asker needs
// told, and the board is where they are looking.
func (h *Handler) dispatchFromBoard(ctx context.Context, team, cell, text string, p identity.Principal) {
	task := strings.TrimSpace(mentionRe.ReplaceAllString(text, ""))
	if task == "" {
		h.systemPost(ctx, team, "@"+cell+" 收到,但没说要做什么。", cell)
		return
	}
	var c acv1.Cell
	if err := h.Client.Get(ctx, types.NamespacedName{Namespace: h.Namespace, Name: cell}, &c); err != nil {
		h.systemPost(ctx, team, "找不到工作区 "+cell+"。", "")
		return
	}
	if !can(p, &c, ActionDispatch) {
		h.systemPost(ctx, team, "你在 "+cell+" 里没有动手的权限。", cell)
		return
	}
	cred, err := h.soleCredential(ctx, p)
	if err != nil {
		h.systemPost(ctx, team, err.Error(), cell)
		return
	}

	// Same rule as the Cell page: one live session per person per project.
	// A board ask continues the conversation they already have there.
	// The board's conversation with a project belongs to the TEAM, not to
	// whoever happened to ask. Otherwise the first person to type would lend
	// out their private terminal, and the second would be answered inside
	// somebody else's session.
	owner := TeamOwnerPrefix + team
	if live, err := liveSessionFor(ctx, h.Client, h.Namespace, cell, owner); err == nil && live != nil {
		if err := h.queueFollowUp(ctx, live, task); err != nil {
			h.systemPost(ctx, team, "接不上你在 "+cell+" 的会话:"+err.Error(), cell)
			return
		}
		ack := acv1.Post{
			Kind: acv1.PostAgent, Author: cell, Cell: cell, Session: live.Name,
			Body: "接着你在 " + cell + " 的会话说:" + task,
		}
		_ = h.appendPost(ctx, team, &ack)
		return
	}
	sess := &acv1.Session{ObjectMeta: metav1.ObjectMeta{
		Namespace: h.Namespace, Name: ids.SessionName(ids.NewSessionID()),
	}}
	sess.Spec.Cell = cell
	sess.Spec.Task = task
	runner, provider, model := c.Spec.Defaults.Runner, c.Spec.Defaults.Provider, c.Spec.Defaults.Model
	if runner == "" || provider == "" {
		// Fall back to what this Cell was last dispatched with, then say so
		// plainly if there is nothing to follow. Guessing a vendor would
		// spend somebody's budget somewhere they never chose.
		var err error
		runner, provider, model, err = h.providerFor(ctx, cell)
		if err != nil {
			h.systemPost(ctx, team, err.Error(), cell)
			return
		}
	}
	sess.Spec.Runner, sess.Spec.Provider, sess.Spec.Model = runner, provider, model
	sess.Spec.CredentialSecret = cred
	sess.Spec.OwnerUserID = owner
	sess.Spec.Board = team
	if err := h.Client.Create(ctx, sess); err != nil {
		h.systemPost(ctx, team, "派不出去:"+err.Error(), cell)
		return
	}
	ack := acv1.Post{
		Kind: acv1.PostAgent, Author: cell, Cell: cell, Session: sess.Name,
		Body: "接了:" + task,
	}
	_ = h.appendPost(ctx, team, &ack)
}

// soleCredential picks the caller's model key when there is exactly one.
//
// Guessing between several would spend the wrong budget against the wrong
// vendor, so more than one is a question, not a coin flip — and the answer
// goes in the stream where it was asked.
func (h *Handler) soleCredential(ctx context.Context, p identity.Principal) (string, error) {
	var list corev1.SecretList
	if err := h.Client.List(ctx, &list,
		client.InNamespace(h.Namespace),
		// Model keys only: a connected account is a credential, but it is not
		// a key you can be asked to choose between.
		client.MatchingLabels{credLabel: credKindModel}); err != nil {
		return "", fmt.Errorf("读不到凭据:%w", err)
	}
	mine := []string{}
	for i := range list.Items {
		if p.Owns(list.Items[i].Labels[OwnerLabel]) {
			mine = append(mine, list.Items[i].Name)
		}
	}
	sort.Strings(mine)
	switch len(mine) {
	case 0:
		return "", fmt.Errorf("你还没有配模型 key——去「我的凭据」加一个再来。")
	case 1:
		return mine[0], nil
	default:
		return "", fmt.Errorf("你有好几把 key(%s),黑板不替你挑;这一单去工作区里派,或者只留一把。",
			strings.Join(mine, "、"))
	}
}

// providerFor picks a provider for a board dispatch.
//
// The board is for quick asks, so it must not turn into the dispatch form.
// It reuses what this Cell was last dispatched with — the pairing somebody
// already chose deliberately — and only falls back to asking when there is
// no precedent to follow. Hard-coding a vendor here would quietly spend
// somebody's budget somewhere they never picked.
func (h *Handler) providerFor(ctx context.Context, cell string) (runner, provider, model string, err error) {
	var list acv1.SessionList
	if err := h.Client.List(ctx, &list, client.InNamespace(h.Namespace)); err != nil {
		return "", "", "", err
	}
	var newest *acv1.Session
	for i := range list.Items {
		s := &list.Items[i]
		if s.Spec.Cell != cell || s.Spec.Provider == "" {
			continue
		}
		if newest == nil || s.CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = s
		}
	}
	if newest == nil {
		return "", "", "", fmt.Errorf(
			"这个工作区还没派过工,黑板不替你挑 runner 和模型——先在工作区里派一单,之后 @ 它就会沿用那次的选择。")
	}
	return newest.Spec.Runner, newest.Spec.Provider, newest.Spec.Model, nil
}
