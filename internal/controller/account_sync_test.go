package controller

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"testing"
)

// packCredential builds what a session's state directory looks like packed.
func packCredential(t *testing.T, body string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{
		Name: "credentials/kimi-code.json", Mode: 0o600, Size: int64(len(body)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// The wreckage of a failed refresh must never be mistaken for a newer
// credential.
//
// When its token is rejected, the CLI rewrites the file with expires_at 0
// and no refresh token. Syncing that back would overwrite a perfectly good
// stored credential with something that cannot renew itself — turning one
// session's bad luck into "re-login required" for every future session.
func TestWipedCredentialIsNeverTreatedAsNewer(t *testing.T) {
	good := packCredential(t, `{"access_token":"a","refresh_token":"r","expires_at":1900000000}`)
	if exp, ok := credentialExpiry(good); !ok || exp != 1900000000 {
		t.Fatalf("a healthy credential read as (%d, %v)", exp, ok)
	}

	for name, body := range map[string]string{
		"cleared after a rejected refresh": `{"access_token":"","expires_at":0}`,
		"no refresh token":                 `{"access_token":"a","expires_at":1900000000}`,
		"not json at all":                  `garbage`,
	} {
		if _, ok := credentialExpiry(packCredential(t, body)); ok {
			t.Errorf("%s: was accepted as a usable credential", name)
		}
	}

	// And something that is not even a credential archive.
	if _, ok := credentialExpiry("not-base64!!"); ok {
		t.Error("unreadable input was accepted as a credential")
	}
	if _, ok := credentialExpiry(""); ok {
		t.Error("an empty blob was accepted as a credential")
	}
}
