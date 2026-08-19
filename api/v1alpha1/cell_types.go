package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RepoSpec points a Cell at one git repository.
type RepoSpec struct {
	// Name identifies this repository inside the project — the label a
	// person uses ("frontend") and the key a branch result is reported
	// against. Empty on the single-repo form, where there is nothing to
	// disambiguate.
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name,omitempty"`
	// Path is where this repository sits under /workspace, relative and
	// without "..". Empty means the workspace root.
	//
	// Data rather than a rule in code, because the layout is the project's
	// business: an existing single-repo project keeps its repo at the root
	// and nothing about it moves, while a project group gives every
	// repository a directory of its own.
	// +kubebuilder:validation:MaxLength=200
	Path string `json:"path,omitempty"`
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
	// Mode says whether a preview should run at all.
	//
	// Empty and "auto" both mean the platform reads the checkout and works
	// the command out (see cmd/cell-runtime/preview_detect.go). That is the
	// default because a preview is the thing people actually want and the
	// answer is already written in the repository — asking for it on a
	// create form, before the repository has anything in it, produced
	// projects with no preview and nothing saying why.
	//
	// "off" means never, for a project that genuinely has nothing to serve.
	// +kubebuilder:validation:Enum=auto;off
	Mode PreviewMode `json:"mode,omitempty"`
	// Command is run (via sh -c when len==1, else exec'd) in the preview
	// target directory by the anchor, restarted with backoff when it exits.
	// Empty in auto mode; set, it always wins over detection.
	Command []string `json:"command,omitempty"`
	// Port the command serves HTTP on inside the anchor pod.
	Port int32 `json:"port,omitempty"`
	// FollowSession switches the preview working directory to that
	// session's worktree so the user watches work-in-progress live; empty
	// means the main checkout.
	FollowSession string `json:"followSession,omitempty"`
}

// LibraryVersionAnnotation marks when a project's files last changed, so a
// running session can be topped up instead of having to be restarted. Any
// value that changes will do; the console writes a timestamp.
const LibraryVersionAnnotation = "agentcell.io/library-version"

// PreviewMode selects between letting the platform decide and switching the
// preview off. There is deliberately no "on": "on" without a command is
// exactly what auto already is.
type PreviewMode string

const (
	// PreviewAuto reads the checkout and decides. The zero value.
	PreviewAuto PreviewMode = "auto"
	// PreviewOff runs no preview for this project.
	PreviewOff PreviewMode = "off"
)

// PreviewEnabled reports whether this Cell wants a preview at all.
func (p PreviewSpec) PreviewEnabled() bool { return p.Mode != PreviewOff }

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
	// +kubebuilder:validation:Enum=incell;external
	//
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
	// Repos are the OTHER repositories this project is made of.
	//
	// One project, one workspace, one agent, several repositories side by
	// side. The alternative — one project per repository — fails for the
	// case it most needs to serve: changing an interface means editing the
	// frontend and the backend together, and an agent that cannot see the
	// other half cannot do it. It also multiplies the cost, since every
	// project carries its own runtime and its own agent process.
	//
	// Each repository keeps its own remote, its own base branch and its own
	// credential, because they are separate repositories on the forge and
	// pretending otherwise would be a fiction. A session that touches three
	// of them produces three branches, reviewed and approved separately —
	// there is no cross-repository atomicity to be had, so the platform does
	// not imply one.
	Repos []RepoSpec `json:"repos,omitempty"`
	// Image is the devbox image for the anchor and session pods; it must
	// contain the agent CLIs plus git and tmux. cell-runtime is baked in at
	// image build time.
	Image string `json:"image"`
	// Description is the living product description the user calibrates
	// while watching the preview; dispatches default to carrying it.
	Description string `json:"description,omitempty"`
	// +kubebuilder:validation:Enum=open;restricted
	//
	// Access decides whether Members are enforced.
	//
	// Empty is treated as "open" so an upgrade does not lock anyone out of
	// their own project — but an empty array is a bad way to express a
	// dangerous state, so the controller RECORDS the conclusion in
	// status.access. A Cell that is open to everyone should say so, not be
	// inferred from a missing field (ADR-0013).
	Access AccessMode `json:"access,omitempty"`
	// Members grants roles on this Cell. Ignored while Access is open.
	Members []Member `json:"members,omitempty"`
	// MaxSessions is this project's concurrency budget: how many sessions
	// may hold a slot here at once. Defaults to 2.
	//
	// It counts SESSIONS, not people. It used to be described as a headcount
	// — "a slot is a person" — which was true only as long as a separate
	// routing rule gave each person exactly one live session per project.
	// That rule is a routing choice and may change; the quota is a resource
	// limit and must not be defined by it. Reading the field as a headcount
	// also made shared sessions unaccountable: one board conversation is one
	// slot whether two people type into it or ten.
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
	// Placement says which machines this Cell may run on. A Cell cannot span
	// nodes — its workspace volume is ReadWriteOnce and every pod follows the
	// anchor — so this is the whole of "where does this project live".
	//
	// It exists because a real deployment is not uniform: hyperconverged
	// hosts next to cloud instances, a big-memory box, a GPU box, a cheap
	// box. Without a way to say so, a project either takes whatever the
	// scheduler picks or the whole cluster has to be identical.
	Placement PlacementSpec `json:"placement,omitempty"`
	// Defaults is the runner/provider/model pairing this project works with.
	//
	// Chosen once, when the project is created, because it is a property of
	// the project — this codebase, this team's account — not of each task.
	// The dispatch form prefills from it, and the board uses it instead of
	// inferring a pairing from whatever was dispatched last, which was a
	// guess dressed up as a memory.
	Defaults RunDefaults `json:"defaults,omitempty"`
	// Database is where this project's data lives. AgentCell does not run
	// it: a database is not a thing to lock to an application server, and a
	// platform that provisions one quietly becomes responsible for its
	// backups, its upgrades and its outages. What the platform does is
	// deliver the connection to the workloads that need it.
	Database DatabaseSpec `json:"database,omitempty"`
	// Team is LEGACY and ignored.
	//
	// Kept as a field so a Cell written before teams were removed still
	// round-trips through the API instead of being rejected, and so the
	// controller can see it in order to clear it and say so. Nothing reads
	// it for a decision: membership is this project's own member list.
	//
	// +optional
	Team string `json:"team,omitempty"`
}

// RunDefaults is what this project runs with unless a task says otherwise.
type RunDefaults struct {
	// +kubebuilder:validation:MaxLength=63
	Runner string `json:"runner,omitempty"`
	// +kubebuilder:validation:MaxLength=63
	Provider string `json:"provider,omitempty"`
	// +kubebuilder:validation:MaxLength=200
	Model string `json:"model,omitempty"`
}

// DatabaseSpec points a Cell's zones at their databases.
//
// Two entries, not one, and that is the whole point of the type. A preview
// runs code an agent has just written, against data it may have just decided
// to migrate; pointing it at the same database production uses is the
// ordinary way a company loses a table. So the dev zone and the production
// zone name SEPARATE secrets, and leaving production unset means production
// simply gets no database — never the dev one by default.
type DatabaseSpec struct {
	// DevSecretName is a Secret in the control namespace whose keys become
	// environment variables in the preview. Keys rather than one fixed
	// variable, because "what a connection looks like" is the framework's
	// business, not ours: DATABASE_URL, PGHOST+PGUSER, JDBC — all just keys.
	DevSecretName string `json:"devSecretName,omitempty"`
	// ProdSecretName is the same for the production zone. Unset means
	// production has no database, which is a state to notice rather than a
	// reason to fall back to the dev one.
	ProdSecretName string `json:"prodSecretName,omitempty"`
}

// PlacementSpec pins a Cell to a class of machine.
type PlacementSpec struct {
	// Class names a PlacementClass an administrator has offered. This is the
	// ONLY placement the API will set on a maintainer's behalf: the raw
	// fields below stay writable through kubectl, which already means
	// cluster access, and are ignored when a class is named.
	// +kubebuilder:validation:MaxLength=63
	Class string `json:"class,omitempty"`
	// NodeSelector is matched against node labels. It must match at least
	// one node at the time it is set: a selector matching nothing schedules
	// nothing, and the symptom — pods Pending forever, with the reason
	// buried in an event — is the kind of failure this field exists to
	// prevent, not cause.
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Tolerations let this Cell onto tainted nodes.
	//
	// Only ever set by somebody editing this object directly — which means
	// cluster access — or copied from a PlacementClass an administrator
	// wrote. The console once derived these from whatever taints the chosen
	// nodes carried, which turned a cluster administrator's refusal into a
	// checkbox for anyone who maintained a project. It does not any more.
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
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
	// Node is the machine this Cell actually landed on. A Cell is one node's
	// worth of project, so "which one" is a fact its owner needs and cannot
	// otherwise get without cluster access.
	Node string `json:"node,omitempty"`
	// SchedulingMessage is why it has not landed anywhere, in the
	// scheduler's own words. A Cell that stays Pending is the most opaque
	// failure this system has — the reason lives in an event on a pod in
	// another namespace — so it is carried up to where the question is
	// asked.
	SchedulingMessage string `json:"schedulingMessage,omitempty"`
	// Access is the mode actually in force, so "who can touch this project"
	// is answerable from `kubectl get cell` without reasoning about whether
	// an empty list means everyone or no one.
	Access AccessMode `json:"access,omitempty"`
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

// AccessMode says whether a Cell's member list governs it.
type AccessMode string

const (
	// AccessOpen: every authenticated user is a maintainer. This is what
	// every Cell did before roles existed, and it is what an unset Cell with
	// no members still does — stated rather than inferred.
	AccessOpen AccessMode = "open"
	// AccessRestricted: only Members have any role at all.
	AccessRestricted AccessMode = "restricted"
)

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
	// +kubebuilder:validation:MaxLength=63
	UserID string `json:"userID"`
	// +kubebuilder:validation:Enum=viewer;member;maintainer
	Role Role `json:"role"`
}

// EffectiveAccess is whether this Cell's member list is enforced.
//
// It lives on the type because two places need the answer — the controller,
// which records it in status so `kubectl get cell` can be trusted, and the
// authorization check, which enforces it. Two copies of this rule is how a
// Cell ends up reporting "open" while refusing everybody, or the reverse.
//
// An empty member list means OPEN, whatever else the spec still carries.
//
// This used to also treat a named team as "an inside". Teams are gone, and
// keeping that clause would have locked people out of their own projects on
// upgrade: a Cell created before the change has spec.team set and no
// members, so it would report restricted with a member list that names
// nobody — closed to everybody, administrators included, and recoverable
// only by editing the CR. A rule that turns an upgrade into a lockout is
// worse than a rule that is briefly too generous.
func (c *Cell) EffectiveAccess() AccessMode {
	if c.Spec.Access != "" {
		return c.Spec.Access
	}
	if len(c.Spec.Members) == 0 {
		return AccessOpen
	}
	return AccessRestricted
}

// AllRepos is every repository in this project, normalised.
//
// The single-repo form is one entry at the workspace root, which is what
// every existing Cell already is: no migration, no path changes, and an
// agent's existing context stays valid.
func (c *Cell) AllRepos() []RepoSpec {
	out := make([]RepoSpec, 0, 1+len(c.Spec.Repos))
	if c.Spec.Repo.URL != "" {
		r := c.Spec.Repo
		if r.Name == "" {
			r.Name = "main"
		}
		out = append(out, r)
	}
	out = append(out, c.Spec.Repos...)
	for i := range out {
		if out[i].Branch == "" {
			out[i].Branch = "main"
		}
		if out[i].Name == "" {
			out[i].Name = out[i].Path
		}
	}
	return out
}

// PrimaryRepo is the one at the workspace root, or the first declared. It is
// what a question about "the project's repository" means when nobody said
// which.
func (c *Cell) PrimaryRepo() RepoSpec {
	all := c.AllRepos()
	for _, r := range all {
		if r.Path == "" {
			return r
		}
	}
	if len(all) > 0 {
		return all[0]
	}
	return RepoSpec{}
}
