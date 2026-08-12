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
  releaseRef: string
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

export interface Meta {
  runners: string[]
  providers: string[]
  /** Absolute origin serving untrusted preview/app content (ADR-0007). */
  previewOrigin: string
}

export interface DispatchInput {
  task: string
  runner: string
  provider: string
  model: string
  credentialSecret: string
  followPreview: boolean
}
