import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'

/** Fleet view: every Cell with its phase, slot usage and zone links. */
export function CellsPage() {
  const { data, isLoading, error } = useQuery({
    queryKey: ['cells'],
    queryFn: api.cells,
  })

  return (
    <main>
      <div className="card grow">
        <h2>Cells</h2>
        {isLoading && <div className="empty">加载中…</div>}
        {error && <div className="err">{(error as Error).message}</div>}
        <div className="scroll">
          {data?.map((c) => (
            <div className="item" key={c.name}>
              <div className="title">
                <Link to={`/cells/${c.name}`}>
                  <strong>{c.name}</strong>
                </Link>
                {c.description ? ` — ${c.description}` : ''}
              </div>
              <div className="sub">
                <span className={`chip ${c.phase}`}>{c.phase || '…'}</span>
                <span>
                  槽位 {c.activeSessions}/{c.maxSessions}
                </span>
                {c.followSession && <span>预览跟随 {c.followSession.slice(0, 8)}…</span>}
                <a href={c.previewPath} target="_blank" rel="noreferrer">
                  开发区 ↗
                </a>
                {c.productionPath && (
                  <a href={c.productionPath} target="_blank" rel="noreferrer">
                    正式区 ↗
                  </a>
                )}
                {c.message && <span title={c.message}>ℹ</span>}
              </div>
            </div>
          ))}
          {data?.length === 0 && (
            <div className="empty">
              还没有 Cell。用 <code>cellctl cell create …</code> 建第一个。
            </div>
          )}
        </div>
      </div>
    </main>
  )
}
