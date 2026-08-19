package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RestartRequestedAnnotation marks a runtime teardown that a PERSON asked
// for, so the controller rebuilds without counting it as a crash. Recovery
// is budgeted (a runtime that dies over and over must eventually settle
// rather than flap forever), and a deliberate restart drawing on that same
// budget would turn the third press of a button into "your session is
// over".
const RestartRequestedAnnotation = "agentcell.io/restart-requested"

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
	// This is the person the session's model spend is charged to. Always a
	// real user — never a synthetic principal standing in for a group.
	//
	// Written once, at creation, and never changed. An operator typing into
	// a shared session must not quietly become the one paying for it, and
	// the credential must not be swapped out from under whoever lent it.
	//
	// Who may OPERATE the session is a different question, answered by the
	// project's member list (see Board) rather than stored here: a second
	// copy of membership is a second thing that can disagree with the first.
	//
	// Sessions created before ownership existed have an empty value and are
	// visible only to the static-token principal; guessing an owner that was
	// never recorded would hand one user another user's work.
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
	// PendingTasks are follow-ups waiting to be typed into this session's
	// terminal, in the order they were said.
	//
	// A queue, not a slot. It exists because a session may be ASLEEP when
	// the next instruction arrives, and the honest options then are to lose
	// it, to block the caller while a pod is scheduled, or to write it down
	// and deliver it on waking — only the third is both quick and lossless.
	//
	// It was a single string, which lost work in the most ordinary case
	// there is: say two things in a row, and the second overwrote the first
	// before anything delivered it. Nobody was told; the first instruction
	// simply never happened.
	PendingTasks []string `json:"pendingTasks,omitempty"`

	// PendingTask is the LEGACY single-slot form, drained into the queue and
	// cleared on first sight. Kept only so a session written before the
	// change does not lose the instruction it was holding.
	//
	// +optional
	PendingTask string `json:"pendingTask,omitempty"`
	// Board names the project board this work was asked for on, and marks
	// the session as SHARED: everyone who may dispatch in the project can
	// drive it, because the conversation is the project's rather than one
	// person's.
	//
	// It does not change who PAYS. OwnerUserID stays whoever first asked,
	// and their credential funds every turn — including turns a colleague
	// types. Sharing the keyboard and sharing the bill are different
	// decisions, and only one of them was made here.
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
	Phase SessionPhase `json:"phase,omitempty"`
	// LibraryVersion is the project's library as this session last received
	// it. The library used to arrive only in the pod's environment, which is
	// fixed at creation — so a file uploaded while somebody was working
	// could not reach them until the session restarted. Comparing this with
	// the Cell's own marker is what lets a live session be topped up.
	LibraryVersion string `json:"libraryVersion,omitempty"`
	SessionID      string `json:"sessionID,omitempty"`
	PodName        string `json:"podName,omitempty"`
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
	// Outputs is what this session produced, one entry per repository.
	//
	// Separate entries rather than one verdict, because the repositories are
	// separate on the forge: they have their own remotes, their own history
	// and their own reviewers. A session that changes a frontend and a
	// backend produces two branches, and somebody may reasonably take one
	// and not the other. Presenting them as a single all-or-nothing decision
	// would be inventing an atomicity that does not exist anywhere below.
	//
	// Empty on a single-repo project, where Branch/Produced/ReviewState
	// below say the same thing and every existing Session keeps working.
	Outputs []RepoOutput `json:"outputs,omitempty"`
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

// RepoOutput is one repository's share of what a session produced, with its
// own review verdict.
type RepoOutput struct {
	// Repo is the repository's name within the project.
	Repo string `json:"repo"`
	// Branch is session/<id> in that repository.
	Branch string `json:"branch,omitempty"`
	// Produced is false when the agent changed nothing here — common, and
	// not a failure: a task usually touches some of a project, not all.
	Produced bool   `json:"produced,omitempty"`
	Message  string `json:"message,omitempty"`
	// Review is this repository's own verdict. Approving one says nothing
	// about the others.
	Review ReviewState `json:"review,omitempty"`
	Note   string      `json:"note,omitempty"`
	// PR fields track the pull request opened for THIS repository.
	PRURL    string `json:"prURL,omitempty"`
	PRNumber int    `json:"prNumber,omitempty"`
	PRState  string `json:"prState,omitempty"`
}
