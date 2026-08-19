package store

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, db *DB, cell, path string) []byte {
	t.Helper()
	rc, _, _, err := db.OpenFile(t.Context(), cell, path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The point of the whole change: the policy database must not grow with
// uploads.
//
// It used to. Accounts, roles and grants shared one SQLite file with the
// file library, and that file is opened with SetMaxOpenConns(1) — so one
// connection for the store meant one connection for the platform, and a
// 25 MB download queued in front of every login and every authorization
// check. Keeping the bytes out is what makes those two workloads stop
// competing.
func TestUploadsDoNotGrowThePolicyDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	before := fileSize(t, path)
	big := bytes.Repeat([]byte("x"), 5<<20) // 5 MB
	if err := db.PutFile(t.Context(), "shop", "spec.bin", "application/octet-stream", "", big, "zhu@tinci.com"); err != nil {
		t.Fatal(err)
	}
	// WAL means the write may not be in the main file yet; check both.
	after := fileSize(t, path) + fileSize(t, path+"-wal")

	if grew := after - before; grew > 1<<20 {
		t.Fatalf("the policy database grew by %d bytes for a 5 MB upload; the bytes are still in it", grew)
	}
	if got := read(t, db, "shop", "spec.bin"); len(got) != len(big) {
		t.Fatalf("content did not survive the trip: got %d bytes, want %d", len(got), len(big))
	}
}

func fileSize(t *testing.T, p string) int64 {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

func TestFileRoundTrip(t *testing.T) {
	db := open(t)
	ctx := t.Context()

	if err := db.PutFile(ctx, "shop", "notes/plan.md", "text/markdown", "写在这里", []byte("# 计划"), "zhu@tinci.com"); err != nil {
		t.Fatal(err)
	}

	files, err := db.Files(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "notes/plan.md" {
		t.Fatalf("listing = %+v", files)
	}
	if !files[0].HasText {
		t.Error("a file with extracted text must be listed as readable by the agent")
	}
	if files[0].Size != int64(len("# 计划")) {
		t.Errorf("size = %d, want %d", files[0].Size, len("# 计划"))
	}
	if got := string(read(t, db, "shop", "notes/plan.md")); got != "# 计划" {
		t.Errorf("content = %q", got)
	}
	text, err := db.FileText(ctx, "shop", "notes/plan.md")
	if err != nil || text != "写在这里" {
		t.Errorf("text = %q, %v", text, err)
	}
	// The text layer is what reaches a sandbox.
	layer, err := db.TextLayer(ctx, "shop")
	if err != nil || layer["notes/plan.md"] != "写在这里" {
		t.Errorf("text layer = %v, %v", layer, err)
	}
}

// Replacing a file must not leave the old bytes behind for the next reader.
func TestReplacingAFileReplacesItsBytes(t *testing.T) {
	db := open(t)
	ctx := t.Context()
	if err := db.PutFile(ctx, "shop", "a.txt", "text/plain", "old", []byte("old"), "a@b.c"); err != nil {
		t.Fatal(err)
	}
	if err := db.PutFile(ctx, "shop", "a.txt", "text/plain", "new", []byte("new"), "a@b.c"); err != nil {
		t.Fatal(err)
	}

	if got := string(read(t, db, "shop", "a.txt")); got != "new" {
		t.Fatalf("content = %q, want the replacement", got)
	}
	if files, _ := db.Files(ctx, "shop"); len(files) != 1 {
		t.Fatalf("replacing produced %d rows, want 1", len(files))
	}
}

// A path arrives from a browser and from whatever the file was called on
// somebody's laptop. It must never become a filesystem path.
func TestAnUploadCannotEscapeItsDirectory(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	evil := "../../../escaped"
	if err := db.PutFile(t.Context(), "shop", evil, "text/plain", "", []byte("pwned"), "a@b.c"); err != nil {
		t.Fatal(err)
	}

	// Nothing may exist outside the blob root, whatever the path claimed.
	if _, err := os.Stat(filepath.Join(dir, "escaped")); err == nil {
		t.Fatal("an upload escaped the blob directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "escaped")); err == nil {
		t.Fatal("an upload escaped the blob directory")
	}
	// And it is still readable under its logical name.
	if got := string(read(t, db, "shop", evil)); got != "pwned" {
		t.Errorf("content = %q", got)
	}
}

// A project name could only ever be one directory component. The API
// validates it; this is the layer that would be holding the knife if it
// stopped.
func TestAProjectNameCannotBeAPath(t *testing.T) {
	db := open(t)
	err := db.PutFile(t.Context(), "../evil", "a.txt", "text/plain", "", []byte("x"), "a@b.c")
	if err == nil {
		t.Fatal("a project name with a separator must be refused")
	}
	if !strings.Contains(err.Error(), "bad project name") {
		t.Errorf("refusal should name the problem, got %v", err)
	}
}

func TestDeletingAFileRemovesItsBytes(t *testing.T) {
	db := open(t)
	ctx := t.Context()
	if err := db.PutFile(ctx, "shop", "a.txt", "text/plain", "t", []byte("x"), "a@b.c"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteFile(ctx, "shop", "a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := db.OpenFile(ctx, "shop", "a.txt"); err != ErrNotFound {
		t.Fatalf("a deleted file is still readable: %v", err)
	}
	if err := db.DeleteFile(ctx, "shop", "a.txt"); err != ErrNotFound {
		t.Errorf("deleting twice should say not found, got %v", err)
	}
}

func TestDeletingAProjectTakesItsWholeLibrary(t *testing.T) {
	db := open(t)
	ctx := t.Context()
	for _, n := range []string{"a.txt", "b.txt"} {
		if err := db.PutFile(ctx, "shop", n, "text/plain", "t", []byte("x"), "a@b.c"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.DeleteCellFiles(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	if files, _ := db.Files(ctx, "shop"); len(files) != 0 {
		t.Fatalf("rows survived: %v", files)
	}
	if _, _, _, err := db.OpenFile(ctx, "shop", "a.txt"); err != ErrNotFound {
		t.Errorf("bytes survived the project: %v", err)
	}
}

// An existing deployment has rows whose bytes are still in the database.
// Starting up must move them out — and must be safe to interrupt.
func TestLegacyBlobsAreMovedOntoTheVolumeOnStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	// Build a database in the OLD shape and put a row in it by hand.
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`ALTER TABLE files ADD COLUMN content BLOB`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`ALTER TABLE files ADD COLUMN text TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(
		`INSERT INTO files (cell, path, size, mime, text, content, uploaded_by, created_at)
		 VALUES ('shop','old.md',6,'text/markdown','抽出来的正文',?, 'a@b.c', 0)`,
		[]byte("# 旧的")); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening runs the migration.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopening a legacy database must work: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })

	if got := string(read(t, db2, "shop", "old.md")); got != "# 旧的" {
		t.Errorf("legacy content did not reach the volume: %q", got)
	}
	text, err := db2.FileText(t.Context(), "shop", "old.md")
	if err != nil || text != "抽出来的正文" {
		t.Errorf("legacy text did not reach the volume: %q %v", text, err)
	}
	// has_text has to be reconstructed, or the file silently stops being
	// delivered into sandboxes.
	files, err := db2.Files(t.Context(), "shop")
	if err != nil || len(files) != 1 || !files[0].HasText {
		t.Fatalf("listing after migration = %+v (%v)", files, err)
	}
	// And the bytes are gone from the row.
	var content []byte
	if err := db2.sql.QueryRow(`SELECT content FROM files WHERE path='old.md'`).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if len(content) != 0 {
		t.Errorf("the row still holds %d bytes", len(content))
	}

	// Running it again is a no-op, not a second migration.
	if err := db2.drainFileBlobs(); err != nil {
		t.Errorf("re-running the migration failed: %v", err)
	}
}
