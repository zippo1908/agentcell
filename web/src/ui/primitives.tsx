import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'

/** Status tone. Amber means in-flight and is the only one that pulses. */
export type Tone = 'green' | 'red' | 'amber' | 'gray'

/**
 * A status pill: the pill itself stays gray and only a 7px dot carries the
 * colour. Colour is a budget — spent on branding it says nothing, spent only
 * on state a single amber dot in a table of forty rows is alarming.
 */
export function Badge({ tone = 'gray', children }: { tone?: Tone; children: ReactNode }) {
  return (
    <span className={`badge ${tone}`}>
      <span className="dot" />
      {children}
    </span>
  )
}

/** A fact, not a state: square, mono, no dot. */
export function Tag({ children, title }: { children: ReactNode; title?: string }) {
  return (
    <span className="tag" title={title}>
      {children}
    </span>
  )
}

export function Stat({
  num,
  label,
  tone,
}: {
  num: ReactNode
  label: string
  tone?: Tone | 'accent'
}) {
  return (
    <div className={`stat ${tone ?? ''}`}>
      <div className="num">{num}</div>
      <div className="label">{label}</div>
    </div>
  )
}

export function Spinner({ size = 14 }: { size?: number }) {
  return <span className="spinner" style={{ width: size, height: size }} />
}

/** Pseudo-random widths so a loading table does not read as a grid. */
export function SkeletonTable({ rows = 4, cols = 4 }: { rows?: number; cols?: number }) {
  return (
    <table className="data">
      <tbody>
        {Array.from({ length: rows }, (_, r) => (
          <tr key={r}>
            {Array.from({ length: cols }, (_, c) => (
              <td key={c}>
                <div className="skel" style={{ width: `${40 + ((r * 7 + c * 13) % 45)}%` }} />
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  )
}

/**
 * Empty states name the exact fix and link to it. "Nothing here" is a dead
 * end; "create one, and here is the button" is not.
 */
export function EmptyState({
  title,
  hint,
  action,
}: {
  title: string
  hint?: string
  action?: ReactNode
}) {
  return (
    <div className="empty-state">
      <div className="empty-state-title">{title}</div>
      {hint && <div className="empty-state-hint">{hint}</div>}
      {action && <div className="btn-row" style={{ marginTop: 6 }}>{action}</div>}
    </div>
  )
}

export function Defs({ items }: { items: [string, ReactNode][] }) {
  return (
    <dl className="defs">
      {items.map(([k, v]) => (
        <div key={k} style={{ display: 'contents' }}>
          <dt>{k}</dt>
          <dd>{v}</dd>
        </div>
      ))}
    </dl>
  )
}

/* ── Toasts ─────────────────────────────────────────────────────────── */

type Toast = { id: number; kind: 'success' | 'error' | 'info'; text: string }
type ToastApi = {
  success: (t: string) => void
  error: (t: string) => void
  info: (t: string) => void
}
const ToastCtx = createContext<ToastApi>({ success: () => {}, error: () => {}, info: () => {} })
export const useToast = () => useContext(ToastCtx)

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Toast[]>([])
  const push = useCallback((kind: Toast['kind'], text: string) => {
    const id = Date.now() + Math.random()
    setItems((xs) => [...xs, { id, kind, text }])
    // Errors linger: they are the ones you need time to read, and often to
    // copy an error code out of.
    setTimeout(() => setItems((xs) => xs.filter((x) => x.id !== id)), kind === 'error' ? 6000 : 3500)
  }, [])
  const api: ToastApi = {
    success: (t) => push('success', t),
    error: (t) => push('error', t),
    info: (t) => push('info', t),
  }
  return (
    <ToastCtx.Provider value={api}>
      {children}
      <div className="toast-stack" role="status" aria-live="polite">
        {items.map((t) => (
          <div key={t.id} className={`toast toast-${t.kind}`}>
            <span className="glyph">{t.kind === 'success' ? '✓' : t.kind === 'error' ? '✕' : 'ℹ'}</span>
            <span>{t.text}</span>
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  )
}

/* ── Confirm ────────────────────────────────────────────────────────── */

/**
 * A confirmation states the consequence. "Are you sure?" tells the reader
 * nothing they did not already know; naming the object and what will not
 * survive does.
 */
export function Confirm({
  title,
  body,
  confirmText = '确认',
  danger,
  onConfirm,
  onCancel,
}: {
  title: string
  body: ReactNode
  confirmText?: string
  danger?: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onCancel()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onCancel])
  return (
    <div className="modal-mask" onMouseDown={(e) => e.target === e.currentTarget && onCancel()}>
      <div className="modal" role="dialog" aria-modal="true">
        <h2>{title}</h2>
        <div style={{ fontSize: 12.5, color: 'var(--text-dim)', lineHeight: 1.6 }}>{body}</div>
        <div className="modal-actions">
          <button className="small ghost" onClick={onCancel}>
            取消
          </button>
          <button className={`small ${danger ? 'danger' : 'primary'}`} onClick={onConfirm}>
            {confirmText}
          </button>
        </div>
      </div>
    </div>
  )
}
