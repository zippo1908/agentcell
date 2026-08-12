/** Formatting shared across the console. */

/** The universal empty marker: an em dash, never a blank cell. */
export const NONE = '—'

/** Relative under a day, absolute beyond it — the point after which "22
 * 小时前" stops being more useful than a date. */
export function ago(iso?: string): string {
  if (!iso) return NONE
  const t = new Date(iso.replace(' ', 'T'))
  if (isNaN(t.getTime())) return iso
  const s = Math.floor((Date.now() - t.getTime()) / 1000)
  if (s < 60) return `${Math.max(s, 0)} 秒前`
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`
  if (s < 86400) return `${Math.floor(s / 3600)} 小时前`
  const p = (n: number) => String(n).padStart(2, '0')
  return `${t.getFullYear()}-${p(t.getMonth() + 1)}-${p(t.getDate())} ${p(t.getHours())}:${p(t.getMinutes())}`
}

/** Identifiers are shown short and mono; the full value belongs in a
 * details block, not in a table cell. */
export function shortID(id: string, n = 8): string {
  if (!id) return NONE
  const bare = id.replace(/^sess-/, '')
  return bare.length > n ? bare.slice(0, n) : bare
}

import type { Tone } from '../ui/primitives'

/** Session phase → tone. Amber is in-flight and pulses, which is what makes
 * a working session findable in a long list. */
export function sessionTone(phase: string): Tone {
  switch (phase) {
    case 'Settled':
      return 'green'
    case 'Error':
      return 'red'
    case 'Running':
    case 'Settling':
    case 'Queued':
      return 'amber'
    default:
      return 'gray'
  }
}

export function cellTone(phase: string): Tone {
  switch (phase) {
    case 'Ready':
      return 'green'
    case 'Error':
      return 'red'
    case '':
      return 'gray'
    default:
      return 'amber'
  }
}

export function reviewTone(state: string): Tone {
  switch (state) {
    case 'Approved':
      return 'green'
    case 'Rejected':
      return 'gray'
    case 'Pending':
      return 'amber'
    default:
      return 'gray'
  }
}
