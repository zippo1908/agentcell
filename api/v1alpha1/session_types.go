package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SessionSpec is one disposable work session: a task dispatched to an
// agent inside a Cell's slot.
type SessionSpec struct {
	// Cell names the Cell CR (same namespace as this Session CR).
	Cell string `json:"cell"`
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
