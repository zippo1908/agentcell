// Package ids is the single place where every derived resource name in
// AgentCell comes from. Nothing else in the codebase may invent a naming
// scheme: controllers, runtime applets and CLIs all call into here, so a
// cold reader can map any object seen in a cluster back to its Cell/Session.
package ids

import (
	"crypto/rand"
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
