// Package webui serves the calibration loop: the embedded single-page UI
// (product description editor + dispatch + session list) side by side with
// the resident product preview, which celld reverse-proxies from each
// Cell's in-cluster preview Service.
package webui

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/access"
	"github.com/zippo1908/agentcell/pkg/ids"
)

//go:embed index.html
var indexHTML []byte

// Handler exposes the control API. It talks to the cluster through the
// manager's cached client and holds no state of its own.
type Handler struct {
	Client    client.Client
	Namespace string // control namespace holding Cell/Session CRs
	Registry  *access.Registry
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("GET /api/meta", h.meta)
	mux.HandleFunc("GET /api/cells", h.listCells)
	mux.HandleFunc("GET /api/cells/{cell}", h.getCell)
	mux.HandleFunc("PUT /api/cells/{cell}/description", h.putDescription)
	mux.HandleFunc("POST /api/cells/{cell}/dispatch", h.dispatch)
	mux.HandleFunc("POST /api/cells/{cell}/release", h.release)
	mux.HandleFunc("DELETE /api/sessions/{session}", h.settleSession)
	mux.HandleFunc("/preview/{cell}/", h.preview)
	mux.HandleFunc("/app/{cell}/", h.productionApp)
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (h *Handler) meta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"runners":   access.Runners(),
		"providers": h.Registry.Providers(),
	})
}

type cellView struct {
	Name           string `json:"name"`
	Phase          string `json:"phase"`
	Description    string `json:"description"`
	ActiveSessions int32  `json:"activeSessions"`
	MaxSessions    int32  `json:"maxSessions"`
	PreviewPath    string `json:"previewPath"`
	ProductionPath string `json:"productionPath"`
	ReleaseRef     string `json:"releaseRef"`
	FollowSession  string `json:"followSession"`
	Message        string `json:"message"`
}

func toCellView(c *acv1.Cell) cellView {
	return cellView{
		Name: c.Name, Phase: string(c.Status.Phase), Description: c.Spec.Description,
		ActiveSessions: c.Status.ActiveSessions, MaxSessions: c.Spec.MaxSessions,
		PreviewPath: c.Status.PreviewPath, ProductionPath: c.Status.ProductionPath,
		ReleaseRef: c.Spec.Production.Ref, FollowSession: c.Spec.Preview.FollowSession,
		Message: c.Status.Message,
	}
}

func (h *Handler) listCells(w http.ResponseWriter, r *http.Request) {
	var list acv1.CellList
	if err := h.Client.List(r.Context(), &list, client.InNamespace(h.Namespace)); err != nil {
		writeErr(w, 500, err)
		return
	}
	views := make([]cellView, 0, len(list.Items))
	for i := range list.Items {
		views = append(views, toCellView(&list.Items[i]))
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
	writeJSON(w, 200, map[string]any{"cell": toCellView(&cell), "sessions": sessions})
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

func (h *Handler) proxyTo(w http.ResponseWriter, r *http.Request, cellName, svc string, port int32, prefix string) {
	target := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("%s.%s.svc:%d", svc, ids.WorkloadNamespace(cellName), port),
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprintf(w, "upstream not ready: %v\n", err)
	}
	http.StripPrefix(prefix, proxy).ServeHTTP(w, r)
}
