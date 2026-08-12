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
	EnvSessionID   = "AGENTCELL_SESSION_ID"
	EnvTask        = "AGENTCELL_TASK"
	EnvRunner      = "AGENTCELL_RUNNER"
	EnvBaseBranch  = "AGENTCELL_BASE_BRANCH"
	EnvDescription = "AGENTCELL_DESCRIPTION" // the Cell's living product description

	// KnowledgePath is the persistent, session-shared knowledge directory
	// on the workspace PVC (outside the git checkout). The anchor creates
	// it; every session's TASK.md points the agent at it.
	KnowledgePath = "/workspace/knowledge"

	// Session credential indirection: the pod defines EnvAPIKey from a
	// per-session Secret, and protocol variables reference it via the
	// Kubernetes $(VAR) substitution syntax.
	EnvAPIKey = "AGENTCELL_API_KEY"

	// Git credentials for DIRECT mode (anchor clone + settle push only;
	// never in session pods). Provided via the workload-namespace copy of
	// the forge secret. In broker mode (EnvGitBroker set) these are absent —
	// the workload never holds the forge token; see ADR-0005.
	EnvGitUsername = "GIT_USERNAME"
	EnvGitToken    = "GIT_TOKEN"

	// EnvGitBroker, when set, is the base URL of the git-broker service
	// (e.g. http://git-broker.agentcell-system.svc:8080). cell-runtime then
	// routes all git through <broker>/<cell> and authenticates with its
	// projected ServiceAccount token instead of a forge credential.
	EnvGitBroker = "AGENTCELL_GIT_BROKER"
	// EnvCellName is the Cell this workload belongs to; broker URLs are
	// <broker>/<cell>/… and the broker binds it to the pod's namespace.
	EnvCellName = "AGENTCELL_CELL"

	// SATokenPath is an audience-bound projected ServiceAccount token
	// (audience BrokerAudience), mounted only into anchor/settle/prod pods.
	// cell-runtime sends it as the git password in broker mode. It is NOT
	// the default kube-apiserver token: a token scoped to the broker cannot
	// be replayed against the apiserver, and vice-versa.
	SATokenPath = "/var/run/secrets/agentcell/git-broker/token"
	// BrokerAudience is the audience the broker requires on the SA token.
	BrokerAudience = "agentcell-git-broker"
	// BrokerGitUser is the fixed basic-auth username workloads present to
	// the broker; the password carries the SA token.
	BrokerGitUser = "x-access-token"

	// Dedicated ServiceAccount names per git role, so the broker can tell
	// anchor/prod (fetch only) from settle (the only role allowed to push).
	SAAnchor = "anchor"
	SASettle = "settle"
	SAProd   = "prod"
	// SACelld is the control plane's own ServiceAccount; only it may use
	// the broker's narrow forge REST route (ADR-0006).
	SACelld = "celld"

	// BrokerClientLabel marks pods permitted to reach the broker; the
	// NetworkPolicy egress rule selects on it, so session pods (which lack
	// it and have no token) cannot reach the broker at all.
	BrokerClientLabelKey = "agentcell.io/broker-client"
	BrokerClientLabelVal = "true"

	// BrokerTokenVolume is the projected-token volume name.
	BrokerTokenVolume = "git-broker-token"
	// BrokerTokenMount is where that volume is mounted.
	BrokerTokenMount = "/var/run/secrets/agentcell/git-broker"

	// Production (正式区) container: fresh shallow clone per release,
	// on an emptyDir — never the dev-zone PVC.
	EnvProdRef       = "AGENTCELL_PROD_REF"
	EnvProdCmd       = "AGENTCELL_PROD_CMD" // JSON array
	EnvProdReleaseID = "AGENTCELL_PROD_RELEASE_ID"

	// ProdRepoPath is where the prod applet clones the release checkout
	// (backed by an emptyDir volume in the prod pod).
	ProdRepoPath = "/prodspace/repo"

	// RuntimeBin is where images bake the static cell-runtime binary.
	RuntimeBin = "/agentcell/cell-runtime"

	// TerminationMessagePath JSON emitted by the settle applet:
	// {"produced":bool,"branch":string,"message":string}.
	SettleResultPath = "/dev/termination-log"
)
