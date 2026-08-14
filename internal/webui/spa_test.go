package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The SPA must serve real assets as-is and rewrite unknown paths to
// index.html, so a hard reload of /cells/shop or /reviews works.
func TestSPAServesAssetsAndFallsBackToIndex(t *testing.T) {
	h := spaHandler()

	get := func(path string) (int, string, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body, _ := io.ReadAll(rec.Body)
		return rec.Code, rec.Header().Get("Content-Type"), string(body)
	}

	code, ctype, index := get("/")
	if code != http.StatusOK || !strings.Contains(ctype, "text/html") {
		t.Fatalf("GET / = %d %q", code, ctype)
	}
	if !strings.Contains(index, `id="root"`) {
		t.Fatal("index.html does not look like the built SPA entrypoint")
	}

	// A client-side route must return the same entrypoint, not a 404.
	for _, p := range []string{"/cells", "/cells/shop", "/reviews"} {
		code, ctype, body := get(p)
		if code != http.StatusOK || !strings.Contains(ctype, "text/html") {
			t.Errorf("GET %s = %d %q, want 200 html", p, code, ctype)
		}
		if body != index {
			t.Errorf("GET %s did not return the SPA entrypoint", p)
		}
	}

	// The built JS bundle must be served with a JS content type, not HTML.
	i := strings.Index(index, "/assets/")
	if i < 0 {
		t.Fatal("no /assets/ reference in index.html")
	}
	end := strings.IndexAny(index[i:], `"'`)
	asset := index[i : i+end]
	code, ctype, _ = get(asset)
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d", asset, code)
	}
	if strings.Contains(ctype, "text/html") {
		t.Errorf("asset %s served as HTML (%q) — the fallback swallowed a real file", asset, ctype)
	}
}

// The fallback must not turn mistakes into "successful" HTML: an unknown
// API path, a missing asset and a non-GET to a client route are all errors.
func TestSPAFallbackDoesNotMaskErrors(t *testing.T) {
	h := spaHandler()
	do := func(method, path string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec.Code, rec.Header().Get("Content-Type")
	}

	t.Run("unknown api path is a JSON 404", func(t *testing.T) {
		code, ctype := do(http.MethodGet, "/api/nope")
		if code != http.StatusNotFound {
			t.Errorf("GET /api/nope = %d, want 404", code)
		}
		if strings.Contains(ctype, "text/html") {
			t.Errorf("api 404 answered with HTML (%q)", ctype)
		}
	})
	t.Run("missing asset is a 404, not HTML", func(t *testing.T) {
		code, ctype := do(http.MethodGet, "/assets/does-not-exist.js")
		if code != http.StatusNotFound {
			t.Errorf("missing asset = %d, want 404", code)
		}
		if strings.Contains(ctype, "text/html") && code == http.StatusOK {
			t.Error("missing asset served as HTML 200")
		}
	})
	t.Run("POST to a client route is not HTML 200", func(t *testing.T) {
		code, _ := do(http.MethodPost, "/reviews")
		if code == http.StatusOK {
			t.Errorf("POST /reviews = 200; a write to an SPA route must not succeed")
		}
	})
	t.Run("other prefixes do not fall into the SPA", func(t *testing.T) {
		for _, p := range []string{"/preview/shop/x", "/app/shop/x", "/login/extra"} {
			if code, _ := do(http.MethodGet, p); code == http.StatusOK {
				t.Errorf("GET %s fell through to the SPA (200)", p)
			}
		}
	})
	t.Run("known client routes still reload", func(t *testing.T) {
		for _, p := range []string{"/", "/cells", "/cells/shop", "/reviews"} {
			if code, ctype := do(http.MethodGet, p); code != http.StatusOK || !strings.Contains(ctype, "text/html") {
				t.Errorf("GET %s = %d %q, want 200 html", p, code, ctype)
			}
		}
	})
}

// Every route the SPA router owns must survive a hard reload. The allow-list
// is deliberate (an unknown path must not be answered with HTML), which
// makes forgetting to add a new page a real and easy mistake — so the list
// is asserted rather than assumed.
func TestEveryClientRouteSurvivesAReload(t *testing.T) {
	for _, p := range []string{
		"/", "/dashboard", "/cells", "/cells/new", "/cells/shop", "/reviews", "/capabilities",
		"/credentials",
		"/teams",
		"/board",
		"/workspace",
	} {
		if !isClientRoute(p) {
			t.Errorf("%s would 404 on a hard reload", p)
		}
	}
	// And nothing else is swallowed: an unknown path or a mistyped asset has
	// to fail visibly.
	for _, p := range []string{
		"/api/cells", "/api/nope", "/cells/shop/sessions", "/assets/index-xyz.js", "/nope",
	} {
		if isClientRoute(p) {
			t.Errorf("%s was treated as a client route; errors would render as a 200 page", p)
		}
	}
}
