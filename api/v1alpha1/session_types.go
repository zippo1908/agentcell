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
	// The rule lives here as a marker rather than in hand-edited YAML: it is
	// the property ADR-0008 rests on, and a hand-maintained CRD is exactly
	// where it would be lost the next time the file was regenerated.
	//
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="oldSelf == '' || self == oldSelf",message="ownerUserID is immutable once set"
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
	// TTLSeconds force-settles a session after this long. For a one-shot
	// session it is measured from the start (default 3600); for a resident
	// one it bounds how long a DORMANT session is kept before its work is
	// published and its worktree reclaimed (default 604800 — a week), since
	// a dormant session costs storage but no compute.
	TTLSeconds int64 `json:"ttlSeconds,omitempty"`
	// IdleSeconds is how long a resident session may sit with no agent
	// working and nobody watching before it goes dormant. Defaults to 900.
	//
	// Short on purpose: this is not a deadline, it is a yawn. Nothing is
	// lost — the terminal comes back where it was — so the cost of being
	// wrong is a few seconds of waking, while the cost of never sleeping is
	// a project's slots held by work that finished hours ago.
	IdleSeconds int64 `json:"idleSeconds,omitempty"`
	// DesiredState is what this session is supposed to be doing: "running"
	// or "dormant". Both the control plane and a person write it — the
	// reconciler sets "dormant" when nobody is using the session, and the
	// console sets "running" the moment somebody opens its terminal or sends
	// it a follow-up.
	//
	// A desired state rather than a timer, because "should this be awake" is
	// a question with an answer, and burying that answer in elapsed
	// milliseconds makes it unaskable. `kubectl get session` says which
	// sessions are meant to be up; the phase says which ones are.
	// +kubebuilder:validation:Enum=running;dormant
	// +kubebuilder:default=running
	DesiredState string `json:"desiredState,omitempty"`
	// Resident keeps the slot alive after the agent finishes: the work runs
	// inside a tmux server on the owner's private socket, so they can attach,
	// look at what happened and keep going in the same context instead of
	// dispatching a fresh session that has to rediscover everything.
	//
	// ON by default. A one-shot agent prints nothing until it is finished,
	// so from outside there is no difference between working and hung — and
	// that turned out to be the thing people actually could not live with.
	// Resident work runs in a terminal somebody can open, read, and type
	// into. A resident session still settles — on idle, on request, or if its
	// pod disappears — so nothing escapes the publication gate; what changes
	// is that the run is visible while it happens.
	//
	// A pointer so that "unset" is distinguishable from "explicitly off":
	// with a plain bool, every client that forgot the field would silently
	// mean false, which is exactly the wrong default to have inherited.
	// +kubebuilder:default=true
	Resident *bool `json:"resident,omitempty"`
	// PendingTask is a follow-up waiting to be typed into this session's
	// terminal.
	//
	// It exists because a session may be ASLEEP when the next instruction
	// arrives, and the honest options then are to lose it, to block the
	// caller while a pod is scheduled, or to write it down and deliver it on
	// waking. Only the third is both quick and lossless.
	PendingTask string `json:"pendingTask,omitempty"`
	// Board names the team board that asked for this work, so the agent can
	// answer where the question was asked instead of somewhere else.
	// +kubebuilder:validation:MaxLength=63
	Board string `json:"board,omitempty"`
	// FollowPreview points the Cell's resident preview at this session's
	// worktree while it runs, so the user watches the work live.
	FollowPreview bool `json:"followPreview,omitempty"`
}

// SessionPhase tracks the dispatch → work → settle → reclaim lifecycle.
type SessionPhase string

const (
	// Desired states. A session is meant to be awake or asleep; the phase
	// says which it currently is.
	SessionDesiredRunning = "running"
	SessionDesiredDormant = "dormant"
)

const (
	// SessionQueued: all slots busy, waiting for one to free up.
	SessionQueued SessionPhase = "Queued"
	// SessionRunning: pod exists and the agent is working.
	SessionRunning SessionPhase = "Running"
	// SessionDormant: nobody is using this session, so it holds no compute.
	//
	// Not an ending. The worktree and the CLI's own conversation live on the
	// volume, so waking restores the terminal where it was — what was given
	// up is the runtime process and the slot, which is the expensive part.
	// Before this existed, an idle session was force-settled: five minutes of
	// work cost a slot for hours and then had its session ended for it.
	SessionDormant SessionPhase = "Dormant"
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
	// RunnerSessionID names the conversation inside the agent CLI, where that
	// CLI lets the caller choose one. It is what makes "keep going" continue
	// the same conversation instead of starting one that has to rediscover
	// everything. The CLIs already do this well; the platform's job is to
	// name the conversation and give it a private $HOME (ADR-0009), not to
	// reimplement transcripts.
	RunnerSessionID string `json:"runnerSessionID,omitempty"`
	// LastActivity is when something last happened in this session: a window
	// opened, a follow-up typed, or the agent observed working. A resident
	// session's TTL is measured from here rather than from its start, so the
	// slot reclaimed is an idle one and never one in use.
	LastActivity *metav1.Time `json:"lastActivity,omitempty"`
	// RuntimeInstance identifies the exact container this session's window
	// lives in. A tmux server dies with its container, so a restart takes
	// every window with it — which looks identical to the owner closing one.
	// Recording the instance is what tells those two apart: same container
	// and no window means someone closed it; a different container means the
	// runtime was replaced and the session should be handed back, not
	// settled out from under its owner.
	RuntimeInstance string `json:"runtimeInstance,omitempty"`
	// BoardNotified records that the board this work came from has already
	// been told the agent finished. Without it the reconciler would say so
	// again on every poll — a chat that repeats itself every ten seconds.
	BoardNotified bool `json:"boardNotified,omitempty"`
	// DormantSince is when this session stopped holding compute. The TTL
	// that eventually publishes its work is measured from here.
	DormantSince *metav1.Time `json:"dormantSince,omitempty"`
	// Recoveries counts how often this session's runtime had to be rebuilt
	// under it. Bounded: a session that cannot stay up is settled rather than
	// rebuilt forever, so a failing node does not become an infinite loop.
	Recoveries int `json:"recoveries,omitempty"`
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

// IsResident reports whether this session runs in a terminal somebody can
// open. Unset means yes — see the field's comment; the pointer exists so a
// client that omits it gets the default rather than silently getting false.
func (s *Session) IsResident() bool {
	return s.Spec.Resident == nil || *s.Spec.Resident
}
