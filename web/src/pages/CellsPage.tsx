import { Link, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'
import { Badge, EmptyState, SkeletonTable } from '../ui/primitives'
import { NONE, cellTone } from '../lib/format'

/**
 * One card, one wide table: a monochrome grid with a constellation of small
 * status dots. The point of this page is to see every project's state
 * without scrolling, so density is the feature.
 */
export function CellsPage() {
  const nav = useNavigate()
  const { data: cells, isLoading, error } = useQuery({ queryKey: ['cells'], queryFn: api.cells })

  return (
    <>
      <h1 className="page-title">
        工作区
        <span className="spacer" />
        <span className="btn-row">
          <Link to="/cells/new" style={{ borderBottom: 'none' }}>
            <button className="primary small">新建工作区</button>
          </Link>
        </span>
      </h1>

      {error && <div className="form-error">{(error as Error).message}</div>}

      <div className="card">
        <h3>项目</h3>
        {isLoading ? (
          <SkeletonTable rows={4} cols={6} />
        ) : (cells ?? []).length === 0 ? (
          <EmptyState
            title="还没有工作区"
            hint="一个工作区是一个项目:常驻的代码检出、一个预览,和一组会话槽位。建好之后打开它就能干活。"
            action={
              <Link to="/cells/new" style={{ borderBottom: 'none' }}>
                <button className="primary small">新建工作区</button>
              </Link>
            }
          />
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>名称</th>
                  <th>状态</th>
                  <th>槽位</th>
                  <th>预览</th>
                  <th>正式区</th>
                  <th>说明</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {(cells ?? []).map((c) => (
                  <tr key={c.name} className="clickable" onClick={() => nav(`/cells/${c.name}`)}>
                    <td>
                      <div style={{ fontWeight: 600 }}>{c.displayName || c.name}</div>
                      {/* The address, shown small: it is what appears in URLs
                          and namespaces, so it has to be findable — but it is
                          not what the project is called. */}
                      <div className="mono faint">
                        {c.displayName && c.displayName !== c.name ? c.name : ''}
                        {c.followSession ? ` 跟随 ${c.followSession.slice(0, 8)}` : ''}
                      </div>
                    </td>
                    <td>
                      <Badge tone={cellTone(c.phase)}>{c.phase || 'Unknown'}</Badge>
                    </td>
                    <td className="num-col">
                      {c.activeSessions}/{c.maxSessions}
                    </td>
                    <td>
                      {c.previewPath ? <Badge tone="green">就绪</Badge> : <Badge>未配置</Badge>}
                    </td>
                    <td>
                      {c.releaseRef ? (
                        <span className="mono">{c.releaseRef}</span>
                      ) : (
                        <span className="faint">未发布</span>
                      )}
                    </td>
                    <td className="muted" style={{ maxWidth: 280 }}>
                      {c.description || NONE}
                    </td>
                    <td>
                      <div className="btn-row">
                        <button
                          className="ghost small"
                          onClick={(e) => {
                            e.stopPropagation()
                            nav(`/cells/${c.name}`)
                          }}
                        >
                          打开 →
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  )
}
