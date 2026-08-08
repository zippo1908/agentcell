// Package runtimeapi is the environment-variable contract between the
// controllers (which render pod specs) and cell-runtime (which reads them
// inside anchor, session and settle containers). Nothing else may invent
// variable names on this boundary.
package runtimeapi

const (
	// Anchor container.
	EnvRepoURL       = "AGENTCELL_REPO_URL"
	EnvRepoBranch    = "AGENTCELL_REPO_BRANCH"
	EnvPreviewCmd    = "AGENTCELL_PREVIEW_CMD"    // JSON array, e.g. ["npm","run","dev"]
	EnvPreviewPort   = "AGENTCELL_PREVIEW_PORT"   // informational for probes
	EnvPreviewTarget = "AGENTCELL_PREVIEW_TARGET" // directory the preview serves from

	// Session and settle containers.
	EnvSessionID  = "AGENTCELL_SESSION_ID"
	EnvTask       = "AGENTCELL_TASK"
	EnvRunner     = "AGENTCELL_RUNNER"
	EnvBaseBranch = "AGENTCELL_BASE_BRANCH"

	// Session credential indirection: the pod defines EnvAPIKey from a
	// per-session Secret, and protocol variables reference it via the
	// Kubernetes $(VAR) substitution syntax.
	EnvAPIKey = "AGENTCELL_API_KEY"

	// Git credentials (anchor clone + settle push only; never in session
	// pods). Provided via the workload-namespace copy of the forge secret.
	EnvGitUsername = "GIT_USERNAME"
	EnvGitToken    = "GIT_TOKEN"

	// RuntimeBin is where images bake the static cell-runtime binary.
	RuntimeBin = "/agentcell/cell-runtime"

	// TerminationMessagePath JSON emitted by the settle applet:
	// {"produced":bool,"branch":string,"message":string}.
	SettleResultPath = "/dev/termination-log"
)
