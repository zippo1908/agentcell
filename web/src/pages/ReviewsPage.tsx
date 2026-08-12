import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import { DiffView } from '../components/DiffView'
import type { ReviewState } from '../api/types'

const FILTERS: (ReviewState | 'All')[] = ['Pending', 'Approved', 'Rejected', 'All']

/**
 * The review queue: settled session branches awaiting a verdict. Approving
 * opens a PR (the controller does it through the broker); rejecting records
 * the reason, which seeds a follow-up dispatch.
 */
export function ReviewsPage() {
  const qc = useQueryClient()
  const [filter, setFilter] = useState<ReviewState | 'All'>('Pending')
  const [open, setOpen] = useState<string | null>(null)

  const { data, error } = useQuery({ queryKey: ['reviews'], queryFn: () => api.reviews() })

  // Track which row is in flight so one decision doesn't lock the list.
  const [busy, setBusy] = useState<string | null>(null)

  const decide = useMutation({
    mutationFn: ({
      session,
      decision,
      note,
    }: {
      session: string
      decision: 'approve' | 'reject'
      note: string
    }) => api.review(session, decision, note),
    onMutate: (v) => setBusy(v.session),
    onSettled: () => setBusy(null),
    onSuccess: () => {
      // Refresh both the queue and the nav badge (same query key).
      qc.invalidateQueries({ queryKey: ['reviews'] })
    },
  })

  function reject(session: string) {
    const note = prompt('驳回原因(会作为后续派工的素材):')
    if (note === null) return // cancelled — don't call the API
    if (!note.trim()) {
      alert('驳回必须写明原因。')
      return
    }
    decide.mutate({ session, decision: 'reject', note })
  }

  const rows = (data ?? []).filter((r) => filter === 'All' || r.state === filter)

  return (
    <main>
      <div className="card grow">
        <div className="bar" style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <h2 style={{ margin: 0, flex: 1 }}>批阅队列</h2>
          {FILTERS.map((f) => (
            <button
              key={f}
              className={filter === f ? '' : 'ghost'}
              onClick={() => setFilter(f)}
            >
              {f}
            </button>
          ))}
        </div>
        {error && <div className="err">{(error as Error).message}</div>}
        <div className="scroll">
          {rows.map((r) => (
            <div className="item" key={r.session}>
              <div className="title" title={r.task}>
                {r.task}
              </div>
              <div className="sub">
                <span className={`chip ${r.state}`}>{r.state}</span>
                <span>cell/{r.cell}</span>
                <code>{r.branch}</code>
                {r.prURL && (
                  <a href={r.prURL} target="_blank" rel="noreferrer">
                    PR #{r.prNumber} · {r.prState || 'open'}
                  </a>
                )}
                {r.note && <span title={r.note}>ℹ {r.note.slice(0, 40)}</span>}
                <button
                  className="ghost"
                  onClick={() => setOpen(open === r.session ? null : r.session)}
                >
                  {open === r.session ? '收起 diff' : '看 diff'}
                </button>
                {r.state === 'Pending' && (
                  <>
                    <button
                      onClick={() =>
                        decide.mutate({ session: r.session, decision: 'approve', note: '' })
                      }
                      disabled={busy === r.session}
                    >
                      通过 → 开 PR
                    </button>
                    <button
                      className="ghost"
                      onClick={() => reject(r.session)}
                      disabled={busy === r.session}
                    >
                      驳回
                    </button>
                  </>
                )}
              </div>
              {open === r.session && <DiffView session={r.session} />}
            </div>
          ))}
          {rows.length === 0 && <div className="empty">这个筛选下没有条目。</div>}
        </div>
        {decide.error && <div className="err">{(decide.error as Error).message}</div>}
      </div>
    </main>
  )
}
