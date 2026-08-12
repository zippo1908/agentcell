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
