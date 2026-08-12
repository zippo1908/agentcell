import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import type { Session } from '../api/types'

const TERMINAL = ['Settled', 'Discarded', 'Error']

export function SessionList({ cell, sessions }: { cell: string; sessions: Session[] }) {
  const qc = useQueryClient()
  const settle = useMutation({
    mutationFn: (name: string) => api.settleSession(name),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['cell', cell] }),
  })

  if (sessions.length === 0) {
    return <div className="empty">还没有会话 — 在上面派第一单。</div>
  }

  return (
    <>
      {sessions.map((s) => (
        <div className="item" key={s.name}>
          <div className="title" title={s.task}>
            {s.task}
          </div>
          <div className="sub">
            <span className={`chip ${s.phase}`}>{s.phase || '…'}</span>
            <span>
              {s.runner}·{s.provider}
            </span>
            {s.branch && <code>{s.branch}</code>}
            {s.started && <span>{s.started}</span>}
            {s.message && <span title={s.message}>ℹ</span>}
            {!TERMINAL.includes(s.phase) && (
              <button
                className="ghost"
                onClick={() => settle.mutate(s.name)}
                disabled={settle.isPending}
                title="停止并清算这个会话"
              >
                结算
              </button>
            )}
          </div>
        </div>
      ))}
    </>
  )
}
