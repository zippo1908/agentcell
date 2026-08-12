import type {
  Cell,
  CellDetail,
  Diff,
  DispatchInput,
  Meta,
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
  cells: () => req<Cell[]>('/api/cells'),
  cell: (name: string) => req<CellDetail>(`/api/cells/${name}`),

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

  review: (session: string, decision: 'approve' | 'reject', note: string) =>
    req<Review>(`/api/sessions/${session}/review`, {
      method: 'POST',
      body: JSON.stringify({ decision, note }),
    }),
}
