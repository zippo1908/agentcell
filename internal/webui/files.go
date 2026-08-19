package webui

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/identity"
)

// A project's own files: the material people bring to the work.
//
// Specs, exported spreadsheets, meeting notes, screenshots of the thing
// that is wrong. Until now the only way to give an agent any of it was to
// commit it to the repository or paste it into a message, which means it
// either pollutes the codebase or exists once and is gone.
//
// The design follows what AIP learned rather than inventing a second one:
// a TEXT LAYER is what reaches the sandbox. Text arrives as itself, office
// documents are extracted to text once at upload, and everything else —
// images, PDFs, archives — stays in the console where a person can look at
// it, listed in the index so the agent knows it exists and can ask for it.
// Pushing binaries into every container costs the same bytes over and over
// for something the agent cannot read anyway.
//
// Extraction happens ONCE, here, not in the sandbox: a runtime that has to
// parse .docx is a runtime that needs a parser, a parser version, and a
// story for when it fails halfway through somebody's session.

// maxUpload bounds one file. Large enough for a slide deck or a year of
// meeting notes; small enough that a mistaken upload of a database dump
// fails immediately instead of filling the volume.
const maxUpload = 25 << 20

// filesFor authorizes access to a project's library and returns the Cell.
func (h *Handler) filesFor(w http.ResponseWriter, r *http.Request, a Action) (*acv1.Cell, bool) {
	var cell acv1.Cell
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: r.PathValue("cell")}, &cell); err != nil {
		writeErr(w, 404, errNotFound)
		return nil, false
	}
	if !h.authorize(w, r, &cell, a) {
		return nil, false
	}
	if h.Auth == nil || h.Auth.Accounts == nil {
		writeErr(w, 501, fmt.Errorf("这个部署没有开启文件库(需要数据库)"))
		return nil, false
	}
	return &cell, true
}

func (h *Handler) listFiles(w http.ResponseWriter, r *http.Request) {
	cell, ok := h.filesFor(w, r, ActionView)
	if !ok {
		return
	}
	files, err := h.Auth.Accounts.DB.Files(r.Context(), cell.Name)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	type view struct {
		Path       string `json:"path"`
		Size       int64  `json:"size"`
		Mime       string `json:"mime"`
		Readable   bool   `json:"readable"`
		UploadedBy string `json:"uploadedBy,omitempty"`
		Created    int64  `json:"created"`
	}
	out := make([]view, 0, len(files))
	for _, f := range files {
		out = append(out, view{
			Path: f.Path, Size: f.Size, Mime: f.Mime,
			// Readable says whether the AGENT can see this one. A person
			// uploading a PDF should find out here, not by wondering why
			// the agent never mentions it.
			Readable: f.HasText, UploadedBy: f.UploadedBy, Created: f.CreatedAt,
		})
	}
	writeJSON(w, 200, out)
}

func (h *Handler) uploadFile(w http.ResponseWriter, r *http.Request) {
	cell, ok := h.filesFor(w, r, ActionDispatch)
	if !ok {
		return
	}
	// Bound the WHOLE body, not just the part we later read.
	//
	// ParseMultipartForm's argument is how much to keep in memory, not a
	// limit on what the client may send: a request with fifty parts, or one
	// part streamed forever, was read to disk and to memory regardless. The
	// reader below makes the connection fail at the limit instead.
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+1<<20)
	if err := r.ParseMultipartForm(maxUpload); err != nil {
		writeErr(w, 400, fmt.Errorf("上传失败:%w", err))
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, fmt.Errorf("没有收到文件"))
		return
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, maxUpload+1))
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if len(body) > maxUpload {
		writeErr(w, 413, fmt.Errorf("单个文件最大 %d MB", maxUpload>>20))
		return
	}
	dest, err := cleanLibraryPath(r.FormValue("path"), hdr.Filename)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	ct := hdr.Header.Get("Content-Type")
	if ct == "" {
		ct = mime.TypeByExtension(strings.ToLower(path.Ext(dest)))
	}
	text := extractText(dest, ct, body)
	p := identity.FromContext(r.Context())
	if err := h.Auth.Accounts.DB.PutFile(r.Context(), cell.Name, dest, ct, text, body, p.Email); err != nil {
		writeErr(w, 500, err)
		return
	}
	h.markLibraryChanged(r, cell)
	writeJSON(w, 200, map[string]any{
		"path": dest, "size": len(body), "readable": text != "",
		"message": readableNote(text != ""),
	})
}

func readableNote(readable bool) string {
	if readable {
		return "已上传,agent 能读到它"
	}
	// Said plainly at the moment of upload. The alternative is somebody
	// uploading a PDF and slowly concluding the agent is ignoring them.
	return "已上传。这个格式抽不出文本,agent 只知道它存在、读不到内容——需要的话转成 md/txt 再传一份"
}

// cleanLibraryPath keeps an upload inside the project's library.
//
// A path arrives from a browser and from whatever the file was called on
// somebody's laptop, so it is not trusted to be relative, to stay inside,
// or to be free of separators that mean something to a filesystem the
// sandbox will later unpack this into.
func cleanLibraryPath(dir, name string) (string, error) {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "" || name == "." || name == "/" {
		return "", fmt.Errorf("文件名不合法")
	}
	dir = strings.Trim(strings.ReplaceAll(dir, `\`, "/"), "/")
	full := path.Clean(path.Join(dir, name))
	if strings.HasPrefix(full, "../") || full == ".." || path.IsAbs(full) {
		return "", fmt.Errorf("路径不能跑到库外面")
	}
	return full, nil
}

func (h *Handler) getFile(w http.ResponseWriter, r *http.Request) {
	cell, ok := h.filesFor(w, r, ActionView)
	if !ok {
		return
	}
	p := strings.TrimPrefix(r.PathValue("path"), "/")

	// ?text=1 asks for the extracted layer — what the agent sees, which is
	// the thing worth previewing for a document nobody can render.
	if r.URL.Query().Get("text") == "1" {
		text, err := h.Auth.Accounts.DB.FileText(r.Context(), cell.Name, p)
		if err != nil {
			writeErr(w, 404, errNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writeDownloadHeaders(w, p, true)
		_, _ = w.Write([]byte(text))
		return
	}

	// Streamed, not buffered: a 25 MB upload used to be read whole into the
	// server's memory on its way out, and out of a database holding one
	// connection for the entire platform.
	rc, size, ct, err := h.Auth.Accounts.DB.OpenFile(r.Context(), cell.Name, p)
	if err != nil {
		writeErr(w, 404, errNotFound)
		return
	}
	defer func() { _ = rc.Close() }()
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	writeDownloadHeaders(w, p, r.URL.Query().Get("inline") == "1")
	_, _ = io.Copy(w, rc)
}

// writeDownloadHeaders makes uploaded content safe to serve from the
// console's own origin.
//
// This content is UNTRUSTED — anybody who can upload can put HTML or a
// script here — and it is served from the origin that holds the session
// cookie. nosniff stops a text file being executed as script, and the
// default disposition is an attachment so a browser never renders it in
// this origin unless the console explicitly asked for an inline preview.
func writeDownloadHeaders(w http.ResponseWriter, p string, inline bool) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	d := "attachment"
	if inline {
		d = "inline"
	}
	w.Header().Set("Content-Disposition", d+"; filename*=UTF-8''"+urlEscape(path.Base(p)))
}

func urlEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '.' || c == '-' || c == '_' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func (h *Handler) deleteFile(w http.ResponseWriter, r *http.Request) {
	cell, ok := h.filesFor(w, r, ActionDispatch)
	if !ok {
		return
	}
	p := strings.TrimPrefix(r.PathValue("path"), "/")
	if err := h.Auth.Accounts.DB.DeleteFile(r.Context(), cell.Name, p); err != nil {
		writeErr(w, 404, errNotFound)
		return
	}
	h.markLibraryChanged(r, cell)
	writeJSON(w, 200, map[string]string{"ok": "deleted"})
}

// --- text extraction --------------------------------------------------

// extractText returns what an agent can read, or "" for formats where the
// honest answer is nothing.
func extractText(name, ct string, body []byte) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".docx"):
		return officeText(body, "word/document.xml")
	case strings.HasSuffix(lower, ".pptx"):
		return officeText(body, "ppt/slides/")
	case strings.HasSuffix(lower, ".xlsx"):
		return xlsxText(body)
	}
	// Anything that IS text is text, whatever the browser called it: a
	// .md uploaded from a Mac arrives as application/octet-stream often
	// enough that trusting the content type alone loses real documents.
	if utf8.Valid(body) && !looksBinary(body) {
		return string(body)
	}
	if strings.HasPrefix(ct, "text/") {
		return string(body)
	}
	return ""
}

// looksBinary reports whether the bytes contain the one thing valid UTF-8
// text does not: a NUL.
func looksBinary(b []byte) bool { return bytes.IndexByte(b, 0) >= 0 }

// maxExtracted bounds the TOTAL text pulled out of one document, and
// maxEntries how many parts are opened at all.
//
// A per-part limit is not a limit: a 200 KB archive can hold hundreds of
// parts that each decompress to the per-part maximum, so the bound that
// matters is the sum. Both are generous for prose and cheap to hold.
const (
	maxExtracted = 8 << 20
	maxEntries   = 512
)

// officeText pulls the visible text out of an OOXML part. Not a converter —
// no styling, no layout, no attempt at fidelity — because what an agent
// needs from a specification is its sentences.
func officeText(body []byte, prefix string) string {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return ""
	}
	var out strings.Builder
	opened := 0
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, prefix) || !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		if opened++; opened > maxEntries {
			break
		}
		// The archive's own claim about how big this part is. A declared
		// size wildly larger than the compressed bytes is the shape of a
		// zip bomb, and refusing on the header costs nothing to check.
		if f.UncompressedSize64 > uint64(maxExtracted) {
			continue
		}
		if remaining := maxExtracted - out.Len(); remaining <= 0 {
			break
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, int64(maxExtracted-out.Len())))
		_ = rc.Close()
		if err != nil {
			continue
		}
		out.WriteString(xmlText(data))
		out.WriteString("\n")
	}
	return strings.TrimSpace(out.String())
}

// xlsxText reads the shared string table, which is where a spreadsheet's
// words live. Numbers stay in the sheets and are not worth reconstructing
// without their coordinates.
func xlsxText(body []byte) string {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return ""
	}
	for _, f := range zr.File {
		if f.Name != "xl/sharedStrings.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return ""
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxExtracted))
		_ = rc.Close()
		if err != nil {
			return ""
		}
		return xmlText(data)
	}
	return ""
}

// xmlText concatenates character data, inserting a newline at paragraph and
// row boundaries so the result reads as lines rather than one long smear.
func xmlText(data []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	// OOXML declares namespaces the strict decoder rejects on some
	// documents; being lenient here is the difference between extracting a
	// spec and extracting nothing.
	dec.Strict = false
	var out strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.CharData:
			out.Write(t)
		case xml.EndElement:
			switch t.Name.Local {
			case "p", "tr", "br", "si":
				out.WriteString("\n")
			}
		}
	}
	return strings.TrimSpace(out.String())
}

// markLibraryChanged tells the control plane a project's files moved.
//
// A running session holds the library it was given when its pod was created,
// so without this an upload reaches nobody until they restart — and nothing
// says so. The reconciler compares this marker with what each session last
// received and tops up the ones that are behind.
//
// Best effort: the upload has already succeeded and the bytes are stored.
// Failing the request now would tell somebody their file did not arrive when
// it did.
func (h *Handler) markLibraryChanged(r *http.Request, cell *acv1.Cell) {
	_ = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var live acv1.Cell
		if err := h.Client.Get(r.Context(),
			types.NamespacedName{Namespace: h.Namespace, Name: cell.Name}, &live); err != nil {
			return err
		}
		if live.Annotations == nil {
			live.Annotations = map[string]string{}
		}
		live.Annotations[acv1.LibraryVersionAnnotation] = strconv.FormatInt(time.Now().UnixNano(), 10)
		return h.Client.Update(r.Context(), &live)
	})
}
