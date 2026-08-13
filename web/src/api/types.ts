// Mirrors celld's JSON surface (internal/webui). Kept hand-written and
// small: the API is ours, and a generated client would be more machinery
// than this earns.

export interface Cell {
  name: string
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
  /** True when productionURL points at a system we do not run: open it,
   * never embed it. */
  productionExternal?: boolean
  handoffMessage?: string
  followSession: string
  message: string
}

export type SessionPhase =
  | 'Queued'
  | 'Running'
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
}

/** Live state of a resident session — answered by asking its tmux window,
 * not by looking at the pod (a runtime can be up while the window is gone). */
export interface SessionState {
  resident: boolean
  live: boolean
  working: boolean
  exitCode?: string
  attach: string
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
}
