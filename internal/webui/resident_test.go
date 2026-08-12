package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
)

func residentSession(name string, resident bool, phase acv1.SessionPhase) *acv1.Session {
	s := sessionOwnedBy(name, alice, phase, false)
	s.Spec.Resident = resident
	return s
}

func postContinue(h *Handler, name, body string, as any) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+name+"/continue", strings.NewReader(body))
	req.SetPathValue("session", name)
	rec := httptest.NewRecorder()
	switch p := as.(type) {
	case nil:
		h.continueSession(rec, req)
	default:
		_ = p
		h.continueSession(rec, asUser(req, alice))
	}
	return rec
}

// Continuing a session is talking to a live terminal: only its owner may.
func TestContinueRefusesNonOwnersWith404(t *testing.T) {
	_, h := ownedFixture(t, residentSession("sess-b", true, acv1.SessionRunning))
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-b/continue",
		strings.NewReader(`{"text":"hi"}`))
	req.SetPathValue("session", "sess-b")
	rec := httptest.NewRecorder()
	h.continueSession(rec, asUser(req, bob)) // sess-b is owned by alice
	if rec.Code != http.StatusNotFound {
		t.Errorf("continue by a non-owner = %d, want 404", rec.Code)
	}
}

// A one-shot session has no terminal to talk to; saying so beats a confusing
// exec failure.
func TestContinueRefusesNonResidentSessions(t *testing.T) {
	_, h := ownedFixture(t, residentSession("sess-a", false, acv1.SessionRunning))
	if rec := postContinue(h, "sess-a", `{"text":"hi"}`, alice); rec.Code != http.StatusConflict {
		t.Errorf("continue on a one-shot session = %d, want 409", rec.Code)
	}
}

func TestContinueRefusesSessionsThatAreNotRunning(t *testing.T) {
	for _, phase := range []acv1.SessionPhase{acv1.SessionSettling, acv1.SessionSettled, acv1.SessionQueued} {
		_, h := ownedFixture(t, residentSession("sess-a", true, phase))
		if rec := postContinue(h, "sess-a", `{"text":"hi"}`, alice); rec.Code != http.StatusConflict {
			t.Errorf("continue on a %s session = %d, want 409", phase, rec.Code)
		}
	}
}

func TestContinueRejectsEmptyText(t *testing.T) {
	_, h := ownedFixture(t, residentSession("sess-a", true, acv1.SessionRunning))
	for _, body := range []string{`{"text":""}`, `{"text":"   "}`, `{}`} {
		if rec := postContinue(h, "sess-a", body, alice); rec.Code != http.StatusBadRequest {
			t.Errorf("continue with %s = %d, want 400", body, rec.Code)
		}
	}
}

// State is an ownership-gated question too: whether a session exists at all
// must not leak (ADR-0008).
func TestSessionStateRefusesNonOwnersWith404(t *testing.T) {
	_, h := ownedFixture(t, residentSession("sess-b", true, acv1.SessionRunning))
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/sess-b/state", nil)
	req.SetPathValue("session", "sess-b")
	rec := httptest.NewRecorder()
	h.sessionState(rec, asUser(req, bob))
	if rec.Code != http.StatusNotFound {
		t.Errorf("state for a non-owner = %d, want 404", rec.Code)
	}
}
