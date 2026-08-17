import type {
  Cell,
  CellDetail,
  Diff,
  DispatchInput,
  Credential,
  Me,
  Meta,
  NodePool,
  Team,
  NewProjectOptions,
  Post,
  Branch,
  SessionState,
  Review,
} from './types'

/** Thrown for non-2xx responses; `status` lets callers special-case 401. */
export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message)
  }
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: {
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
  })
  if (res.status === 401) {
    // The session cookie expired or was never set: celld serves the login
    // form at /login.
    window.location.href = '/login'
    throw new ApiError('unauthorized', 401)
  }
  const text = await res.text()
  const body = text ? JSON.parse(text) : {}
  if (!res.ok) {
    throw new ApiError(body?.error ?? `${res.status} ${res.statusText}`, res.status)
  }
  return body as T
}

export const api = {
  meta: () => req<Meta>('/api/meta'),

  /** Who am I acting as — and is this a shared principal? */
  me: () => req<Me>('/api/me'),

  credentials: () => req<Credential[]>('/api/credentials'),
  putCredential: (name: string, key: string) =>
    req<{ name: string; hint: string }>(`/api/credentials/${encodeURIComponent(name)}`, {
      method: 'PUT',
      body: JSON.stringify({ key }),
    }),
  deleteCredential: (name: string) =>
    req<{ ok: string }>(`/api/credentials/${encodeURIComponent(name)}`, { method: 'DELETE' }),
  cells: () => req<Cell[]>('/api/cells'),

  /** Onboard a project from the console. */
  createCell: (body: Record<string, unknown>) =>
    req<{ cell: string }>('/api/cells', { method: 'POST', body: JSON.stringify(body) }),
  cell: (name: string) => req<CellDetail>(`/api/cells/${name}`),

  /** Just the task: the server uses the project's defaults and your key. */
  dispatchSimple: (cell: string, task: string) =>
    req<{ session: string; continued?: boolean; message?: string }>(
      `/api/cells/${cell}/dispatch`, { method: 'POST', body: JSON.stringify({ task }) }),

  branches: (cell: string) => req<Branch[]>(`/api/cells/${cell}/branches`),

  newProjectOptions: () => req<NewProjectOptions>('/api/new-project-options'),

  /** Start the Kimi device-code flow; returns the URL and code to approve. */
  kimiLoginStart: () =>
    req<{ url?: string; code?: string; status: string; message?: string }>(
      '/api/kimi/login', { method: 'POST' }),
  kimiLoginPoll: () =>
    req<{ url?: string; code?: string; status: string; message?: string }>('/api/kimi/login'),

  teams: () => req<Team[]>('/api/teams'),

  board: (team: string) =>
    req<{ posts: Post[]; latest: number }>(`/api/teams/${team}/board`),

  postToBoard: (team: string, body: string) =>
    req<{ id: number }>(`/api/teams/${team}/board`, {
      method: 'POST',
      body: JSON.stringify({ body }),
    }),

  createTeam: (name: string, displayName: string) =>
    req<Team>('/api/teams', { method: 'POST', body: JSON.stringify({ name, displayName }) }),

  putTeamMember: (team: string, userID: string, role: string) =>
    req<{ members: { userID: string; role: string }[] }>(`/api/teams/${team}/members`, {
      method: 'PUT',
      body: JSON.stringify({ userID, role }),
    }),

  deleteTeamMember: (team: string, userID: string) =>
    req<{ members: { userID: string; role: string }[] }>(
      `/api/teams/${team}/members/${userID}`, { method: 'DELETE' }),

  /** Empty team detaches the Cell and returns it to its own member list. */
  setCellTeam: (name: string, team: string) =>
    req<{ team: string }>(`/api/cells/${name}/team`, {
      method: 'PUT',
      body: JSON.stringify({ team }),
    }),

  nodePools: () => req<NodePool[]>('/api/nodepools'),

  /** Empty key clears the placement and lets the scheduler choose again. */
  savePlacement: (name: string, key: string, value: string) =>
    req<{ nodeSelector: Record<string, string>; tolerations: number }>(
      `/api/cells/${name}/placement`,
      { method: 'PUT', body: JSON.stringify({ key, value }) },
    ),

  saveDescription: (name: string, description: string) =>
    req<{ ok: string }>(`/api/cells/${name}/description`, {
      method: 'PUT',
      body: JSON.stringify({ description }),
    }),

  dispatch: (name: string, input: DispatchInput) =>
    req<{ session: string }>(`/api/cells/${name}/dispatch`, {
      method: 'POST',
      body: JSON.stringify(input),
    }),

  release: (name: string, ref?: string) =>
    req<{ ok: string; releaseID: string }>(`/api/cells/${name}/release`, {
      method: 'POST',
      body: JSON.stringify(ref ? { ref } : {}),
    }),

  settleSession: (session: string) =>
    req<{ ok: string }>(`/api/sessions/${session}`, { method: 'DELETE' }),

  reviews: (cell?: string) =>
    req<Review[]>(`/api/reviews${cell ? `?cell=${encodeURIComponent(cell)}` : ''}`),

  diff: (session: string) => req<Diff>(`/api/sessions/${session}/diff`),

  putMember: (cell: string, userID: string, role: string) =>
    req<{ access: string }>(`/api/cells/${cell}/members`, {
      method: 'PUT',
      body: JSON.stringify({ userID, role }),
    }),
  removeMember: (cell: string, userID: string) =>
    req<{ access: string }>(`/api/cells/${cell}/members/${encodeURIComponent(userID)}`, {
      method: 'DELETE',
    }),

  /** Is a resident session still working, or waiting for you? */
  sessionState: (session: string) => req<SessionState>(`/api/sessions/${session}/state`),

  /** Say one more thing to a live session, in the same conversation. */
  continueSession: (session: string, text: string) =>
    req<{ ok: string }>(`/api/sessions/${session}/continue`, {
      method: 'POST',
      body: JSON.stringify({ text }),
    }),

  review: (session: string, decision: 'approve' | 'reject', note: string) =>
    req<Review>(`/api/sessions/${session}/review`, {
      method: 'POST',
      body: JSON.stringify({ decision, note }),
    }),
}
