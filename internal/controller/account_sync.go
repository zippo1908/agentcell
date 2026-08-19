package controller

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

// Keeping a connected account alive across sessions.
//
// What this exists for: a Kimi access token lives about fifteen minutes and
// is renewed with a refresh token, and the provider issues a NEW refresh
// token each time. Every session holds its own copy of the credential file,
// so those copies drift into separate refresh lineages — and the copy in the
// Secret, which is what the next session starts from, would otherwise stay
// at whatever it was on the day it was written.
//
// MEASURED, 2026-08-19, against api.kimi.com: issuing a new refresh token
// does NOT invalidate the old one. Two runtimes were given the same
// credential; both refreshed from the same old token, minutes apart, and
// both succeeded. So drift between copies is waste, not breakage — an
// earlier version of this comment claimed the copies killed each other, and
// that claim was never tested. It was wrong.
//
// The waste is still worth removing: a credential belongs to a PERSON, and
// per-session copies of it exist only because KIMI_CODE_HOME points at one
// directory that holds both the login and the conversation state. Pointing
// `credentials_path` at a per-user file would give one lineage per person
// and make most of this file unnecessary.
//
// Until then, the stored copy has to keep up with the live one: the CLI
// refreshes on its own schedule, inside the pod, and there is no way to stop
// it. So the platform reads back what the session now holds and stores that
// as the truth.
//
// The direction matters. Session pods hold no API credential (ADR-0005), so
// they cannot push anything to the control plane; the control plane reaches
// IN over the exec channel it already uses. Nothing new is trusted.

// syncAccountCredential copies a live session's current account credential
// back into its owner's Secret, if the session has a newer one.
//
// Best effort by design: a failure here must never fail a reconcile. The
// worst case of not syncing is the next session starting with a credential
// it has to refresh once; the worst case of failing the reconcile is a
// session that never starts.
func (r *SessionReconciler) syncAccountCredential(ctx context.Context, sess *acv1.Session, ns, id string, uid int64) {
	if r.Exec == nil || sess.Spec.Runner != "kimi" || sess.Spec.OwnerUserID == "" {
		return
	}
	if sess.Status.PodName == "" {
		return
	}
	// Read the PERSON's credential, not the session's.
	//
	// Every session of theirs now shares one directory (the session's own is
	// a symlink to it), so there is one lineage to store rather than several
	// to choose between. -h follows the link in case an older session is
	// still running with a real directory in place.
	home := ids.SessionStateDir(uid, id)
	shared := ids.AccountCredentialDir(uid)
	out, err := r.Exec(ctx, ns, sess.Status.PodName, []string{"sh", "-c",
		`[ -d ` + shellQuoteArg(shared) + ` ] && ` +
			`tar czhf - -C ` + shellQuoteArg(ids.UserHome(uid)) + ` credentials ` +
			`$([ -f ` + shellQuoteArg(home) + `/device_id ] && echo -C ` + shellQuoteArg(home) + ` device_id) | base64 -w0`}, nil)
	if err != nil || strings.TrimSpace(out) == "" {
		return
	}
	blob := strings.TrimSpace(out)

	live, ok := credentialExpiry(blob)
	if !ok {
		// A credential we cannot read the expiry of is one we cannot
		// compare. Storing it anyway risks overwriting a good credential
		// with the wiped remains of a failed refresh — which is exactly
		// what the CLI leaves behind when its token is rejected.
		return
	}
	name := strings.TrimPrefix(sess.Spec.OwnerUserID, "u-") + "-kimi"
	var sec corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: name}, &sec); err != nil {
		return
	}
	if stored, ok := credentialExpiry(string(sec.Data["kimi-credentials"])); ok && stored >= live {
		return
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	sec.Data["kimi-credentials"] = []byte(blob)
	_ = r.Update(ctx, &sec)
}

// credentialExpiry reads expires_at out of a packed credential.
//
// It is the only field that says which of two copies is current. A zero or
// missing value means the CLI cleared the credential after a rejected
// refresh — never newer than anything, and never worth storing.
func credentialExpiry(blob string) (int64, bool) {
	if blob == "" {
		return 0, false
	}
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return 0, false
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return 0, false
	}
	defer func() { _ = zr.Close() }()
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err != nil {
			return 0, false
		}
		if !strings.HasPrefix(h.Name, "credentials/") || !strings.HasSuffix(h.Name, ".json") {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, 1<<20))
		if err != nil {
			return 0, false
		}
		var c struct {
			ExpiresAt    int64  `json:"expires_at"`
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.Unmarshal(body, &c); err != nil {
			return 0, false
		}
		// No refresh token means the credential cannot renew itself: it is
		// the wreckage of a failed refresh, not a newer copy.
		if c.ExpiresAt <= 0 || c.RefreshToken == "" {
			return 0, false
		}
		return c.ExpiresAt, true
	}
}

// shellQuoteArg makes a path safe inside the single shell line this uses.
func shellQuoteArg(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// servePreviewFrom runs this session's preview inside the runtime that hosts
// it, and points the Cell's preview Service at that runtime.
//
// The preview machinery was written when a session was a pod: it selects
// "the session's pod" by label and expects something in that pod to be
// serving. A resident session has neither — it is a window in a runtime
// shared across the owner's projects — so following a session selected no
// pod at all and the preview came up blank, with nothing anywhere saying
// why.
//
// The label goes on the RUNTIME pod because that is what actually holds the
// worktree and can therefore serve it. Only one session per Cell is followed
// at a time, so at most one runtime carries the label for that Cell.
func (r *SessionReconciler) servePreviewFrom(ctx context.Context, sess *acv1.Session, cell *acv1.Cell, ns, id string, uid int64) {
	// An empty command no longer means "no preview" — it means the runtime
	// works one out from the worktree. Only an explicit "off" skips.
	if r.Exec == nil || cell == nil || !cell.Spec.Preview.PreviewEnabled() {
		return
	}
	if cell.Spec.Preview.FollowSession != id {
		return
	}
	pod := sess.Status.PodName
	if pod == "" {
		return
	}
	// The Service selects by session id; the runtime pod is labelled by cell
	// and user, so without this it matches nothing.
	var live corev1.Pod
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: pod}, &live); err == nil {
		if live.Labels[ids.SessionLabelKey] != id {
			if live.Labels == nil {
				live.Labels = map[string]string{}
			}
			live.Labels[ids.SessionLabelKey] = id
			_ = r.Update(ctx, &live)
		}
	}
	argv := append([]string{runtimeapi.RuntimeBin, "preview-start", id,
		strconv.Itoa(int(previewPort(cell)))}, previewArgv(cell)...)
	// Idempotent inside the pod: a pidfile stops a second server fighting
	// the first for the port, so calling this every reconcile is free.
	_, _ = r.Exec(ctx, ns, pod, argv, nil)
}

// previewArgv is the command a preview runs, in the shape the runtime can
// exec. One element means a shell line, which is how the Cell type
// documents it.
func previewArgv(cell *acv1.Cell) []string {
	cmd := cell.Spec.Preview.Command
	if len(cmd) == 1 {
		return []string{"sh", "-c", cmd[0]}
	}
	return cmd
}

// syncLibrary tops up a LIVE session's copy of the project's files.
//
// The library reaches a session through its pod environment, which is fixed
// when the pod is created. So a file uploaded while somebody was working
// could not reach them at all: they had to restart the session, and until
// they knew that, the agent simply could not see the specification they had
// just been told to read.
//
// The Cell carries a marker that changes whenever its files change; a
// session records what it last received. Different means push, which happens
// on the next reconcile — seconds, not a restart.
//
// Best effort, like the credential sync: failing to top up a library must
// never fail a reconcile. The worst case is an agent working from the
// previous version, which is exactly where it was before this existed.
func (r *SessionReconciler) syncLibrary(ctx context.Context, sess *acv1.Session, cell *acv1.Cell, ns, id string) {
	if r.Exec == nil || cell == nil || sess.Status.PodName == "" {
		return
	}
	want := cell.Annotations[acv1.LibraryVersionAnnotation]
	if want == "" || want == sess.Status.LibraryVersion {
		return
	}
	blob := r.libraryBlob(ctx, cell.Name)
	if blob == "" {
		// Nothing readable in the project. Record the version anyway, or
		// every reconcile from now on rebuilds an empty tar.
		sess.Status.LibraryVersion = want
		_ = r.Status().Update(ctx, sess)
		return
	}
	if _, err := r.Exec(ctx, ns, sess.Status.PodName,
		[]string{runtimeapi.RuntimeBin, "library-write", id},
		strings.NewReader(blob)); err != nil {
		return
	}
	sess.Status.LibraryVersion = want
	_ = r.Status().Update(ctx, sess)
}
