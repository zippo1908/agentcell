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
type ProductionSpec struct {
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
	// first release.
	ProductionPath string `json:"productionPath,omitempty"`
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
