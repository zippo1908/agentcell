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

// mentionRe matches @token where token is a Cell name, a u- id, or the way a
// person is actually named — the local part of their address, or the whole
// address when two colleagues share one.
//
// It used to match only `[a-z0-9][-a-z0-9]*`, which in practice meant the
// hashed user id. Nobody types `@u-9f3a1c…`, so in practice nobody addressed
// anybody: the feature existed and was unreachable. The composer now offers
// a picker, and what the picker inserts has to be something a human can also
// type and read back afterwards.
var mentionRe = regexp.MustCompile(`@([A-Za-z0-9][-A-Za-z0-9._]{0,62}(?:@[A-Za-z0-9][-A-Za-z0-9.]{0,62})?)`)

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

	users := h.resolveMentions(r.Context(), t, text)
	post := acv1.Post{
		Kind: acv1.PostUser, Author: p.ID(), Body: text, Mentions: users,
	}
	if err := h.appendPost(r.Context(), t.Name, &post); err != nil {
		writeErr(w, 500, err)
		return
	}

	// Addressing the agent no longer needs a name. One board, one project,
	// one agent — so calling it is just saying so.
	askID := ""
	if hasBotAlias(text) {
		task := strings.TrimSpace(mentionRe.ReplaceAllString(text, ""))
		// quiet: the streamed answer replaces the "接了" ack — two messages
		// for one ask is noise, and the bubble already says it is coming.
		// The ask is registered only when the dispatch actually started:
		// otherwise the asker would hold a stream that waits out its whole
		// deadline for a session the board already said could not start.
		if h.dispatchFromBoard(r.Context(), t.Name, t.Name, text, p, true) && task != "" {
			askID = h.asks.put(askEntry{Cell: t.Name, Task: task, Asker: p.ID()})
		}
	}

	// The rule at the top of this file, finally applied to people too: an @
	// that matched nobody used to just not be in Mentions, and the writer had
	// no way to tell the difference between "delivered" and "typed the name
	// slightly wrong". Now it answers in the stream.
	if miss := h.unresolvedMentions(r.Context(), t, text, users); len(miss) > 0 {
		h.systemPost(r.Context(), t.Name,
			"没找到这些人:@"+strings.Join(miss, " @")+" —— 只能 @ 这个项目的成员。输入 @ 会列出可选的人。",
			t.Name)
	}
	resp := map[string]any{"id": post.ID}
	if askID != "" {
		resp["ask"] = askID
	}
	writeJSON(w, 201, resp)
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
func (h *Handler) resolveMentions(ctx context.Context, t *acv1.Cell, body string) (users []string) {
	member := map[string]bool{}
	for _, m := range t.Spec.Members {
		member[m.UserID] = true
	}
	// How a person may be written: their id, their whole address, or the
	// local part of it. The last one is what people actually type, and it is
	// only offered when it is unambiguous among THIS project's members —
	// delivering "@li" to the wrong Li is worse than not delivering it.
	byName := map[string]string{}
	ambiguous := map[string]bool{}
	for _, u := range h.accountsForMentions(ctx) {
		id := principalIDFor(ctx, h.accountsDB(), u.email)
		if !member[id] {
			continue
		}
		full := strings.ToLower(u.email)
		byName[full] = id
		local := full
		if i := strings.IndexByte(full, '@'); i > 0 {
			local = full[:i]
		}
		if prev, dup := byName[local]; dup && prev != id {
			ambiguous[local] = true
		}
		byName[local] = id
	}
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			users = append(users, id)
		}
	}
	for _, m := range mentionRe.FindAllStringSubmatch(body, -1) {
		tok := m[1]
		if member[tok] {
			add(tok)
			continue
		}
		low := strings.ToLower(tok)
		if ambiguous[low] {
			// Deliberately not delivered, and deliberately not silent: the
			// caller reports what it could not resolve.
			continue
		}
		add(byName[low])
	}
	return users
}

// unresolvedMentions returns the @tokens that named nobody.
//
// Things that are legitimately not people are excluded: the bot aliases, and
// the project's own name (which is how the board has always addressed the
// agent). Everything left over is somebody the writer meant to reach and did
// not — which is exactly what they need told.
func (h *Handler) unresolvedMentions(ctx context.Context, t *acv1.Cell, body string, resolved []string) []string {
	if len(t.Spec.Members) == 0 {
		// An open project has no member list to check against, so there is
		// nothing here that can be called wrong.
		return nil
	}
	known := map[string]bool{}
	for _, id := range resolved {
		known[id] = true
	}
	member := map[string]bool{}
	for _, m := range t.Spec.Members {
		member[m.UserID] = true
	}
	byName := map[string]bool{}
	for _, u := range h.accountsForMentions(ctx) {
		id := principalIDFor(ctx, h.accountsDB(), u.email)
		if !member[id] {
			continue
		}
		full := strings.ToLower(u.email)
		byName[full] = true
		if i := strings.IndexByte(full, '@'); i > 0 {
			byName[full[:i]] = true
		}
	}
	var miss []string
	seen := map[string]bool{}
	for _, m := range mentionRe.FindAllStringSubmatch(body, -1) {
		tok := m[1]
		low := strings.ToLower(tok)
		switch {
		case seen[low], known[tok], byName[low], member[tok]:
		case low == strings.ToLower(t.Name):
		case low == "bot" || low == "agent" || low == "ai":
		default:
			seen[low] = true
			miss = append(miss, tok)
		}
	}
	return miss
}

// accountsForMentions lists the deployment's people, or nothing at all on a
// deployment without accounts — where mentions can only ever be ids.
func (h *Handler) accountsForMentions(ctx context.Context) []accountLite {
	db := h.accountsDB()
	if db == nil {
		return nil
	}
	users, err := db.Users(ctx)
	if err != nil {
		return nil
	}
	out := make([]accountLite, 0, len(users))
	for _, u := range users {
		out = append(out, accountLite{u.Email, u.Name})
	}
	return out
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
// told, and the board is where they are looking. quiet suppresses the "接了"
// ack (the caller is about to stream the answer itself, and the ack would be
// a second message saying less). The return reports whether a session was
// started or continued — the failure posts are already on the board either
// way, so callers need only the fact.
func (h *Handler) dispatchFromBoard(ctx context.Context, team, cell, text string, p identity.Principal, quiet bool) bool {
	task := strings.TrimSpace(mentionRe.ReplaceAllString(text, ""))
	if task == "" {
		h.systemPost(ctx, team, "@"+cell+" 收到,但没说要做什么。", cell)
		return false
	}
	var c acv1.Cell
	if err := h.Client.Get(ctx, types.NamespacedName{Namespace: h.Namespace, Name: cell}, &c); err != nil {
		h.systemPost(ctx, team, "找不到工作区 "+cell+"。", "")
		return false
	}
	if !can(p, &c, ActionDispatch) {
		h.systemPost(ctx, team, "你在 "+cell+" 里没有动手的权限。", cell)
		return false
	}

	// One shared conversation per project, and the FIRST person to open it
	// is its owner.
	//
	// A board ask must not land in the asker's own private terminal, and it
	// must not be owned by a synthetic principal either: something has to
	// pay for the model, and only a real account can. So the first speaker
	// sponsors the conversation, everyone else who may dispatch here can
	// drive it, and the ack says whose budget is funding it — sharing a
	// keyboard and sharing a bill are different decisions.
	if live, err := h.liveBoardSession(ctx, cell); err == nil && live != nil {
		if err := h.queueFollowUp(ctx, live, task); err != nil {
			h.systemPost(ctx, team, "接不上你在 "+cell+" 的会话:"+err.Error(), cell)
			return false
		}
		if !quiet {
			body := "接着这个项目的会话说:" + task
			if !p.Owns(live.Spec.OwnerUserID) {
				// Say who is paying, every time somebody else drives it. A
				// shared session that silently spends one person's quota is a
				// surprise waiting to land on them.
				body += "(这条会话由 " + h.displayOwner(ctx, live.Spec.OwnerUserID) + " 的额度承担)"
			}
			ack := acv1.Post{
				Kind: acv1.PostAgent, Author: cell, Cell: cell, Session: live.Name,
				Body: body,
			}
			_ = h.appendPost(ctx, team, &ack)
		}
		return true
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
			return false
		}
	}
	if model == "" && provider != "" {
		// Defaults may name a pairing without a model; the dispatch form has
		// the same gap and answers it with the provider's first model. A
		// session created without one starts the CLI with no default_model,
		// which fails only after everything else has already worked.
		if pr, ok := h.Registry.Provider(provider); ok && len(pr.Models) > 0 {
			model = pr.Models[0]
		}
	}
	sess.Spec.Runner, sess.Spec.Provider, sess.Spec.Model = runner, provider, model
	cred, err := h.credentialFor(ctx, p, runner)
	if err != nil {
		h.systemPost(ctx, team, err.Error(), cell)
		return false
	}
	sess.Spec.CredentialSecret = cred
	// The real person who asked. They fund it; everyone else who may
	// dispatch here can drive it.
	sess.Spec.OwnerUserID = p.ID()
	sess.Spec.Board = cell
	sess.Spec.Board = team
	if err := h.Client.Create(ctx, sess); err != nil {
		h.systemPost(ctx, team, "派不出去:"+err.Error(), cell)
		return false
	}
	if !quiet {
		ack := acv1.Post{
			Kind: acv1.PostAgent, Author: cell, Cell: cell, Session: sess.Name,
			Body: "接了:" + task,
		}
		_ = h.appendPost(ctx, team, &ack)
	}
	return true
}

// soleCredential picks the caller's model key when there is exactly one.
//
// Guessing between several would spend the wrong budget against the wrong
// vendor, so more than one is a question, not a coin flip — and the answer
// goes in the stream where it was asked.
func (h *Handler) soleCredential(ctx context.Context, p identity.Principal) (string, error) {
	// Owned AND lent: a colleague who has been handed a key must be able to
	// spend it without also owning one, which is the whole point of lending.
	// Model keys only — a connected account is a credential, but it is not a
	// key you can be asked to choose between.
	mine := h.spendableCredentials(ctx, p)
	switch len(mine) {
	case 0:
		// Names where to go, and all three ways out — the third one exists
		// precisely so a new colleague is not stuck on their first afternoon.
		return "", fmt.Errorf("你还没有可用的模型 key。去「我的凭据」自己加一把、" +
			"连一次账号,或者请有 key 的同事在他那页把凭据借给你。")
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

// liveBoardSession finds the project's shared conversation, whoever opened
// it.
//
// Keyed on the PROJECT, not on the asker: the board holds one conversation
// per project, so the second person to speak continues the first person's
// thread instead of opening a rival one beside it.
func (h *Handler) liveBoardSession(ctx context.Context, cell string) (*acv1.Session, error) {
	var list acv1.SessionList
	if err := h.Client.List(ctx, &list, client.InNamespace(h.Namespace)); err != nil {
		return nil, err
	}
	for i := range list.Items {
		s := &list.Items[i]
		if s.Spec.Cell != cell || !s.DeletionTimestamp.IsZero() {
			continue
		}
		// Legacy synthetic owners count as board sessions too, so an
		// upgrade does not strand the conversation that was already there.
		if s.Spec.Board == "" && !strings.HasPrefix(s.Spec.OwnerUserID, LegacyTeamOwnerPrefix) {
			continue
		}
		switch s.Status.Phase {
		case acv1.SessionRunning, acv1.SessionQueued, acv1.SessionDormant, "":
			return s, nil
		}
	}
	return nil, nil
}

// credentialFor picks what will fund a session for THIS person.
//
// A connected account counts. Asking only for a model-key Secret is what
// made the board refuse people who had done exactly what the console told
// them to do — connect their Kimi account — and left them reading "你还没有
// 配模型 key" with an account plainly connected on the credentials page.
func (h *Handler) credentialFor(ctx context.Context, p identity.Principal, runner string) (string, error) {
	if h.runnerUsesAccount(ctx, runner, p.ID()) {
		return "", nil
	}
	return h.soleCredential(ctx, p)
}

// displayOwner names the person funding a session, for a line somebody
// reads. Falls back to the opaque id rather than to silence: "somebody" is
// not an answer to "who is paying".
func (h *Handler) displayOwner(ctx context.Context, id string) string {
	if h.Auth == nil || h.Auth.Accounts == nil || id == "" {
		return id
	}
	users, err := h.Auth.Accounts.DB.Users(ctx)
	if err != nil {
		return id
	}
	for _, u := range users {
		if principalIDFor(ctx, h.accountsDB(), u.Email) == id {
			if u.Name != "" {
				return u.Name
			}
			return u.Email
		}
	}
	return id
}
