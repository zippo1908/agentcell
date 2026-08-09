package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
}

func TestAuthRequiresBearer(t *testing.T) {
	h := NewAuthenticator("tok-a tok-b").Middleware(okHandler())

	cases := []struct {
		name, path, auth string
		want             int
	}{
		{"no header", "/api/cells", "", 401},
		{"wrong token", "/api/cells", "Bearer nope", 401},
		{"not bearer", "/api/cells", "Basic tok-a", 401},
		{"valid token a", "/api/cells", "Bearer tok-a", 200},
		{"valid token b (rotation)", "/api/cells", "Bearer tok-b", 200},
		{"health exempt", "/healthz", "", 200},
		{"preview also gated", "/preview/shop/", "", 401},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", c.path, nil)
			if c.auth != "" {
				req.Header.Set("Authorization", c.auth)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Errorf("%s %s = %d, want %d", c.auth, c.path, rec.Code, c.want)
			}
		})
	}
}

func TestAuthDisabledPassesThrough(t *testing.T) {
	h := NewAuthenticator("").Middleware(okHandler())
	req := httptest.NewRequest("GET", "/api/cells", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("disabled auth should pass through, got %d", rec.Code)
	}
}
