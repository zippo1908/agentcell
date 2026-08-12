package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
)

// ADR-0006: the review queue is Sessions that settled with output. Approval
// records the verdict on the CR; the Session controller then opens the PR
// through the broker and tracks its merge state.

type reviewView struct {
	Session  string `json:"session"`
	Cell     string `json:"cell"`
	Task     string `json:"task"`
	Branch   string `json:"branch"`
	State    string `json:"state"`
	Note     string `json:"note"`
	PRURL    string `json:"prURL"`
	PRNumber int    `json:"prNumber"`
	PRState  string `json:"prState"`
	Settled  string `json:"settled"`
}

func toReviewView(s *acv1.Session) reviewView {
	v := reviewView{
		Session: s.Name, Cell: s.Spec.Cell, Task: s.Spec.Task,
		Branch: s.Status.Branch, State: string(s.Status.ReviewState),
		Note: s.Status.ReviewNote, PRURL: s.Status.PRURL,
		PRNumber: s.Status.PRNumber, PRState: s.Status.PRState,
	}
	if v.State == "" {
		v.State = string(acv1.ReviewPending)
	}
	if s.Status.StartTime != nil {
		v.Settled = s.Status.StartTime.UTC().Format("2006-01-02 15:04Z")
	}
	return v
}

// listReviews returns settled-with-output sessions, newest first. ?cell=
// filters to one Cell; ?state=Pending|Approved|Rejected filters the verdict.
func (h *Handler) listReviews(w http.ResponseWriter, r *http.Request) {
	var list acv1.SessionList
	if err := h.Client.List(r.Context(), &list, client.InNamespace(h.Namespace)); err != nil {
		writeErr(w, 500, err)
		return
	}
	cellFilter := r.URL.Query().Get("cell")
	stateFilter := r.URL.Query().Get("state")
	out := []reviewView{}
	for i := range list.Items {
		s := &list.Items[i]
		if !s.Status.Produced || s.Status.Phase != acv1.SessionSettled {
			continue
		}
		if cellFilter != "" && s.Spec.Cell != cellFilter {
			continue
		}
		v := toReviewView(s)
		if stateFilter != "" && v.State != stateFilter {
			continue
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Session > out[j].Session })
	writeJSON(w, 200, out)
}

// sessionDiff proxies a compare of the session branch through the broker,
// so celld itself never holds a forge credential.
func (h *Handler) sessionDiff(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("session")
	var sess acv1.Session
	if err := h.Client.Get(r.Context(), types.NamespacedName{Namespace: h.Namespace, Name: name}, &sess); err != nil {
		writeErr(w, 404, err)
		return
	}
	if !h.Forge.Enabled() {
		writeErr(w, 501, fmt.Errorf("diff requires the git-broker to be configured"))
		return
	}
	if sess.Status.SessionID == "" || !sess.Status.Produced {
		writeErr(w, 400, fmt.Errorf("session has no settled output"))
		return
	}
	res, err := h.Forge.Compare(r.Context(), sess.Spec.Cell, sess.Status.SessionID)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, res)
}

// reviewSession records the human verdict. Approval is picked up by the
// Session controller, which opens the PR through the broker.
func (h *Handler) reviewSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("session")
	var body struct {
		Decision string `json:"decision"` // approve | reject
		Note     string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	var sess acv1.Session
	if err := h.Client.Get(r.Context(), types.NamespacedName{Namespace: h.Namespace, Name: name}, &sess); err != nil {
		writeErr(w, 404, err)
		return
	}
	if !sess.Status.Produced || sess.Status.Phase != acv1.SessionSettled {
		writeErr(w, 400, fmt.Errorf("only a settled session with output can be reviewed"))
		return
	}
	// The verdict is a one-way transition, enforced here rather than by the
	// UI hiding buttons: Pending → Approved | Rejected, and no reversal.
	// Re-deciding an approved session is especially dangerous once a PR
	// exists — the branch is already in flight.
	if cur := sess.Status.ReviewState; cur != "" && cur != acv1.ReviewPending {
		writeErr(w, http.StatusConflict,
			fmt.Errorf("session is already %s and cannot be reviewed again", cur))
		return
	}
	if sess.Status.PRNumber != 0 {
		writeErr(w, http.StatusConflict,
			fmt.Errorf("a pull request (#%d) already exists for this session", sess.Status.PRNumber))
		return
	}
	note := strings.TrimSpace(body.Note)
	switch body.Decision {
	case "approve":
		sess.Status.ReviewState = acv1.ReviewApproved
	case "reject":
		// A rejection without a reason is useless to whoever picks up the
		// follow-up dispatch.
		if note == "" {
			writeErr(w, 400, fmt.Errorf("a rejection requires a reason"))
			return
		}
		sess.Status.ReviewState = acv1.ReviewRejected
	default:
		writeErr(w, 400, fmt.Errorf("decision must be approve or reject"))
		return
	}
	sess.Status.ReviewNote = note
	if err := h.Client.Status().Update(r.Context(), &sess); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, toReviewView(&sess))
}
