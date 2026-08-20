// Package webui serves the control API and the embedded single-page UI
// (web/), plus the reverse proxies for each Cell's preview and production
// zones.

package webui

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/access"
	"github.com/zippo1908/agentcell/internal/forge"
	"github.com/zippo1908/agentcell/internal/identity"
	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/web"
)

// Handler exposes the control API. It talks to the cluster through the
// manager's cached client and holds no state of its own.
type Handler struct {
	Client    client.Client
	Namespace string // control namespace holding Cell/Session CRs
	Registry  *access.Registry
	// Devboxes is the image catalogue the create-a-project form offers,
	// resolved once at startup so an operator's overlay actually reaches it.
	// It used to be re-read from the built-in table on every request, which
	// meant an overlay directory could never change what people saw.
	Devboxes []access.Devbox
	// Forge serves diffs through the broker (ADR-0006); nil/disabled makes
	// the diff endpoint report 501 and review purely informational.
	Forge *forge.Client
	// PreviewOrigin is the absolute origin serving untrusted Cell content
	// (ADR-0007), e.g. https://preview.example.com. Empty derives it from
	// the request host and PreviewPort.
	PreviewOrigin string
	// PreviewPort is used only when PreviewOrigin is empty.
	PreviewPort string
	// PreviewDomain, when set (e.g. "preview.example.com"), gives every
	// Cell its OWN host — <cell>.<domain> — so one Cell's untrusted content
	// is cross-origin to another's. This is the only way to isolate Cells
	// from each other in the browser; see ADR-0007.
	PreviewDomain string
	// Auth mints the short-lived per-Cell tickets that authorize the
	// preview origin (the console credential is never accepted there).
	Auth *Authenticator
	// DefaultRunner and DefaultProvider are the pairing a new project starts
	// with, set by the operator. Empty means the console asks with nothing
	// preselected, which is right for a deployment that has not decided.
	DefaultRunner   string
	DefaultProvider string
	// RESTConfig and Kube back exec into a resident session's pod. They are
	// how the console reaches a live tmux without the session pod holding an
	// API token of its own (ADR-0005).
	RESTConfig *rest.Config
	Kube       kubernetes.Interface
	// terminals bounds how many exec streams one person can hold open; see
	// maxTerminalsPerUser.
	terminals *terminalCounter
	// asks holds the @机器人 questions awaiting their streamed answer; see
	// board_ask.go. Zero value is ready to use.
	asks   askRegistry
	probes probeCache
}

// previewBaseFor returns the origin serving a specific Cell's untrusted
// content. With PreviewDomain each Cell gets its own host; without it they
// share one origin, which is weaker — documented in ADR-0007.
// previewHostFor returns the host serving one Cell ZONE. Dev and prod get
// distinct hosts so the agent's unreviewed work cannot read, share storage
// with, or install a service worker over the released build (ADR-0007).
func (h *Handler) previewHostFor(r *http.Request, cell string, zone Zone) string {
	if h.PreviewDomain != "" {
		return cell + "-" + string(zone) + "." + strings.TrimPrefix(h.PreviewDomain, ".")
	}
	// Without a preview domain every zone shares one host; the ticket and
	// path-scoped cookie are then the only separation. Documented as a
	// non-production configuration in ADR-0007.
	return h.Auth.hostOnly(r) + portSuffix(h.PreviewPort)
}

func portSuffix(port string) string {
	if port == "" || port == "80" || port == "443" {
		return ""
	}
	return ":" + port
}

func (h *Handler) previewBaseFor(r *http.Request, cell string, zone Zone) string {
	if h.PreviewOrigin != "" && h.PreviewDomain == "" {
		return strings.TrimRight(h.PreviewOrigin, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := h.Auth.forwarded(r, "X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	return scheme + "://" + h.previewHostFor(r, cell, zone)
}

// previewURL builds a ready-to-open absolute URL carrying a fresh
// single-use ticket bound to this Cell, zone and host.
func (h *Handler) previewURL(r *http.Request, cell string, zone Zone, path string) string {
	if path == "" || h.Auth == nil {
		return ""
	}
	base := h.previewBaseFor(r, cell, zone)
	host := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return base + path + "?" + previewTicketQS + "=" + h.Auth.MintPreviewTicket(cell, zone, host)
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/meta", h.meta)
	mux.HandleFunc("GET /api/me", h.me)
	mux.HandleFunc("POST /api/kimi/login", h.startKimiLogin)
	mux.HandleFunc("DELETE /api/kimi/login", h.disconnectKimi)
	mux.HandleFunc("GET /api/kimi/login", h.pollKimiLogin)
	mux.HandleFunc("GET /api/credentials", h.listCredentials)
	mux.HandleFunc("PUT /api/credentials/{name}", h.putCredential)
	mux.HandleFunc("DELETE /api/credentials/{name}", h.deleteCredential)
	mux.HandleFunc("GET /api/cells", h.listCells)
	mux.HandleFunc("POST /api/cells", h.createCell)
	mux.HandleFunc("GET /api/cells/{cell}", h.getCell)
	mux.HandleFunc("PUT /api/cells/{cell}/description", h.putDescription)
	mux.HandleFunc("PUT /api/cells/{cell}/repo", h.putRepo)
	mux.HandleFunc("PUT /api/cells/{cell}/repo-credential", h.putRepoCredential)
	mux.HandleFunc("GET /api/me/git-identities", h.listGitIdentities)
	mux.HandleFunc("PUT /api/me/git-identities", h.putGitIdentity)
	mux.HandleFunc("DELETE /api/me/git-identities/{provider}", h.deleteGitIdentity)
	mux.HandleFunc("GET /api/me/grants", h.listGrants)
	mux.HandleFunc("POST /api/me/grants", h.createGrant)
	mux.HandleFunc("DELETE /api/me/grants/{credential}/{who}", h.deleteGrant)
	mux.HandleFunc("GET /api/placementclasses", h.listPlacementClasses)
	mux.HandleFunc("GET /api/new-project-options", h.newProjectOptions)
	mux.HandleFunc("GET /api/cells/{cell}/branches", h.listBranches)
	mux.HandleFunc("GET /api/cells/{cell}/board", h.listBoard)
	mux.HandleFunc("POST /api/cells/{cell}/board", h.postToBoard)
	mux.HandleFunc("GET /api/cells/{cell}/board/ask/{ask}", h.boardAsk)
	mux.HandleFunc("GET /api/cells/{cell}/board/prewarm", h.boardPrewarm)
	mux.HandleFunc("GET /api/sessions/{session}/terminal", h.sessionTerminal)
	mux.HandleFunc("PUT /api/cells/{cell}/placement", h.putPlacement)
	mux.HandleFunc("POST /api/cells/{cell}/dispatch", h.dispatch)
	mux.HandleFunc("POST /api/cells/{cell}/open", h.openCell)
	mux.HandleFunc("GET /api/whoami", h.whoami)
	mux.HandleFunc("GET /api/people", h.listPeople)
	mux.HandleFunc("POST /api/invites", h.createInvite)
	mux.HandleFunc("GET /api/cells/{cell}/members", h.listMembers)
	mux.HandleFunc("GET /api/cells/{cell}/files", h.listFiles)
	mux.HandleFunc("POST /api/cells/{cell}/files", h.uploadFile)
	mux.HandleFunc("GET /api/cells/{cell}/files/{path...}", h.getFile)
	mux.HandleFunc("DELETE /api/cells/{cell}/files/{path...}", h.deleteFile)
	mux.HandleFunc("POST /api/password", h.changePassword)
	mux.HandleFunc("POST /api/cells/{cell}/release", h.release)
	mux.HandleFunc("PUT /api/cells/{cell}/members", h.putMember)
	mux.HandleFunc("DELETE /api/cells/{cell}/members/{user}", h.deleteMember)
	mux.HandleFunc("GET /api/reviews", h.listReviews)
	mux.HandleFunc("GET /api/sessions/{session}/diff", h.sessionDiff)
	mux.HandleFunc("POST /api/sessions/{session}/review", h.reviewSession)
	mux.HandleFunc("DELETE /api/sessions/{session}", h.settleSession)
	mux.HandleFunc("GET /api/sessions/{session}/state", h.sessionState)
	mux.HandleFunc("POST /api/sessions/{session}/continue", h.continueSession)
	mux.HandleFunc("POST /api/sessions/{session}/sleep", h.sleepSession)
	mux.HandleFunc("POST /api/sessions/{session}/restart", h.restartRuntime)
	// The SPA is last: it serves built assets and falls back to index.html
	// so client-side routes (/cells/x, /reviews) survive a hard reload.
	mux.Handle("/", spaHandler())
	return mux
}

// PreviewRoutes serves untrusted Cell content — and nothing else — on a
// SEPARATE ORIGIN from the console (ADR-0007). Origin separation, not
// sandboxing, is what confines this content, so a previewed app keeps full
// same-origin powers over itself (cookies, localStorage, service workers)
// while being unable to touch the console: cross-origin scripts cannot read
// our DOM, cookie-authenticated writes fail the Origin check, and responses
// are not exposed to it by CORS.
//
// The console API and SPA are deliberately absent from this mux: nothing
// here should be reachable from the origin that runs untrusted code.
func (h *Handler) PreviewRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/preview/{cell}/", h.preview)
	mux.HandleFunc("/app/{cell}/", h.productionApp)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// CellFromPreviewRequest extracts the Cell a preview request targets, from
// the path (/preview/<cell>/…, /app/<cell>/…). Used to scope authorization
// to exactly one Cell.
func CellFromPreviewRequest(r *http.Request) string {
	p := strings.TrimPrefix(r.URL.Path, "/")
	for _, prefix := range []string{"preview/", "app/"} {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			if i := strings.IndexByte(rest, '/'); i > 0 {
				return rest[:i]
			}
			return rest
		}
	}
	return ""
}

// isClientRoute reports whether a path is one the SPA router owns, so a
// hard reload of it should be answered with index.html. Anything else —
// unknown API paths, missing assets, other prefixes — must NOT be masked
// as a 200 HTML page.
// isClientRoute lists the paths the SPA router owns, so a hard reload of one
// is answered with index.html.
//
// It is an allow-list rather than a catch-all because answering 200 HTML for
// an unknown /api path or a mistyped asset turns a programming error into a
// silently "successful" response. The cost is that a new page must be added
// here — a real trap, and the reason the test below enumerates them.
func isClientRoute(p string) bool {
	switch p {
	case "/", "/dashboard", "/cells", "/cells/new", "/reviews", "/capabilities", "/credentials", "/people", "/board", "/workspace":
		return true
	}
	if rest, ok := strings.CutPrefix(p, "/cells/"); ok {
		// /cells/<name> only; no deeper synthetic paths.
		return !strings.Contains(rest, "/")
	}
	// /workspace/<project>. The project moved into the URL so a link to what
	// somebody is looking at can be pasted to a colleague — which only works
	// if the server serves that link on a cold load.
	if rest, ok := strings.CutPrefix(p, "/workspace/"); ok {
		return rest != "" && !strings.Contains(rest, "/")
	}
	return false
}

// spaHandler serves the embedded build. It serves real assets as-is and
// falls back to index.html only for GET/HEAD of a known client route:
// returning HTML 200 for an unknown /api/* path or for a POST would turn
// programming errors into silently "successful" responses.
func spaHandler() http.Handler {
	assets := web.Dist()
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if _, err := fs.Stat(assets, p); err == nil {
				files.ServeHTTP(w, r) // a real built asset
				return
			}
		}
		// Unmatched API paths answer in the caller's language.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeErr(w, http.StatusNotFound, fmt.Errorf("no such endpoint"))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !isClientRoute(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		index, err := fs.ReadFile(assets, "index.html")
		if err != nil {
			http.Error(w, "UI not built", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(index)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (h *Handler) meta(w http.ResponseWriter, r *http.Request) {
	// The catalogue, not two flat name lists: a runner can only drive
	// providers that speak one of its protocols, and the UI should not be
	// able to offer a combination the API will refuse.
	runners, providers := h.Registry.Catalogue()
	writeJSON(w, 200, map[string]any{
		"runners":   runners,
		"providers": providers,
		// Absolute base for preview/app URLs. It is a different origin from
		// the console on purpose; the UI must not build these as relative
		// paths or the isolation collapses.
		"previewOrigin": h.previewOriginFor(r),
	})
}

// me tells the UI who it is acting as.
//
// Ownership is invisible without it: a user needs to know whether they are
// themselves or the shared static-token principal, because that decides
// which sessions they can see and whether "private" means anything here.
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	p := identity.FromContext(r.Context())
	writeJSON(w, 200, map[string]any{
		"subject": p.ID(),
		"name":    p.Display(),
		"email":   p.Email,
		"kind":    string(p.Kind),
		// Shared means every caller is the same principal, so nothing is
		// private from anyone else holding the token. Saying so plainly beats
		// a UI that implies isolation it does not have.
		"shared": p.Kind == identity.KindToken,
		// The console hides what you cannot do rather than offering it and
		// refusing later.
		"admin": p.Admin,
	})
}

// previewOriginFor resolves the origin browsers should use for untrusted
// content: the configured value, or the console's host with the preview
// port substituted (which is what a port-forward setup gives you).
func (h *Handler) previewOriginFor(r *http.Request) string {
	if h.PreviewOrigin != "" {
		return strings.TrimRight(h.PreviewOrigin, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := h.Auth.forwarded(r, "X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	host := r.Host
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	port := h.PreviewPort
	if port == "" {
		port = "8081"
	}
	return scheme + "://" + host + ":" + port
}

type cellView struct {
	Name string `json:"name"`
	// DisplayName is what people called it. Empty on projects created
	// before names were free text, where the address IS the name.
	DisplayName    string `json:"displayName,omitempty"`
	Phase          string `json:"phase"`
	Description    string `json:"description"`
	ActiveSessions int32  `json:"activeSessions"`
	MaxSessions    int32  `json:"maxSessions"`
	PreviewPath    string `json:"previewPath"`
	ProductionPath string `json:"productionPath"`
	// Absolute, ticketed URLs on the untrusted-content origin. The UI must
	// use these rather than composing paths against its own origin.
	PreviewURL    string `json:"previewURL"`
	ProductionURL string `json:"productionURL"`
	// ProductionExternal means ProductionURL points at a system we do not
	// run: open it, do not embed it.
	ProductionExternal bool   `json:"productionExternal,omitempty"`
	HandoffMessage     string `json:"handoffMessage,omitempty"`
	ReleaseRef         string `json:"releaseRef"`
	FollowSession      string `json:"followSession"`
	Message            string `json:"message"`
	// Access and Members let the console show who can touch this project —
	// and say so when the answer is "anyone who can log in".
	Access  string        `json:"access"`
	Members []acv1.Member `json:"members,omitempty"`
	// Where this project runs. A Cell is one machine's worth of project, so
	// this is not trivia: it is the whole of its capacity, and on a mixed
	// fleet it is a decision somebody made or declined to make.
	Node string `json:"node,omitempty"`
	// Pool is the placement in force, "" when the scheduler chooses freely.
	Pool string `json:"pool,omitempty"`
	// SchedulingMessage is the scheduler's own explanation for a Cell that
	// has landed nowhere — otherwise the most opaque state this system has.
	SchedulingMessage string `json:"schedulingMessage,omitempty"`
	// RepoURL is empty for a project created before its repository existed.
	// The console needs to know, because that project can be looked at but
	// not worked in, and the reason has to be on the page rather than
	// discovered as an agent with nothing to check out.
	RepoURL    string `json:"repoURL,omitempty"`
	RepoBranch string `json:"repoBranch,omitempty"`
	// RepoSecretName is which credential this project clones and pushes
	// with. A name only — never anything from inside it.
	RepoSecretName string `json:"repoSecretName,omitempty"`
}

func (h *Handler) toCellView(r *http.Request, c *acv1.Cell) cellView {
	v := cellView{
		Name: c.Name, DisplayName: c.Spec.DisplayName,
		Phase: string(c.Status.Phase), Description: c.Spec.Description,
		ActiveSessions: c.Status.ActiveSessions, MaxSessions: c.Spec.MaxSessions,
		PreviewPath: c.Status.PreviewPath, ProductionPath: c.Status.ProductionPath,
		ReleaseRef: c.Spec.Production.Ref, FollowSession: c.Spec.Preview.FollowSession,
		Message: c.Status.Message, HandoffMessage: c.Status.HandoffMessage,
		Access: string(effectiveAccess(c)), Members: c.Spec.Members,
		Node: c.Status.Node, SchedulingMessage: c.Status.SchedulingMessage,
		RepoURL: c.Spec.Repo.URL, RepoBranch: c.Spec.Repo.Branch,
		RepoSecretName: c.Spec.Repo.SecretName,
	}
	for k, val := range c.Spec.Placement.NodeSelector {
		v.Pool = k + "=" + val
	}
	v.PreviewURL = h.previewURL(r, c.Name, ZoneDev, c.Status.PreviewPath)
	v.ProductionURL = h.previewURL(r, c.Name, ZoneProd, c.Status.ProductionPath)
	if c.Spec.Production.Target == acv1.ProductionExternal {
		// Someone else's production is linked to, never proxied: it is not
		// our origin, it has its own auth, and routing it through the
		// untrusted-content proxy would break both — and would put a
		// production system behind a ticket we mint.
		v.ProductionURL = c.Spec.Production.ExternalURL
		v.ProductionExternal = true
	}
	return v
}

func (h *Handler) listCells(w http.ResponseWriter, r *http.Request) {
	var list acv1.CellList
	if err := h.Client.List(r.Context(), &list, client.InNamespace(h.Namespace)); err != nil {
		writeErr(w, 500, err)
		return
	}
	// Filter BEFORE building the view. toCellView mints preview and
	// production tickets, and a ticket is a capability, not a label — so
	// building one for a Cell the caller cannot see would hand out access
	// while "only" leaking a name.
	p := identity.FromContext(r.Context())
	views := make([]cellView, 0, len(list.Items))
	for i := range list.Items {
		if !can(p, &list.Items[i], ActionView) {
			continue
		}
		views = append(views, h.toCellView(r, &list.Items[i]))
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	writeJSON(w, 200, views)
}

type sessionView struct {
	Name     string `json:"name"`
	Task     string `json:"task"`
	Runner   string `json:"runner"`
	Provider string `json:"provider"`
	Phase    string `json:"phase"`
	Branch   string `json:"branch"`
	Produced bool   `json:"produced"`
	Message  string `json:"message"`
	Started  string `json:"started"`
	// FundedBy names the person whose credential pays for this session, and
	// Shared says whether anybody else may drive it. Both are shown rather
	// than inferred: a shared session that silently spends one person's
	// quota is a surprise waiting to land on them.
	FundedBy string `json:"fundedBy,omitempty"`
	Mine     bool   `json:"mine,omitempty"`
	Shared   bool   `json:"shared,omitempty"`
}

func (h *Handler) getCell(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("cell")
	var cell acv1.Cell
	if err := h.Client.Get(r.Context(), types.NamespacedName{Namespace: h.Namespace, Name: name}, &cell); err != nil {
		writeErr(w, 404, err)
		return
	}
	if !h.authorize(w, r, &cell, ActionView) {
		return
	}
	var list acv1.SessionList
	if err := h.Client.List(r.Context(), &list, client.InNamespace(h.Namespace)); err != nil {
		writeErr(w, 500, err)
		return
	}
	sessions := []sessionView{}
	for i := range list.Items {
		s := &list.Items[i]
		if s.Spec.Cell != name {
			continue
		}
		// Another user's session is invisible until it settles.
		if !visible(r.Context(), s) {
			continue
		}
		sv := sessionView{
			Name: s.Name, Task: s.Spec.Task, Runner: s.Spec.Runner, Provider: s.Spec.Provider,
			Phase: string(s.Status.Phase), Branch: s.Status.Branch, Produced: s.Status.Produced,
			Message:  s.Status.Message,
			FundedBy: h.displayOwner(r.Context(), s.Spec.OwnerUserID),
			Mine:     identity.FromContext(r.Context()).Owns(s.Spec.OwnerUserID),
			Shared:   s.Spec.Board != "" || strings.HasPrefix(s.Spec.OwnerUserID, LegacyTeamOwnerPrefix),
		}
		if s.Status.StartTime != nil {
			sv.Started = s.Status.StartTime.UTC().Format("2006-01-02 15:04:05Z")
		}
		sessions = append(sessions, sv)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Name > sessions[j].Name })
	writeJSON(w, 200, map[string]any{"cell": h.toCellView(r, &cell), "sessions": sessions})
}

func (h *Handler) putDescription(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("cell")
	var body struct {
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	var cell acv1.Cell
	if err := h.Client.Get(r.Context(), types.NamespacedName{Namespace: h.Namespace, Name: name}, &cell); err != nil {
		writeErr(w, 404, err)
		return
	}
	if !h.authorize(w, r, &cell, ActionSettings) {
		return
	}
	cell.Spec.Description = body.Description
	if err := h.Client.Update(r.Context(), &cell); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "saved"})
}

type dispatchRequest struct {
	Task             string `json:"task"`
	Runner           string `json:"runner"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	CredentialSecret string `json:"credentialSecret"`
	FollowPreview    bool   `json:"followPreview"`
	// Resident keeps the slot alive in tmux after the agent finishes, so the
	// owner can look at the result and keep going in the same context.
	// A pointer so an omitted field means the platform default (resident),
	// not false. A caller that genuinely wants a headless one-shot has to
	// say so.
	Resident *bool `json:"resident"`
	// TTLSeconds overrides the default. For a resident session this is IDLE
	// time, not total age.
	TTLSeconds int64 `json:"ttlSeconds"`
	// IdleSeconds is the OTHER clock: how long a resident session sits
	// unused before it sleeps. Separate field because the two mean different
	// things — one gives back compute, the other publishes — and a single
	// "TTL" is how somebody asks for one and gets the other.
	IdleSeconds int64 `json:"idleSeconds"`
}

// openCell gives the caller their terminal in this project, task or no task.
//
// A project IS a runtime somebody works in — not a queue you have to put a
// task into before anything exists. Requiring a first instruction meant the
// workspace opened onto nothing at all, and "打开项目" had no meaning until
// you had already decided what to ask for. Opening is now its own verb, and
// the session it lands you in is the same one every later instruction goes
// to.
func (h *Handler) openCell(w http.ResponseWriter, r *http.Request) {
	h.dispatchInto(w, r, true)
}

func (h *Handler) dispatch(w http.ResponseWriter, r *http.Request) {
	h.dispatchInto(w, r, false)
}

func (h *Handler) dispatchInto(w http.ResponseWriter, r *http.Request, taskOptional bool) {
	cellName := r.PathValue("cell")
	var req dispatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !taskOptional {
		writeErr(w, 400, err)
		return
	}
	if strings.TrimSpace(req.Task) == "" && !taskOptional {
		writeErr(w, 400, fmt.Errorf("task is empty"))
		return
	}
	// Fill in what the project already decided.
	//
	// A dispatch needs a runner, a provider and a key; none of those are new
	// information at the moment somebody says what they want. The pairing was
	// chosen when the project was created, and the key is the caller's own.
	// Asking again on every task is how a "say what you want" box turns back
	// into a form.
	var cellForDefaults acv1.Cell
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: cellName}, &cellForDefaults); err == nil {
		if req.Runner == "" {
			req.Runner = cellForDefaults.Spec.Defaults.Runner
		}
		if req.Provider == "" {
			req.Provider = cellForDefaults.Spec.Defaults.Provider
		}
		if req.Model == "" {
			req.Model = cellForDefaults.Spec.Defaults.Model
		}
	}
	// Then the platform's own defaults. A project created before anybody
	// chose a pairing — or created by a script — has none, and "打开项目"
	// then failed with `unknown runner ""`, which reads like a bug in the
	// request rather than a setting nobody ever made.
	if req.Runner == "" {
		req.Runner = h.DefaultRunner
	}
	if req.Provider == "" {
		req.Provider = h.DefaultProvider
	}
	if req.Model == "" && req.Provider != "" {
		if pr, ok := h.Registry.Provider(req.Provider); ok && len(pr.Models) > 0 {
			// The provider's own first model. Naming one here beats making
			// somebody pick from a list before they have said anything.
			req.Model = pr.Models[0]
		}
	}
	if req.Runner == "" || req.Provider == "" {
		writeErr(w, 400, fmt.Errorf("这个部署还没设默认的 runner/供应商,先在项目里选一个"))
		return
	}
	if req.CredentialSecret == "" {
		// A connected account IS the credential. Demanding a model key on
		// top of it was the old shape showing through: "连一次账号,之后所有
		// 会话都用它" cannot be true if you still cannot start without
		// pasting a key.
		if h.runnerUsesAccount(r.Context(), req.Runner, identity.FromContext(r.Context()).ID()) {
			req.CredentialSecret = ""
		} else {
			cred, err := h.soleCredential(r.Context(), identity.FromContext(r.Context()))
			if err != nil {
				writeErr(w, 400, err)
				return
			}
			req.CredentialSecret = cred
		}
	}
	// Fail fast on an invalid binding before creating anything.
	binding, err := h.Registry.Resolve(req.Runner, req.Provider, req.Model)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	var cell acv1.Cell
	if err := h.Client.Get(r.Context(), types.NamespacedName{Namespace: h.Namespace, Name: cellName}, &cell); err != nil {
		writeErr(w, 404, err)
		return
	}
	if !h.authorize(w, r, &cell, ActionDispatch) {
		return
	}
	// A caller may spend a model credential it owns — or one somebody lent
	// them, which is how a new colleague gets to do anything at all.
	if err := h.mayUseCredential(r, req.CredentialSecret); err != nil {
		writeErr(w, 404, err)
		return
	}
	// One live session per person per project. A second ask continues the
	// conversation instead of opening a rival one — the CLI is already good
	// at holding several conversations, and a platform-level copy of that
	// only bought a way to lock somebody out of their own work.
	p := identity.FromContext(r.Context())
	if live, err := liveSessionFor(r.Context(), h.Client, h.Namespace, cellName, p.ID()); err == nil && live != nil {
		if strings.TrimSpace(req.Task) == "" {
			// Opening something already open is just going there. Waking it
			// if it was parked is the whole point of asking.
			if _, err := h.wakeIfDormant(r, live); err != nil {
				writeErr(w, 500, err)
				return
			}
			writeJSON(w, 200, map[string]any{"session": live.Name, "continued": true})
			return
		}
		if err := h.queueFollowUp(r.Context(), live, req.Task); err != nil {
			writeErr(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{
			"session":   live.Name,
			"continued": true,
			"message":   "接着你在这个工作区的会话说的——不是新开一条。",
		})
		return
	}
	// Claim before creating. Looking for a live session and then creating
	// one is a race that two clicks can win at once; the claim is a Create,
	// which only one caller can complete.
	won, existing, cerr := h.claimLiveSession(r.Context(), cellName, p.ID())
	if cerr != nil {
		writeErr(w, 409, cerr)
		return
	}
	if !won {
		var live acv1.Session
		if err := h.Client.Get(r.Context(), types.NamespacedName{
			Namespace: h.Namespace, Name: existing}, &live); err == nil {
			if strings.TrimSpace(req.Task) != "" {
				if err := h.queueFollowUp(r.Context(), &live, req.Task); err != nil {
					writeErr(w, 500, err)
					return
				}
			}
			writeJSON(w, 200, map[string]any{
				"session": live.Name, "continued": true,
				"message": "接着你在这个工作区的会话说的——不是新开一条。",
			})
			return
		}
	}

	id := ids.NewSessionID()
	sess := &acv1.Session{}
	sess.Namespace = h.Namespace
	sess.Name = ids.SessionName(id)
	sess.Spec = acv1.SessionSpec{
		Cell: cellName, Task: req.Task, Runner: req.Runner, Provider: req.Provider,
		Model: req.Model, CredentialSecret: req.CredentialSecret, FollowPreview: req.FollowPreview,
		Resident:   req.Resident,
		TTLSeconds: req.TTLSeconds,
		// Persisted, not merely accepted. The API took this field and threw
		// it away: somebody setting a longer idle window watched their
		// session sleep on the default anyway, with nothing anywhere saying
		// their setting had been ignored.
		IdleSeconds: req.IdleSeconds,
		OwnerUserID: identity.FromContext(r.Context()).ID(),
	}
	if err := h.Client.Create(r.Context(), sess); err != nil {
		// Nothing was created, so the claim must not be left standing: the
		// next caller would wait for a session that is never coming.
		h.releaseClaim(r.Context(), cellName, p.ID())
		writeErr(w, 500, err)
		return
	}
	if err := h.nameTheClaim(r.Context(), cellName, p.ID(), sess); err != nil {
		// The session exists and is the answer; a claim that failed to
		// record it only costs the next caller a short wait.
		writeErr(w, 200, err)
	}
	out := map[string]string{"session": sess.Name}
	if binding.CrossVendor {
		// Returned, not blocked: the operator chose this pairing and it
		// works. Saying so once, where they can see it, beats a footnote.
		out["advisory"] = binding.Advisory
	}
	writeJSON(w, 201, out)
}

func (h *Handler) settleSession(w http.ResponseWriter, r *http.Request) {
	sess, err := h.ownedSession(r, r.PathValue("session"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if err := h.Client.Delete(r.Context(), sess); err != nil {
		writeErr(w, 404, err)
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "settling"})
}

// release is the only door into the 正式区: it stamps a new ReleaseID
// (rolling the isolated prod pod, which re-clones the ref). Dev-zone
// debugging has no other path into production.
func (h *Handler) release(w http.ResponseWriter, r *http.Request) {
	cellName := r.PathValue("cell")
	var body struct {
		Ref string `json:"ref"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var cell acv1.Cell
	if err := h.Client.Get(r.Context(), types.NamespacedName{Namespace: h.Namespace, Name: cellName}, &cell); err != nil {
		writeErr(w, 404, err)
		return
	}
	if !h.authorize(w, r, &cell, ActionRelease) {
		return
	}
	if body.Ref != "" {
		cell.Spec.Production.Ref = body.Ref
	}
	cell.Spec.Production.ReleaseID = ids.NewSessionID()
	if err := h.Client.Update(r.Context(), &cell); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{
		"ok": "release rolling", "releaseID": cell.Spec.Production.ReleaseID,
	})
}

// preview reverse-proxies the dev zone (/preview/<cell>/), productionApp
// the 正式区 (/app/<cell>/); the browser only ever talks to celld.
func (h *Handler) preview(w http.ResponseWriter, r *http.Request) {
	cellName := r.PathValue("cell")
	var cell acv1.Cell
	if err := h.Client.Get(r.Context(), types.NamespacedName{Namespace: h.Namespace, Name: cellName}, &cell); err != nil {
		writeErr(w, 404, err)
		return
	}
	port := cell.Spec.Preview.Port
	if port == 0 {
		port = 3000
	}
	h.proxyTo(w, r, cellName, ids.PreviewService, port, "/preview/"+cellName)
}

func (h *Handler) productionApp(w http.ResponseWriter, r *http.Request) {
	cellName := r.PathValue("cell")
	var cell acv1.Cell
	if err := h.Client.Get(r.Context(), types.NamespacedName{Namespace: h.Namespace, Name: cellName}, &cell); err != nil {
		writeErr(w, 404, err)
		return
	}
	if cell.Status.ProductionPath == "" {
		writeErr(w, 404, fmt.Errorf("cell %q has no production zone yet — release first", cellName))
		return
	}
	port := cell.Spec.Production.Port
	if port == 0 {
		port = cell.Spec.Preview.Port
	}
	if port == 0 {
		port = 3000
	}
	if cell.Spec.Production.Target == acv1.ProductionExternal {
		// There is nothing here to proxy, and pretending otherwise would
		// answer a production URL with a platform error page.
		writeErr(w, http.StatusNotFound,
			fmt.Errorf("this Cell hands production off; it lives at %s", cell.Spec.Production.ExternalURL))
		return
	}
	h.proxyTo(w, r, cellName, ids.ProdService, port, "/app/"+cellName)
}

// untrustedContentCSP confines preview/production content. Since this
// content is served from its own origin (ADR-0007), the isolation that
// matters — no access to the console's DOM, cookie or API — comes from
// origin separation, so allow-same-origin is GRANTED here: a previewed app
// keeps its own cookies, localStorage and service workers and behaves
// exactly as it would standalone.
//
// What remains forbidden is what a framed page should never do regardless
// of origin: navigate or replace the top-level console page. Applied as a
// response header (not only the iframe attribute) so it also holds when an
// operator opens the preview URL directly.
const untrustedContentCSP = "sandbox allow-same-origin allow-scripts allow-forms " +
	"allow-modals allow-popups allow-downloads"

func (h *Handler) proxyTo(w http.ResponseWriter, r *http.Request, cellName, svc string, port int32, prefix string) {
	h.proxyToURL(w, r, fmt.Sprintf("http://%s.%s.svc:%d", svc, ids.WorkloadNamespace(cellName), port), prefix)
}

// proxyToURL forwards to an already-resolved upstream. Split out from
// proxyTo so the untrusted-content headers can be tested without cluster
// DNS.
func (h *Handler) proxyToURL(w http.ResponseWriter, r *http.Request, upstream, prefix string) {
	target, err := url.Parse(upstream)
	if err != nil {
		http.Error(w, "bad upstream", http.StatusInternalServerError)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		// The upstream is repo- and agent-authored code. Refusing to ACCEPT
		// platform credentials here is not enough — we must not HAND them
		// over either, or the proxy itself becomes the leak. The previewed
		// app keeps its own cookies; only platform-reserved names go.
		stripPlatformCredentials(req)
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		// Append rather than replace: multiple CSP headers are enforced as
		// the intersection, so an upstream policy still applies and ours
		// cannot be relaxed by the upstream.
		resp.Header.Add("Content-Security-Policy", untrustedContentCSP)
		resp.Header.Set("Referrer-Policy", "no-referrer")
		resp.Header.Set("X-Content-Type-Options", "nosniff")
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		w.Header().Set("Content-Security-Policy", untrustedContentCSP)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprintf(w, "upstream not ready: %v\n", err)
	}
	http.StripPrefix(prefix, proxy).ServeHTTP(w, r)
}
