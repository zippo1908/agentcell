package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Uploaded bytes live on the volume, not in the database.
//
// They used to be BLOB columns in the same SQLite file as accounts, roles and
// grants — and that database is opened with SetMaxOpenConns(1), because
// SQLite takes one writer at a time and a larger pool only turns contention
// into "database is locked". One connection for the whole store means one
// connection for the whole PLATFORM: while a 25 MB download was being read
// out of a BLOB, every login, every authorization check and every mention
// lookup queued behind it.
//
// Nothing about that is SQLite's fault. A policy store is small, hot and
// transactional; a file library is large, cold and streamed. They are
// different workloads that happened to share a file.
//
// So the row keeps what a listing needs — path, size, type, who, when — and
// the bytes go to a directory beside the database. Reads stream from the
// filesystem and hold no database connection at all.

// blobStore is a directory of uploaded bytes, laid out by project.
type blobStore struct{ root string }

// blobName is derived, never taken from the upload.
//
// A user-supplied path must never become a filesystem path: that is how
// "../../etc" gets written. Hashing the logical path gives a name that is
// hex by construction, is stable across replacement (so an upsert overwrites
// rather than orphaning), and cannot escape the directory.
func blobName(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}

// cellDir refuses a project name that could be anything other than one
// directory component. The API validates names already; this is the layer
// that would be holding the knife if it ever stopped.
func (b blobStore) cellDir(cell string) (string, error) {
	if cell == "" || strings.ContainsAny(cell, `/\.`) {
		return "", fmt.Errorf("bad project name for storage: %q", cell)
	}
	return filepath.Join(b.root, cell), nil
}

func (b blobStore) paths(cell, path string) (content, text string, err error) {
	dir, err := b.cellDir(cell)
	if err != nil {
		return "", "", err
	}
	n := blobName(path)
	return filepath.Join(dir, n), filepath.Join(dir, n+".txt"), nil
}

// put writes both layers, content first.
//
// Written before the row, so a crash between the two leaves bytes nobody
// references — wasted space, which the next upload to the same path
// overwrites. The other order would leave a row promising a file that is not
// there, and that is a download that 500s forever.
func (b blobStore) put(cell, path string, content []byte, text string) error {
	cpath, tpath, err := b.paths(cell, path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cpath), 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(cpath, content); err != nil {
		return err
	}
	return writeFileAtomic(tpath, []byte(text))
}

// writeFileAtomic makes a reader see either the old file or the new one.
// A download that arrives mid-write would otherwise get half a document.
func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// open returns a reader over the content. The caller closes it; no database
// connection is involved, which is the entire point of this file.
func (b blobStore) open(cell, path string) (io.ReadCloser, int64, error) {
	cpath, _, err := b.paths(cell, path)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(cpath)
	if err != nil {
		return nil, 0, ErrNotFound
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

func (b blobStore) text(cell, path string) (string, error) {
	_, tpath, err := b.paths(cell, path)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(tpath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(raw), nil
}

// remove drops both layers. A missing file is not an error: the row is the
// record, and this is only reclaiming space behind it.
func (b blobStore) remove(cell, path string) error {
	cpath, tpath, err := b.paths(cell, path)
	if err != nil {
		return err
	}
	_ = os.Remove(cpath)
	_ = os.Remove(tpath)
	return nil
}

func (b blobStore) removeCell(cell string) error {
	dir, err := b.cellDir(cell)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
