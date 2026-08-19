// Package ids is the single place where every derived resource name in
// AgentCell comes from. Nothing else in the codebase may invent a naming
// scheme: controllers, runtime applets and CLIs all call into here, so a
// cold reader can map any object seen in a cluster back to its Cell/Session.
package ids

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/oklog/ulid/v2"
)

// dns1123Label matches what Kubernetes accepts for object names.
var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// MaxCellName keeps derived names ("cell-"+name etc.) comfortably inside
// the 63-char DNS label limit.
const MaxCellName = 40

// ValidateCellName rejects names that cannot be embedded into namespace,
// service and pod names.
func ValidateCellName(name string) error {
	if name == "" {
		return fmt.Errorf("cell name is empty")
	}
	if len(name) > MaxCellName {
		return fmt.Errorf("cell name %q longer than %d chars", name, MaxCellName)
	}
	if !dns1123Label.MatchString(name) {
		return fmt.Errorf("cell name %q is not a lowercase DNS-1123 label", name)
	}
	return nil
}

// NewSessionID returns a lowercase ULID. Lowercase keeps it usable inside
// DNS labels and git branch names alike.
func NewSessionID() string {
	return strings.ToLower(ulid.MustNew(ulid.Now(), rand.Reader).String())
}

// WorkloadNamespace is the dedicated namespace holding a Cell's anchor,
// sessions, PVC and secrets. The Cell/Session CRs themselves live in the
// control namespace, not here.
func WorkloadNamespace(cell string) string { return "cell-" + cell }

// CellFromNamespace is the inverse, for watches that see a workload object
// and must name the Cell it belongs to. Returns "" for namespaces that are
// not a Cell's.
func CellFromNamespace(ns string) string {
	name, ok := strings.CutPrefix(ns, "cell-")
	if !ok {
		return ""
	}
	return name
}

// Fixed names inside a workload namespace: one anchor, one workspace, one
// preview service per Cell, so constants beat derivations.
const (
	AnchorStatefulSet = "anchor"
	WorkspacePVC      = "workspace"
	PreviewService    = "preview"
	// Production (正式区): fully isolated from the dev zone — its own
	// deployment and service, its own clone, never the shared PVC.
	ProdDeployment    = "prod"
	ProdService       = "prod"
	AnchorPodLabelKey = "agentcell.io/role"
	AnchorPodLabelVal = "anchor"
	ProdPodLabelVal   = "prod"
	CellLabelKey      = "agentcell.io/cell"
	SessionLabelKey   = "agentcell.io/session"
)

// SessionName is the Session CR / pod name for a session id.
func SessionName(id string) string { return "sess-" + id }

// SettleJobName is the settle Job for a session id.
func SettleJobName(id string) string { return "settle-" + id }

// SessionBranch is the git branch a session's output is settled onto.
func SessionBranch(id string) string { return "session/" + id }

// RepoPath is where the anchor clones the project inside the shared PVC.
const RepoPath = "/workspace/repo"

// UserHome is a user's private tree on the shared project volume. The
// directory is created 0700 and owned by that user's UID, so a peer's pod —
// running as a different UID, in a different pod — cannot read it even
// though the volume is shared (ADR-0009).
//
// CLI configuration, transcripts, checkpoints and tmux sockets all belong
// here rather than in a shared location.
func UserHome(uid int64) string {
	return "/workspace/users/" + strconv.FormatInt(uid, 10)
}

// WorktreePath is the per-session git worktree. It lives inside the owner's
// private tree, not in a shared directory: an unpublished worktree is the
// user's own working state, and settle is what makes work visible to the
// project.
//
// It stays on the same volume as RepoPath so the worktree shares the object
// store — a git worktree cannot span filesystems.
func WorktreePath(uid int64, id string) string {
	return UserHome(uid) + "/worktrees/" + id
}

// GitSecretName is the workload-namespace copy of the forge credential.
const GitSecretName = "agentcell-git"

// SessionSecretName is the per-session model-credential secret.
func SessionSecretName(id string) string { return "cred-" + id }

// SessionStateDir is where a CLI that resumes by recency keeps this
// session's conversation, inside its owner's private tree. Per session, not
// per user: "the most recent conversation" has to mean this one.
func SessionStateDir(uid int64, id string) string {
	return UserHome(uid) + "/state/" + id
}

// UserRepoPath is a user's own repository: its own refs and its own object
// store, with the project's published history shared read-only underneath
// through git alternates (ADR-0012).
//
// Sessions carve worktrees out of THIS, not out of the shared mirror, so a
// commit an agent makes lands in a 0700 directory its owner owns — and a
// peer's agent is refused by the kernel rather than by a proxy.
func UserRepoPath(uid int64) string { return UserHome(uid) + "/repo" }

// TmuxSocket is the owner's private tmux socket.
//
// Never tmux's default /tmp/tmux-<uid>/default: that path is derived from the
// uid on a filesystem several users share, so it is exactly the place two
// users can collide — and a tmux socket is an authority, not a name. Anything
// that can open it can attach to that terminal.
func TmuxSocket(uid int64) string {
	return UserHome(uid) + "/tmux/agentcell.sock"
}

// TmuxWindow names a session's window inside its owner's tmux server.
func TmuxWindow(id string) string { return "s-" + id }

// UserRuntimeLabelKey marks a pod as one user's runtime for a Cell.
const UserRuntimeLabelKey = "agentcell.io/user"

// TmuxHolder is the session that keeps the server alive when no work is
// open: a tmux server with nothing in it exits.
const TmuxHolder = "agentcell"

// UserRuntimePod is the pod holding one user's tmux server for one Cell.
// One per user, not one per session — the agent CLIs manage conversations
// themselves, so a process per conversation buys nothing.
func UserRuntimePod(uid int64) string { return "runtime-" + strconv.FormatInt(uid, 10) }

// RepoDir is where one repository of a project is checked out.
//
// The empty path keeps the historical location, so a single-repo project is
// byte-for-byte where it always was: no migration, no moved paths, and an
// agent's existing context stays valid. Additional repositories sit beside
// it, one directory each.
func RepoDir(path string) string {
	if path == "" {
		return RepoPath
	}
	return "/workspace/" + path
}

// WorktreeDirFor is where a session's copy of one repository lives.
//
// With a single repository the worktree IS the session directory, exactly as
// before. With several, the session directory holds one subdirectory per
// repository — they have to be under one tree, because the whole reason for
// a project group is that the agent can see both halves of a change at once.
func WorktreeDirFor(uid int64, id, path string) string {
	if path == "" {
		return WorktreePath(uid, id)
	}
	return WorktreePath(uid, id) + "/" + path
}

// UserRepoDirFor is a user's own clone of one repository of the project.
func UserRepoDirFor(uid int64, path string) string {
	if path == "" {
		return UserRepoPath(uid)
	}
	return UserRepoPath(uid) + "-" + strings.ReplaceAll(path, "/", "-")
}

// SlugCellName turns whatever somebody typed into a name the platform can
// actually use.
//
// The rules are real — the name becomes a Kubernetes namespace and a DNS
// label in a preview host — but making a PERSON satisfy them is not. Asking
// for "lowercase letters, digits and dashes" and then refusing 「平台运维组」
// with "is not a lowercase DNS-1123 label" is a form arguing with somebody
// about an implementation detail they never asked to know.
//
// So the typed name is kept as the project's display name and this derives
// the technical one. A name with nothing usable in it — which every purely
// Chinese name is — gets a stable id from its own hash rather than an error:
// two people typing the same name get the same slug, and the collision is
// then a real one that the API reports honestly.
func SlugCellName(typed string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(typed)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == ' ' || r == '.' || r == '/':
			// Collapse runs: "my  project" and "my-project" are the same name
			// to a person, so they should be the same name here.
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > MaxCellName {
		slug = strings.Trim(slug[:MaxCellName], "-")
	}
	// A DNS label cannot start with a digit-only... it can, but a namespace
	// that reads as a number is a poor label; more importantly an empty slug
	// needs something.
	if slug == "" {
		sum := sha256.Sum256([]byte(strings.TrimSpace(typed)))
		return "p-" + hex.EncodeToString(sum[:3])
	}
	return slug
}
