package main

// The re-login regression: the old dirEmpty guard could not tell "Secret
// holds an older capture of the same login" from "Secret holds a NEW login",
// so a fresh grant never landed and the dead file won forever. The blob's
// hash is the version: same capture skips (live rotated file wins), a new
// capture always replaces.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// acctBlob builds a base64 tar.gz in the shape the login flow captures:
// credentials/kimi-code.json plus device_id.
func acctBlob(t *testing.T, token, device string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct{ name, body string }{
		{"credentials/kimi-code.json", `{"access_token":"` + token + `"}`},
		{"device_id", device},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o600, Size: int64(len(f.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestInstallAccountCredential(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "state", "s1")
	shared := filepath.Join(tmp, "shared")

	// First install lands the credential and the device identity.
	if err := installAccountCredential(home, shared, acctBlob(t, "tok-v1", "dev-v1")); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if got := readFile(t, filepath.Join(shared, "kimi-code.json")); got != `{"access_token":"tok-v1"}` {
		t.Fatalf("first install content: %q", got)
	}

	// Rotation: the CLI rewrites the live file. A later open with the SAME
	// blob must not roll it back to the Secret's older capture.
	if err := os.WriteFile(filepath.Join(shared, "kimi-code.json"), []byte(`{"access_token":"tok-v1-rotated"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installAccountCredential(home, shared, acctBlob(t, "tok-v1", "dev-v1")); err != nil {
		t.Fatalf("same-blob open: %v", err)
	}
	if got := readFile(t, filepath.Join(shared, "kimi-code.json")); got != `{"access_token":"tok-v1-rotated"}` {
		t.Fatalf("same blob rolled the live file back: %q", got)
	}

	// Re-login: a NEW blob must replace the credential, and the session's
	// device_id must follow the new login.
	if err := installAccountCredential(home, shared, acctBlob(t, "tok-v2", "dev-v2")); err != nil {
		t.Fatalf("re-login open: %v", err)
	}
	if got := readFile(t, filepath.Join(shared, "kimi-code.json")); got != `{"access_token":"tok-v2"}` {
		t.Fatalf("re-login did not replace the credential: %q", got)
	}
	if got := readFile(t, filepath.Join(home, "device_id")); got != "dev-v2" {
		t.Fatalf("device did not follow the new login: %q", got)
	}

	// A sibling session opening afterwards: the shared credential is current
	// (skipped), but its own device_id must still land — an old device with a
	// new token reads as theft to the provider.
	home2 := filepath.Join(tmp, "state", "s2")
	if err := installAccountCredential(home2, shared, acctBlob(t, "tok-v2", "dev-v2")); err != nil {
		t.Fatalf("sibling open: %v", err)
	}
	if got := readFile(t, filepath.Join(home2, "device_id")); got != "dev-v2" {
		t.Fatalf("sibling kept the old device: %q", got)
	}
}
