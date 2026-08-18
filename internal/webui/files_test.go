package webui

import (
	"archive/zip"
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

func newHeaderRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

// An uploaded name is somebody else's string. It arrives from a browser and
// from whatever the file was called on a laptop, and it later becomes a real
// path inside a sandbox — so "../" in a filename must not become a write
// outside the library.
func TestUploadPathCannotEscapeTheLibrary(t *testing.T) {
	for _, tc := range []struct{ dir, name string }{
		{"", "../../etc/passwd"},
		{"../..", "notes.md"},
		{"docs", `..\..\windows\system32\x.txt`},
		{"/absolute", "x.md"},
	} {
		got, err := cleanLibraryPath(tc.dir, tc.name)
		if err == nil && (strings.HasPrefix(got, "../") || strings.HasPrefix(got, "/")) {
			t.Errorf("cleanLibraryPath(%q, %q) = %q — escaped the library", tc.dir, tc.name, got)
		}
	}
	// And the ordinary case still works, including a nested folder.
	got, err := cleanLibraryPath("specs/2026", "需求.md")
	if err != nil || got != "specs/2026/需求.md" {
		t.Errorf("= %q, %v; want specs/2026/需求.md", got, err)
	}
}

// A .md that a browser labels application/octet-stream is still text.
// Trusting the content type alone loses real documents — Macs send that for
// markdown routinely.
func TestTextIsDetectedByContentNotByLabel(t *testing.T) {
	if got := extractText("需求.md", "application/octet-stream", []byte("# 标题\n正文")); got == "" {
		t.Error("a markdown file labelled as a binary was treated as unreadable")
	}
	// And something genuinely binary is not passed off as text: an agent
	// reading NUL-riddled bytes as a document is worse than being told the
	// file is unreadable.
	if got := extractText("logo.png", "image/png", []byte{0x89, 'P', 'N', 'G', 0, 0, 1}); got != "" {
		t.Errorf("binary content was extracted as text: %q", got)
	}
}

// A .docx is a zip of XML. Extracting at upload is what keeps the parser out
// of the sandbox, so it has to actually work on the shape Word produces.
func TestDocxExtractsItsSentences(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte(`<?xml version="1.0"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:body><w:p><w:r><w:t>付款审核要先看单价</w:t></w:r></w:p>
<w:p><w:r><w:t>再看付款周期</w:t></w:r></w:p></w:body></w:document>`))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	got := extractText("规范.docx", "", buf.Bytes())
	for _, want := range []string{"付款审核要先看单价", "再看付款周期"} {
		if !strings.Contains(got, want) {
			t.Errorf("extracted %q, missing %q", got, want)
		}
	}
	// Paragraphs become lines, not one smear.
	if !strings.Contains(got, "\n") {
		t.Errorf("paragraphs were run together: %q", got)
	}
}

// Uploaded content is untrusted and served from the console's own origin,
// so it must never be renderable there by default.
func TestUploadedContentIsNotServedAsRenderableByDefault(t *testing.T) {
	rec := newHeaderRecorder()
	writeDownloadHeaders(rec, "evil.html", false)
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Errorf("disposition = %q, want attachment", got)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff: a text file could be executed as script")
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Error("uploaded content is served without a sandbox policy")
	}
}
