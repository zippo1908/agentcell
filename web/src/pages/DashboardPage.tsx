import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'
import { Badge, EmptyState, Stat } from '../ui/primitives'
import { NONE, ago, cellTone, sessionTone } from '../lib/format'

/**
 * "What do I owe people" — not a metrics wall.
 *
 * A tile only earns its place if a number on it would change what you do
 * next, which is why the degraded count appears only when it is non-zero:
 * a permanent zero teaches you to stop reading the row.
 */
export function DashboardPage() {
  const { data: cells, isLoading } = useQuery({ queryKey: ['cells'], queryFn: api.cells })
  const { data: reviews } = useQuery({ queryKey: ['reviews'], queryFn: () => api.reviews() })

  const pending = (reviews ?? []).filter((r) => r.state === 'Pending')
  const running = (cells ?? []).reduce((n, c) => n + c.activeSessions, 0)
  const broken = (cells ?? []).filter((c) => c.phase === 'Error')

  return (
    <>
      <h1 className="page-title">
        概览
        <span className="sub">你欠着什么,和什么在跑</span>
      </h1>

      <div className="stat-row slim">
        <Stat num={isLoading ? NONE : (cells?.length ?? 0)} label="项目" tone="accent" />
        <Stat num={isLoading ? NONE : running} label="在跑的会话" tone={running > 0 ? 'green' : undefined} />
        <Stat num={pending.length} label="待批阅" tone={pending.length > 0 ? 'amber' : undefined} />
        {broken.length > 0 && <Stat num={broken.length} label="异常项目" tone="red" />}
      </div>

      <div className="card">
        <h3>
          待你批阅
          <span className="spacer" />
          <Link to="/reviews" className="faint" style={{ fontSize: 11, borderBottom: 'none' }}>
            全部 →
          </Link>
        </h3>
        {pending.length === 0 ? (
          <EmptyState title="没有待批阅的产出" hint="agent settle 之后,分支和 diff 会出现在这里等你过目。" />
        ) : (
          pending.slice(0, 6).map((r) => (
            <div className="item" key={r.session}>
              <div className="title">{r.task || NONE}</div>
              <div className="sub">
                <Badge tone="amber">Pending</Badge>
                <Link to={`/cells/${r.cell}`}>cell/{r.cell}</Link>
                <code className="mono">{r.branch}</code>
              </div>
            </div>
          ))
        )}
      </div>

      <div className="card">
        <h3>
          项目
          <span className="spacer" />
          <Link to="/cells/new" className="faint" style={{ fontSize: 11, borderBottom: 'none' }}>
            新建 →
          </Link>
        </h3>
        {(cells ?? []).length === 0 ? (
          <EmptyState
            title="还没有项目"
            hint="项目 = 常驻的代码检出、预览,和一组会话槽位。"
            action={
              <Link to="/cells/new" style={{ borderBottom: 'none' }}>
                <button className="primary small">新建项目</button>
              </Link>
            }
          />
        ) : (
          (cells ?? []).map((c) => (
            <div className="item" key={c.name}>
              <div className="title">
                <Link to={`/cells/${c.name}`}>{c.name}</Link>
              </div>
              <div className="sub">
                <Badge tone={cellTone(c.phase)}>{c.phase || 'Unknown'}</Badge>
                <span>
                  槽位 {c.activeSessions}/{c.maxSessions}
                </span>
                {c.releaseRef && <span className="mono faint">已发布 {c.releaseRef}</span>}
                <span className="faint">{c.description || NONE}</span>
              </div>
            </div>
          ))
        )}
      </div>
    </>
  )
}

/** Exported for the cells page so both render a session the same way. */
export function SessionBadge({ phase }: { phase: string }) {
  return <Badge tone={sessionTone(phase)}>{phase || 'Unknown'}</Badge>
}

export function Ago({ at }: { at?: string }) {
  return <span className="faint">{ago(at)}</span>
}
