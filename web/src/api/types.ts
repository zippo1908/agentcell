// Mirrors celld's JSON surface (internal/webui). Kept hand-written and
// small: the API is ours, and a generated client would be more machinery
// than this earns.

export interface Cell {
  name: string
  /** What people call it. The name above is the address it lives at. */
  displayName?: string
  phase: string
  description: string
  activeSessions: number
  maxSessions: number
  previewPath: string
  productionPath: string
  /** Absolute, ticketed URLs on the untrusted-content origin (ADR-0007). */
  previewURL: string
  productionURL: string
  releaseRef: string
  /** "open" means every authenticated user is a maintainer here. */
  access: string
  members?: { userID: string; role: string }[]
  /** True when productionURL points at a system we do not run: open it,
   * never embed it. */
  productionExternal?: boolean
  handoffMessage?: string
  followSession: string
  message: string
  /** The machine this Cell landed on. A Cell cannot span nodes. */
  node?: string
  /** Placement in force, e.g. "agentcell.io/pool=gpu"; absent = scheduler chooses. */
  pool?: string
  /** Absent on a project created before its repository existed. */
  repoURL?: string
  repoBranch?: string
  /** Which credential this project clones and pushes with; a name only. */
  repoSecretName?: string
  /** Why it has landed nowhere, in the scheduler's own words. */
  schedulingMessage?: string
  /** The Team whose members carry a role into this Cell. */
  team?: string
}

/** One branch in a project's repository, as the checkout itself reports it. */
export interface Branch {
  name: string
  /** Commits this branch has that the base does not, and vice versa. */
  ahead: number
  behind: number
  when: string
  subject: string
  base?: boolean
  session?: string
  /** Nothing of its own the base lacks — safe to delete. */
  merged?: boolean
  /** Which repository, for a project made of several. */
  repo?: string
}

/** One post on a team board. */
export interface Post {
  id: number
  kind: 'user' | 'agent' | 'system'
  author: string
  body: string
  cell?: string
  session?: string
  at: string
  mentions?: string[]
  /** True when the reader wrote it. */
  mine?: boolean
}

/** A membership list that outlives any one project. */
export interface Team {
  name: string
  displayName: string
  description: string
  members?: { userID: string; role: string }[]
  /** Which projects this team governs — the blast radius of a membership change. */
  cells?: string[]
  /** The caller's own role in this team. */
  role: string
}

/** A class of machine a Cell can be placed on, as it exists in the cluster. */
export interface NodePool {
  key: string
  value: string
  label: string
  nodes: number
  taints: string[]
  /** Largest SINGLE node's free capacity — a Cell fits on one machine or none. */
  freeCPU: string
  freeMemory: string
  schedulable: boolean
}

export type SessionPhase =
  | 'Queued'
  | 'Running'
  /** Asleep: gave back its slot and runtime, kept its worktree and its
      conversation. Opening the terminal or a follow-up wakes it. */
  | 'Dormant'
  | 'Settling'
  | 'Settled'
  | 'Discarded'
  | 'Error'
  | ''

export interface Session {
  name: string
  task: string
  runner: string
  provider: string
  phase: SessionPhase
  branch: string
  produced: boolean
  message: string
  started: string
}

export interface CellDetail {
  cell: Cell
  sessions: Session[]
}

export type ReviewState = 'Pending' | 'Approved' | 'Rejected'

export interface Review {
  session: string
  cell: string
  task: string
  branch: string
  state: ReviewState
  note: string
  prURL: string
  prNumber: number
  prState: string
  settled: string
}

export interface DiffFile {
  filename: string
  status: string
  additions: number
  deletions: number
  patch?: string
}

export interface Diff {
  files?: DiffFile[]
  additions?: number
  deletions?: number
}

/** Everything the create-a-project form offers, so it can present choices
 *  instead of asking somebody to retype what the platform already knows. */
export interface NewProjectOptions {
  devboxes: { name: string; displayName: string; image: string; description: string; size: string }[]
  gitCredentials: string[]
  /** Empty or single on a one-node cluster; the form then omits the control. */
  placementClasses: {
    name: string; displayName: string; description: string
    selector: string; nodes: number; free?: string; tolerated?: boolean
  }[]
  runners: RunnerInfo[]
  providers: ProviderInfo[]
  /** What a new project starts with, chosen by the operator. */
  defaultRunner?: string
  defaultProvider?: string
}

export interface RunnerInfo {
  name: string
  display: string
  vendor?: string
  protocols: string[]
  /** False means a follow-up starts a new conversation, not a continuation. */
  resumable: boolean
  /** Providers this runner can actually drive — computed server-side. */
  providers: string[]
  defaultProvider?: string
}

export interface ProviderInfo {
  name: string
  display: string
  vendor?: string
  region?: string
  protocols: string[]
  /** A starting list, never a closed set: providers ship models faster than
   * this table is updated, so the form must accept one that is not here. */
  models?: string[]
  docs?: string
}

export interface Meta {
  runners: RunnerInfo[]
  providers: ProviderInfo[]
  previewOrigin: string
}

export interface DispatchInput {
  task: string
  runner: string
  provider: string
  model: string
  credentialSecret: string
  followPreview: boolean
  /** Keep the slot alive after the agent finishes, in the owner's tmux. */
  resident?: boolean
  /** For a resident session this is IDLE time, not total age. */
  ttlSeconds?: number
  /** Idle MINUTES before a resident session sleeps — a different clock from
      ttlSeconds, which is how long a sleeping one is kept before publishing. */
  idleSeconds?: number
}

/** Live state of a resident session — answered by asking its tmux window,
 * not by looking at the pod (a runtime can be up while the window is gone). */
export interface SessionState {
  resident: boolean
  live: boolean
  working: boolean
  exitCode?: string
  attach: string
  /** Asleep: no runtime, no window. Opening the terminal wakes it. */
  dormant?: boolean
  phase?: string
  /** The control plane's own words — why waking has not finished. */
  message?: string
}

/** The principal this console is acting as. `shared` means a static token:
 * everyone is the same subject, so nothing is private from anyone else
 * holding it. */
export interface Me {
  subject: string
  name: string
  email?: string
  kind: string
  shared: boolean
  /** May invite people and act where the product says an administrator may. */
  admin?: boolean
}

/** A model key you own. The key itself is never returned — `hint` is its
 * last four characters, enough to tell two keys apart. */
export interface Credential {
  name: string
  owner: string
  hint: string
  created: string
}
