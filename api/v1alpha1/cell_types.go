package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RepoSpec points a Cell at its git repository.
type RepoSpec struct {
	// URL is the https clone URL.
	URL string `json:"url"`
	// Branch is the base branch sessions fork from and settle against.
	// Defaults to "main".
	Branch string `json:"branch,omitempty"`
	// SecretName references a kubernetes.io/basic-auth Secret in the
	// control namespace (username + password/token). The operator copies it
	// into the workload namespace; session pods never see it — only the
	// anchor (clone) and settle jobs (push) do.
	SecretName string `json:"secretName,omitempty"`
}

// PreviewSpec keeps a live product preview running for the whole life of
// the Cell, so the user can watch the agent's work and recalibrate the
// product description against what they see.
type PreviewSpec struct {
	// Command is run (via sh -c when len==1, else exec'd) in the preview
	// target directory by the anchor, restarted with backoff when it exits.
	Command []string `json:"command,omitempty"`
	// Port the command serves HTTP on inside the anchor pod.
	Port int32 `json:"port,omitempty"`
	// FollowSession switches the preview working directory to that
	// session's worktree so the user watches work-in-progress live; empty
	// means the main checkout.
	FollowSession string `json:"followSession,omitempty"`
}

// ProductionSpec is the Cell's 正式区: a deployment fully isolated from the
// dev zone (own shallow clone on an emptyDir, never the shared PVC), which
// changes only on an explicit release action. Dev/test debugging — preview
// restarts, session churn, dirty worktrees — cannot touch it.
// ProductionTarget says where a released build actually runs.
type ProductionTarget string

const (
	// ProductionInCell runs production inside the Cell, in its own zone:
	// isolated from the dev zone, reachable through the platform. Good for
	// products whose production IS this environment.
	ProductionInCell ProductionTarget = "incell"
	// ProductionExternal hands off instead: AgentCell publishes the release
	// and notifies something else, which owns running it.
	//
	// This is the honest shape once production is somebody else's system —
	// a cluster with its own pipeline, a CDN, an app store. Pretending to
	// manage it from here would mean a second, weaker deployer competing
	// with the real one.
	ProductionExternal ProductionTarget = "external"
)

type ProductionSpec struct {
	// Target selects in-Cell production or a handoff. Empty means incell,
	// which is what every existing Cell already does.
	Target ProductionTarget `json:"target,omitempty"`
	// ExternalURL is where the released product actually lives when Target
	// is external. The console links to it and does NOT proxy it: it is not
	// our origin, it may have its own auth, and routing it through the
	// preview proxy would break both.
	ExternalURL string `json:"externalURL,omitempty"`
	// Webhook is called on release when Target is external. The body is
	// signed so the receiver can tell a real release from anything else that
	// found the URL.
	Webhook WebhookSpec `json:"webhook,omitempty"`
	// Command serves the production app (run in the release checkout).
	Command []string `json:"command,omitempty"`
	// Port the command serves HTTP on. Defaults to the preview port.
	Port int32 `json:"port,omitempty"`
	// Ref is the git ref a release ships — branch, tag or commit SHA.
	// Defaults to the repo base branch. The resolved SHA is recorded in
	// /prodspace/RELEASE_SHA inside the prod pod and printed in its log.
	Ref string `json:"ref,omitempty"`
	// ReleaseID changes on every release action; a new value rolls the
	// production pod, which re-clones Ref. Empty = never released, no
	// production zone exists.
	ReleaseID string `json:"releaseID,omitempty"`
}

// ResourceBudget is the per-session slot budget, expressed as Kubernetes
// quantity strings.
type ResourceBudget struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// CellSpec is the desired state of one project's resident instance.
type CellSpec struct {
	Repo RepoSpec `json:"repo"`
	// Image is the devbox image for the anchor and session pods; it must
	// contain the agent CLIs plus git and tmux. cell-runtime is baked in at
	// image build time.
	Image string `json:"image"`
	// Description is the living product description the user calibrates
	// while watching the preview; dispatches default to carrying it.
	Description string `json:"description,omitempty"`
	// Members grants roles on this Cell. EMPTY means open to every
	// authenticated user, which is what every existing Cell already does —
	// a permission model that locked people out of their own projects on
	// upgrade would be reverted before it was understood.
	Members []Member `json:"members,omitempty"`
	// MaxSessions is the slot count (instance concurrency). Defaults to 2.
	MaxSessions int32 `json:"maxSessions,omitempty"`
	// SessionResources is the per-slot budget. Defaults to 1 CPU / 2Gi.
	SessionResources ResourceBudget `json:"sessionResources,omitempty"`
	// PreviewResources bounds the resident preview (and the production pod,
	// which runs the same command). A dev server's footprint is a property
	// of the project — a Vite app and a JVM are not in the same class — so
	// this is where an operator states what theirs needs. Defaults to
	// 2 CPU / 4Gi limits with modest requests.
	PreviewResources ResourceBudget `json:"previewResources,omitempty"`
	// WorkspaceSize is the PVC size, default "10Gi".
	WorkspaceSize string `json:"workspaceSize,omitempty"`
	// StorageClassName optionally pins the PVC's storage class (cloud
	// presets set this: Alibaba disk/NAS, Tencent CBS/CFS).
	StorageClassName string         `json:"storageClassName,omitempty"`
	Preview          PreviewSpec    `json:"preview,omitempty"`
	Production       ProductionSpec `json:"production,omitempty"`
}

// CellPhase summarizes observed state.
type CellPhase string

const (
	CellPending CellPhase = "Pending"
	CellReady   CellPhase = "Ready"
	CellError   CellPhase = "Error"
)

// CellStatus is the observed state of a Cell.
type CellStatus struct {
	Phase              CellPhase `json:"phase,omitempty"`
	ObservedGeneration int64     `json:"observedGeneration,omitempty"`
	ActiveSessions     int32     `json:"activeSessions,omitempty"`
	// PreviewPath is the platform-relative dev-zone URL (celld proxies it).
	PreviewPath string `json:"previewPath,omitempty"`
	// ProductionPath is the platform-relative 正式区 URL; empty until the
	// first release, and empty for external production, which is not ours to
	// serve.
	ProductionPath string `json:"productionPath,omitempty"`
	// HandedOffRelease is the last ReleaseID an external deployer was told
	// about. It exists so the notification fires once per release rather
	// than on every reconcile — a deploy trigger is not idempotent from the
	// receiver's point of view.
	HandedOffRelease string `json:"handedOffRelease,omitempty"`
	// HandoffMessage carries the deployer's refusal, if it refused. Surfaced
	// rather than retried: a deployer saying no is telling the operator
	// something.
	HandoffMessage string `json:"handoffMessage,omitempty"`
	// SlotLeases are the session ids currently holding a slot. Admission
	// appends here through the apiserver's optimistic concurrency
	// (resourceVersion CAS), which makes the slot gate race-free even with
	// concurrent reconcilers or multiple controller replicas.
	SlotLeases []string `json:"slotLeases,omitempty"`
	Message    string   `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Cell is a project's resident instance: workload namespace + anchor
// StatefulSet + shared workspace PVC + resident preview.
type Cell struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CellSpec   `json:"spec,omitempty"`
	Status CellStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CellList contains a list of Cell.
type CellList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Cell `json:"items"`
}

// WebhookSpec is the handoff to an external deployer.
type WebhookSpec struct {
	// URL receives a POST on every release.
	URL string `json:"url,omitempty"`
	// SecretName holds an HMAC key under "key". Without it the receiver
	// cannot distinguish a release from anyone who learned the URL, so a
	// webhook configured without a secret is refused rather than sent
	// unsigned.
	SecretName string `json:"secretName,omitempty"`
}

// Role is what a member may do in a Cell.
type Role string

const (
	// RoleViewer sees the Cell, its settled sessions and its reviews.
	RoleViewer Role = "viewer"
	// RoleMember also dispatches, runs sessions and reviews.
	RoleMember Role = "member"
	// RoleMaintainer also releases, edits settings and manages members.
	//
	// Release is the line that matters: everything else is recoverable, and
	// a release is the one action that puts code in front of users.
	RoleMaintainer Role = "maintainer"
)

// Member is one user's role. UserID is the hashed principal id, the same
// value a Session records as its owner.
type Member struct {
	UserID string `json:"userID"`
	Role   Role   `json:"role"`
}
