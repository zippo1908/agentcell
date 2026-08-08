// Package ids is the single place where every derived resource name in
// AgentCell comes from. Nothing else in the codebase may invent a naming
// scheme: controllers, runtime applets and CLIs all call into here, so a
// cold reader can map any object seen in a cluster back to its Cell/Session.
package ids

import (
	"crypto/rand"
	"fmt"
	"regexp"
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
	AnchorPodLabelKey = "agentcell.io/role"
	AnchorPodLabelVal = "anchor"
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

// WorktreePath is the per-session git worktree, on the same PVC so it
// shares the object store with RepoPath.
func WorktreePath(id string) string { return "/workspace/.cells/" + id }

// GitSecretName is the workload-namespace copy of the forge credential.
const GitSecretName = "agentcell-git"

// SessionSecretName is the per-session model-credential secret.
func SessionSecretName(id string) string { return "cred-" + id }
