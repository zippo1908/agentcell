import type {
  Cell,
  CellDetail,
  Diff,
  DispatchInput,
  Credential,
  Me,
  Meta,
  PlacementClass,
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

  /** Go to my terminal in this project — no task needed. */
  openCell: (cell: string) =>
    req<{ session: string; continued?: boolean }>(`/api/cells/${cell}/open`, { method: 'POST' }),

  branches: (cell: string) => req<Branch[]>(`/api/cells/${cell}/branches`),

  newProjectOptions: () => req<NewProjectOptions>('/api/new-project-options'),

  /** Start the Kimi device-code flow; returns the URL and code to approve. */
  kimiLoginStart: () =>
    req<{ url?: string; code?: string; status: string; message?: string }>(
      '/api/kimi/login', { method: 'POST' }),
  kimiLoginPoll: () =>
    req<{ url?: string; code?: string; status: string; message?: string }>('/api/kimi/login'),
  sleepSession: (name: string) =>
    req<{ ok: string; message?: string }>(`/api/sessions/${name}/sleep`, { method: 'POST' }),
  restartRuntime: (name: string) =>
    req<{ ok: string; message?: string }>(`/api/sessions/${name}/restart`, { method: 'POST' }),

  kimiDisconnect: () =>
    req<{ status: string; message?: string }>('/api/kimi/login', { method: 'DELETE' }),

  // The board belongs to a PROJECT: whoever may work on it is whoever may
  // see the conversation about it. There is no team layer.
  board: (cell: string) =>
    req<{ posts: Post[]; latest: number }>(`/api/cells/${cell}/board`),

  // Mentioning the robot returns an ask id alongside the post id; the
  // answer itself streams from GET .../board/ask/{ask} as SSE.
  postToBoard: (cell: string, body: string) =>
    req<{ id: number; ask?: string }>(`/api/cells/${cell}/board`, {
      method: 'POST',
      body: JSON.stringify({ body }),
    }),

  people: () =>
    req<{ email: string; name?: string; admin?: boolean; disabled?: boolean; canCreate?: boolean }[]>(
      '/api/people'),
  createInvite: (email: string, name: string, admin: boolean, canCreate: boolean) =>
    req<{ invite: string; path: string; expires: string }>('/api/invites', {
      method: 'POST',
      body: JSON.stringify({ email, name, admin, canCreate }),
    }),

  /** Attach a repository to a project created before it had one. */
  attachRepo: (cell: string, url: string, branch: string, secretName: string) =>
    req<{ repo: unknown }>(`/api/cells/${cell}/repo`, {
      method: 'PUT',
      body: JSON.stringify({ url, branch, secretName }),
    }),

  /** Keys I have lent out, and keys lent to me. */
  grants: () =>
    req<{
      lent: { credential: string; email: string; name?: string; unknown?: boolean }[]
      borrowed: { credential: string; from: string; hint?: string }[]
      lendable: { name: string; kind: string; hint?: string }[]
    }>('/api/me/grants'),
  lendCredential: (credential: string, email: string, acknowledge = false) =>
    req<{ credential: string; kind?: string }>('/api/me/grants', {
      method: 'POST',
      body: JSON.stringify({ credential, email, acknowledge }),
    }),
  revokeGrant: (credential: string, who: string) =>
    req<{ revoked: string }>(
      `/api/me/grants/${encodeURIComponent(credential)}/${encodeURIComponent(who)}`,
      { method: 'DELETE' }),

  /** My own forge tokens. Listing never returns the tokens themselves. */
  gitIdentities: () =>
    req<{ identities: { provider: string; username: string; secretName: string }[] }>(
      '/api/me/git-identities'),
  bindGitIdentity: (provider: string, username: string, token: string) =>
    req<{ provider: string; username: string; secretName: string }>('/api/me/git-identities', {
      method: 'PUT',
      body: JSON.stringify({ provider, username, token }),
    }),
  unbindGitIdentity: (provider: string) =>
    req<{ deleted: string }>(`/api/me/git-identities/${provider}`, { method: 'DELETE' }),

  placementClasses: () => req<PlacementClass[]>('/api/placementclasses'),

  /** Empty class clears the placement and lets the scheduler choose again. */
  savePlacement: (name: string, className: string) =>
    req<{ ok: string }>(
      `/api/cells/${name}/placement`,
      { method: 'PUT', body: JSON.stringify({ class: className }) },
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

  /** The project's knowledge base: what people put there for the agent. */
  // A bare array, which is what the handler writes. Declaring it wrapped in
  // an object made every library look empty: the request succeeded, the
  // response was right, and `data.files` was undefined.
  files: (cell: string) =>
    req<{ path: string; size: number; mime: string; readable: boolean; uploadedBy?: string; created: number }[]>(
      `/api/cells/${cell}/files`),
  uploadFile: async (cell: string, file: File) => {
    const form = new FormData()
    form.append('file', file)
    // No Content-Type header: the browser has to set the multipart boundary,
    // and setting it by hand produces a body the server cannot parse.
    const res = await fetch(`/api/cells/${cell}/files`, { method: 'POST', body: form })
    if (!res.ok) throw new Error((await res.text()) || `上传失败 (${res.status})`)
    return res.json() as Promise<{ path: string }>
  },
  deleteFile: (cell: string, path: string) =>
    req<{ ok: string }>(`/api/cells/${cell}/files/${path.split('/').map(encodeURIComponent).join('/')}`, {
      method: 'DELETE',
    }),
  fileURL: (cell: string, path: string) =>
    `/api/cells/${cell}/files/${path.split('/').map(encodeURIComponent).join('/')}`,

  /** Which credential this project uses for its repository. */
  setRepoCredential: (cell: string, secretName: string) =>
    req<{ secretName: string }>(`/api/cells/${cell}/repo-credential`, {
      method: 'PUT',
      body: JSON.stringify({ secretName }),
    }),
  /** Or type one in on the project's own page, for somebody who has none. */
  setRepoToken: (cell: string, username: string, token: string) =>
    req<{ secretName: string }>(`/api/cells/${cell}/repo-credential`, {
      method: 'PUT',
      body: JSON.stringify({ username, token }),
    }),

  /** Who is on this project — names, not the hashes the CR stores. */
  members: (cell: string) =>
    req<{ members: { email: string; name?: string; role: string; unknown?: boolean }[]; open: boolean }>(
      `/api/cells/${cell}/members`),
  // An address goes in `email`, which the server resolves to an id. Sending
  // it as `userID` stored the address verbatim, and the authorization check
  // compares against a hashed id — so the member list looked right and
  // granted nothing.
  putMember: (cell: string, who: string, role: string) =>
    req<{ access: string }>(`/api/cells/${cell}/members`, {
      method: 'PUT',
      body: JSON.stringify(who.includes('@') ? { email: who, role } : { userID: who, role }),
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
