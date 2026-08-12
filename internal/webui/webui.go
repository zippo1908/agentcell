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
	"sigs.k8s.io/controller-runtime/pkg/client"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/access"
	"github.com/zippo1908/agentcell/internal/forge"
	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/web"
)

// Handler exposes the control API. It talks to the cluster through the
// manager's cached client and holds no state of its own.
type Handler struct {
	Client    client.Client
	Namespace string // control namespace holding Cell/Session CRs
	Registry  *access.Registry
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
	return hostOnly(r) + portSuffix(h.PreviewPort)
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
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
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
	mux.HandleFunc("GET /api/cells", h.listCells)
	mux.HandleFunc("GET /api/cells/{cell}", h.getCell)
	mux.HandleFunc("PUT /api/cells/{cell}/description", h.putDescription)
	mux.HandleFunc("POST /api/cells/{cell}/dispatch", h.dispatch)
	mux.HandleFunc("POST /api/cells/{cell}/release", h.release)
	mux.HandleFunc("GET /api/reviews", h.listReviews)
	mux.HandleFunc("GET /api/sessions/{session}/diff", h.sessionDiff)
	mux.HandleFunc("POST /api/sessions/{session}/review", h.reviewSession)
	mux.HandleFunc("DELETE /api/sessions/{session}", h.settleSession)
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
func isClientRoute(p string) bool {
	switch {
	case p == "/" || p == "/cells" || p == "/reviews":
		return true
	case strings.HasPrefix(p, "/cells/"):
		// /cells/<name> only; no deeper synthetic paths.
		return !strings.Contains(strings.TrimPrefix(p, "/cells/"), "/")
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
	writeJSON(w, 200, map[string]any{
		"runners":   access.Runners(),
		"providers": h.Registry.Providers(),
		// Absolute base for preview/app URLs. It is a different origin from
		// the console on purpose; the UI must not build these as relative
		// paths or the isolation collapses.
		"previewOrigin": h.previewOriginFor(r),
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
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
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
	Name           string `json:"name"`
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
	ReleaseRef    string `json:"releaseRef"`
	FollowSession string `json:"followSession"`
	Message       string `json:"message"`
}

func (h *Handler) toCellView(r *http.Request, c *acv1.Cell) cellView {
	v := cellView{
		Name: c.Name, Phase: string(c.Status.Phase), Description: c.Spec.Description,
		ActiveSessions: c.Status.ActiveSessions, MaxSessions: c.Spec.MaxSessions,
		PreviewPath: c.Status.PreviewPath, ProductionPath: c.Status.ProductionPath,
		ReleaseRef: c.Spec.Production.Ref, FollowSession: c.Spec.Preview.FollowSession,
		Message: c.Status.Message,
	}
	v.PreviewURL = h.previewURL(r, c.Name, ZoneDev, c.Status.PreviewPath)
	v.ProductionURL = h.previewURL(r, c.Name, ZoneProd, c.Status.ProductionPath)
	return v
}

func (h *Handler) listCells(w http.ResponseWriter, r *http.Request) {
	var list acv1.CellList
	if err := h.Client.List(r.Context(), &list, client.InNamespace(h.Namespace)); err != nil {
		writeErr(w, 500, err)
		return
	}
	views := make([]cellView, 0, len(list.Items))
	for i := range list.Items {
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
}

func (h *Handler) getCell(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("cell")
	var cell acv1.Cell
	if err := h.Client.Get(r.Context(), types.NamespacedName{Namespace: h.Namespace, Name: name}, &cell); err != nil {
		writeErr(w, 404, err)
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
		sv := sessionView{
			Name: s.Name, Task: s.Spec.Task, Runner: s.Spec.Runner, Provider: s.Spec.Provider,
			Phase: string(s.Status.Phase), Branch: s.Status.Branch, Produced: s.Status.Produced,
			Message: s.Status.Message,
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
}

func (h *Handler) dispatch(w http.ResponseWriter, r *http.Request) {
	cellName := r.PathValue("cell")
	var req dispatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if strings.TrimSpace(req.Task) == "" {
		writeErr(w, 400, fmt.Errorf("task is empty"))
		return
	}
	// Fail fast on an invalid binding before creating anything.
	if _, err := h.Registry.Resolve(req.Runner, req.Provider, req.Model); err != nil {
		writeErr(w, 400, err)
		return
	}
	var cell acv1.Cell
	if err := h.Client.Get(r.Context(), types.NamespacedName{Namespace: h.Namespace, Name: cellName}, &cell); err != nil {
		writeErr(w, 404, err)
		return
	}
	id := ids.NewSessionID()
	sess := &acv1.Session{}
	sess.Namespace = h.Namespace
	sess.Name = ids.SessionName(id)
	sess.Spec = acv1.SessionSpec{
		Cell: cellName, Task: req.Task, Runner: req.Runner, Provider: req.Provider,
		Model: req.Model, CredentialSecret: req.CredentialSecret, FollowPreview: req.FollowPreview,
	}
	if err := h.Client.Create(r.Context(), sess); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 201, map[string]string{"session": sess.Name})
}

func (h *Handler) settleSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("session")
	sess := &acv1.Session{}
	sess.Namespace = h.Namespace
	sess.Name = name
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
