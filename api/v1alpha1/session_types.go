package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SessionSpec is one disposable work session: a task dispatched to an
// agent inside a Cell's slot.
type SessionSpec struct {
	// Cell names the Cell CR (same namespace as this Session CR).
	Cell string `json:"cell"`
	// OwnerUserID is the principal that created this Session (ADR-0008). It
	// is set once and never changes — the CRD enforces that, so it holds for
	// kubectl edits too, not just for writes through the API.
	//
	// A Session is a user-private execution and memory boundary: transcript,
	// checkpoint and worktree belong to this user alone. Sessions created
	// before ownership existed have an empty value and are visible only to
	// the static-token principal; guessing an owner that was never recorded
	// would hand one user another user's work.
	OwnerUserID string `json:"ownerUserID,omitempty"`
	// Task is the work order handed to the agent.
	Task string `json:"task"`
	// Runner is the agent CLI: claude | codex | pi.
	Runner string `json:"runner"`
	// Provider is a name from the provider registry (e.g. aliyun-bailian).
	Provider string `json:"provider"`
	// Model optionally pins a model at the provider.
	Model string `json:"model,omitempty"`
	// CredentialSecret references a Secret in the control namespace whose
	// "key" entry is the provider API key. Injected into this session only.
	CredentialSecret string `json:"credentialSecret"`
	// TTLSeconds force-settles a session still running after this long.
	// Defaults to 3600.
	TTLSeconds int64 `json:"ttlSeconds,omitempty"`
	// FollowPreview points the Cell's resident preview at this session's
	// worktree while it runs, so the user watches the work live.
	FollowPreview bool `json:"followPreview,omitempty"`
}

// SessionPhase tracks the dispatch → work → settle → reclaim lifecycle.
type SessionPhase string

const (
	// SessionQueued: all slots busy, waiting for one to free up.
	SessionQueued SessionPhase = "Queued"
	// SessionRunning: pod exists and the agent is working.
	SessionRunning SessionPhase = "Running"
	// SessionSettling: work finished (or was interrupted); settle job runs.
	SessionSettling SessionPhase = "Settling"
	// SessionSettled: produced commits, branch pushed, worktree reclaimed.
	SessionSettled SessionPhase = "Settled"
	// SessionDiscarded: no output; worktree reclaimed.
	SessionDiscarded SessionPhase = "Discarded"
	SessionError     SessionPhase = "Error"
)

// ReviewState is the human verdict on a settled session's branch
// (ADR-0006). Meaningful only when Produced is true.
type ReviewState string

const (
	ReviewPending  ReviewState = "Pending"
	ReviewApproved ReviewState = "Approved"
	ReviewRejected ReviewState = "Rejected"
)

// SessionStatus is the observed state of a Session.
type SessionStatus struct {
	Phase     SessionPhase `json:"phase,omitempty"`
	SessionID string       `json:"sessionID,omitempty"`
	PodName   string       `json:"podName,omitempty"`
	// Branch is the settled output branch (session/<id>) when Produced.
	Branch    string       `json:"branch,omitempty"`
	Produced  bool         `json:"produced,omitempty"`
	StartTime *metav1.Time `json:"startTime,omitempty"`
	Message   string       `json:"message,omitempty"`

	// Review (ADR-0006): the queue is Sessions with Produced && Pending.
	ReviewState ReviewState `json:"reviewState,omitempty"`
	// ReviewNote carries the approval remark or the rejection reason (the
	// latter seeds a follow-up dispatch).
	ReviewNote string `json:"reviewNote,omitempty"`
	// PR tracking, populated after approval opens one.
	PRURL    string `json:"prURL,omitempty"`
	PRNumber int    `json:"prNumber,omitempty"`
	// PRState is the forge's view: open | merged | closed.
	PRState string `json:"prState,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Session is one disposable work session inside a Cell.
type Session struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SessionSpec   `json:"spec,omitempty"`
	Status SessionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SessionList contains a list of Session.
type SessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Session `json:"items"`
}
