import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import { DispatchForm } from '../components/DispatchForm'
import { PreviewPane } from '../components/PreviewPane'
import { SessionList } from '../components/SessionList'
import { Badge, Confirm, Defs, Spinner, useToast } from '../ui/primitives'
import { NONE, cellTone } from '../lib/format'

type Tab = 'overview' | 'sessions' | 'preview' | 'settings'

/**
 * Guidance is computed from live state, not decorative.
 *
 * One sentence saying what to do next beats a page of status the reader has
 * to assemble themselves — and it is derived here rather than written into
 * the empty states, so it cannot go stale against the actual condition.
 */
function nextStep(phase: string, sessions: number, hasPreview: boolean, released: boolean) {
  if (phase === 'Error') return { tone: 'red' as const, text: '这个工作区处于 Error:先看下面的消息,修好之后控制器会自己重试。' }
  if (phase !== 'Ready') return { tone: 'amber' as const, text: '正在准备:克隆仓库、拉起常驻锚点和预览。通常一两分钟。' }
  if (sessions === 0) return { tone: 'green' as const, text: '第一步:派一单工作给 agent。想边看边改就勾上「常驻会话」。' }
  if (!hasPreview) return { tone: 'amber' as const, text: '没有配置预览命令,所以看不到跑起来的产品。在设置里补一条即可。' }
  if (!released) return { tone: 'green' as const, text: '会话在跑。产出满意之后清算 → 批阅 → 发布到正式区。' }
  return { tone: 'green' as const, text: '一切正常。改动经清算和批阅之后,用「发布」推到正式区。' }
}

export function CellPage() {
  const { name = '' } = useParams()
  const qc = useQueryClient()
  const toast = useToast()
  const [tab, setTab] = useState<Tab>('overview')
  const [desc, setDesc] = useState<string | null>(null)
  const [releasing, setReleasing] = useState(false)
  const [newMember, setNewMember] = useState('')
  const [newRole, setNewRole] = useState('member')

  const { data, error, isLoading } = useQuery({
    queryKey: ['cell', name],
    queryFn: () => api.cell(name),
    enabled: !!name,
  })
  const cell = data?.cell
  const sessions = data?.sessions ?? []

  // Node pools are only needed on the settings tab, and reading them needs a
  // cluster-scoped permission an operator may not have granted — so a
  // failure here must degrade to "cannot offer a choice", never break the page.
  const pools = useQuery({
    queryKey: ['nodepools'],
    queryFn: () => api.nodePools(),
    enabled: tab === 'settings',
    retry: false,
  })

  const teams = useQuery({ queryKey: ['teams'], queryFn: () => api.teams(), enabled: tab === 'settings', retry: false })

  const saveTeam = useMutation({
    mutationFn: (t: string) => api.setCellTeam(name, t),
    onSuccess: () => {
      toast.success('归属已更新')
      qc.invalidateQueries({ queryKey: ['cell', name] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const savePlacement = useMutation({
    mutationFn: (label: string) => {
      const [key, ...rest] = label ? label.split('=') : ['']
      return api.savePlacement(name, key, rest.join('='))
    },
    onSuccess: () => {
      toast.success('运行位置已更新,锚点会按新位置重建')
      qc.invalidateQueries({ queryKey: ['cell', name] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const saveDesc = useMutation({
    mutationFn: (d: string) => api.saveDescription(name, d),
    onSuccess: () => {
      toast.success('描述已更新')
      setDesc(null)
      qc.invalidateQueries({ queryKey: ['cell', name] })
    },
    onError: (e) => toast.error((e as Error).message),
  })

  const member = useMutation({
    mutationFn: (v: { id: string; role: string }) => api.putMember(name, v.id, v.role),
    onSuccess: () => {
      setNewMember('')
      toast.success('成员已更新')
      qc.invalidateQueries({ queryKey: ['cell', name] })
    },
    onError: (e) => toast.error((e as Error).message),
  })
  const removeMember = useMutation({
    mutationFn: (id: string) => api.removeMember(name, id),
    onSuccess: () => {
      toast.success('成员已移除')
      qc.invalidateQueries({ queryKey: ['cell', name] })
    },
    onError: (e) => toast.error((e as Error).message),
  })

  const release = useMutation({
    mutationFn: () => api.release(name),
    onSuccess: (r) => {
      toast.success(`已发布 ${r.releaseID}`)
      qc.invalidateQueries({ queryKey: ['cell', name] })
    },
    onError: (e) => toast.error((e as Error).message),
  })

  if (isLoading) {
    return (
      <div className="empty">
        <Spinner /> 加载中…
      </div>
    )
  }
  if (error || !cell) {
    return <div className="form-error">{(error as Error)?.message ?? '找不到这个工作区'}</div>
  }

  const step = nextStep(cell.phase, sessions.filter((s) => s.phase === 'Running').length, !!cell.previewPath, !!cell.releaseRef)

  return (
    <>
      <Link to="/cells" className="back-link">
        ← 返回工作区
      </Link>
      <h1 className="page-title">
        {cell.name}
        <Badge tone={cellTone(cell.phase)}>{cell.phase || 'Unknown'}</Badge>
        <span className="sub">
          槽位 {cell.activeSessions}/{cell.maxSessions}
        </span>
        <span className="spacer" />
        <span className="btn-row">
          <button className="small" disabled={!cell.previewPath} onClick={() => setTab('preview')}>
            打开预览
          </button>
          <button className="primary small" disabled={release.isPending} onClick={() => setReleasing(true)}>
            {release.isPending ? '发布中…' : '发布到正式区'}
          </button>
        </span>
      </h1>

      {cell.phase === 'Error' && cell.message && <div className="form-error">{cell.message}</div>}

      <div className="tabs">
        <button className={tab === 'overview' ? 'active' : ''} onClick={() => setTab('overview')}>
          概览
        </button>
        <button className={tab === 'sessions' ? 'active' : ''} onClick={() => setTab('sessions')}>
          会话 {sessions.length > 0 ? `(${sessions.length})` : ''}
        </button>
        <button className={tab === 'preview' ? 'active' : ''} onClick={() => setTab('preview')}>
          预览
        </button>
        <button className={tab === 'settings' ? 'active' : ''} onClick={() => setTab('settings')}>
          设置
        </button>
      </div>

      {tab === 'overview' && (
        <>
          <div className="card">
            <h3>下一步</h3>
            <div className="row" style={{ alignItems: 'flex-start', gap: 8 }}>
              <Badge tone={step.tone}>{cell.phase || 'Unknown'}</Badge>
            </div>
            <p style={{ margin: '10px 0 0', fontSize: 13 }}>{step.text}</p>
          </div>
          {/* Not wrapped in a card: DispatchForm is one already, and a card
              inside a card gave the page two borders and two headings of the
              same name fighting at two different type scales. */}
          <DispatchForm cell={cell.name} description={cell.description} />
          <div className="card">
            <h3>事实</h3>
            <Defs
              items={[
                ['状态', cell.phase || NONE],
                ['槽位', `${cell.activeSessions} / ${cell.maxSessions}`],
                ['预览路径', cell.previewPath || NONE],
                ['正式区路径', cell.productionPath || NONE],
                ['已发布', cell.releaseRef || NONE],
                ['预览跟随', cell.followSession || NONE],
                ['消息', cell.message || NONE],
              ]}
            />
          </div>
        </>
      )}

      {tab === 'sessions' && (
        <div className="card">
          <h3>
            会话
            <span className="spacer" />
            <span className="faint" style={{ fontSize: 11, textTransform: 'none', letterSpacing: 0 }}>
              别人正在跑的会话在他清算之前你看不到
            </span>
          </h3>
          <SessionList sessions={sessions} cell={cell.name} />
        </div>
      )}

      {tab === 'preview' && (
        <div className="card">
          <h3>预览</h3>
          <PreviewPane cell={cell} onRelease={() => setReleasing(true)} releasing={release.isPending} />
        </div>
      )}

      {tab === 'settings' && (
        <>
        <div className="card">
          <h3>运行位置</h3>
          <p className="hint" style={{ marginTop: 0 }}>
            一个工作区<b>跑在一台机器上</b>——工作区卷是 ReadWriteOnce,所有 pod 跟着锚点走。
            所以这里选的是「哪一类机器」,也就是这个项目的全部算力上限。
          </p>

          {cell.node ? (
            <div className="note" style={{ marginTop: 10 }}>
              当前在 <code className="mono">{cell.node}</code>
              {cell.pool ? <> ,限定在 <code className="mono">{cell.pool}</code></> : <> ,未限定(调度器自选)</>}
            </div>
          ) : cell.schedulingMessage ? (
            /* The failure this whole feature exists to make visible: a Cell
               that has landed nowhere. Previously this read as "Pending"
               forever with the reason in an event nobody could reach. */
            <div className="note red" style={{ marginTop: 10 }}>
              <b>还没有机器能放下它。</b>调度器说:
              <div className="mono" style={{ marginTop: 6, whiteSpace: 'pre-wrap' }}>
                {cell.schedulingMessage}
              </div>
            </div>
          ) : (
            <div className="note" style={{ marginTop: 10 }}>正在调度……</div>
          )}

          {pools.isError ? (
            <p className="hint">
              读不到节点列表,所以没法在这里选机器。celld 需要 <code className="mono">nodes</code> 的只读权限
              ——用新版 chart 升级即可。
            </p>
          ) : (
            <div className="table-wrap" style={{ marginTop: 10 }}>
              <table className="data">
                <thead>
                  <tr>
                    <th>机器池</th>
                    <th>节点</th>
                    <th>单机可用(最大)</th>
                    <th>污点</th>
                    <th style={{ width: 90 }}></th>
                  </tr>
                </thead>
                <tbody>
                  {(pools.data ?? []).map((p) => {
                    const current = cell.pool === p.label
                    return (
                      <tr key={p.label}>
                        <td className="mono">{p.label}</td>
                        <td>{p.nodes}</td>
                        <td className="mono">
                          {p.schedulable ? `${p.freeCPU} / ${p.freeMemory}` : <span className="faint">不可调度</span>}
                        </td>
                        <td className="faint mono" style={{ fontSize: 11 }}>
                          {p.taints.length ? p.taints.join(' ') : NONE}
                        </td>
                        <td>
                          <button
                            className="ghost small"
                            disabled={current || savePlacement.isPending}
                            onClick={() => savePlacement.mutate(p.label)}
                          >
                            {current ? '当前' : '放这里'}
                          </button>
                        </td>
                      </tr>
                    )
                  })}
                  {(pools.data ?? []).length === 0 && !pools.isLoading && (
                    <tr>
                      <td className="faint">没有可选的机器池</td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          )}
          {/* 污点由平台按所选池自己算出来加上:选定一个专用池之后,再被它自己的污点挡在门外
              不是安全性,是谜题。 */}
          {cell.pool && (
            <div className="row" style={{ marginTop: 10 }}>
              <button className="ghost small" onClick={() => savePlacement.mutate('')}>
                取消限定,交回调度器
              </button>
            </div>
          )}
          <p className="hint">
            改动会让锚点重建,这个工作区会短暂中断;正在跑的会话会被清算。
          </p>
        </div>

        <div className="card">
          <h3>归属团队</h3>
          <p className="hint" style={{ marginTop: 0 }}>
            团队成员把角色带进这个工作区,不用一个个加。下面「访问」里单独点名的人以那里为准
            ——<b>既能提高也能降低</b>。
          </p>
          <div className="row" style={{ marginTop: 8 }}>
            <select
              value={cell.team ?? ''}
              onChange={(e) => saveTeam.mutate(e.target.value)}
              disabled={saveTeam.isPending}
              style={{ minWidth: 240 }}
            >
              <option value="">不归属任何团队</option>
              {(teams.data ?? []).map((t) => (
                <option key={t.name} value={t.name}>
                  {t.displayName || t.name}
                </option>
              ))}
            </select>
          </div>
          {cell.team ? (
            <p className="hint">
              归属 <code className="mono">{cell.team}</code>。归属团队会让这个工作区变成按成员授权
              ——属于某个组的项目,不该同时对所有能登录的人开放。
            </p>
          ) : (
            <p className="hint">
              你只能选自己所在的团队:否则「设置归属」就成了从外面把项目交出去、或者接管过来的办法。
            </p>
          )}
        </div>

        <div className="card">
          <h3>访问</h3>
          {cell.access === 'open' ? (
            <div className="note">
              这个工作区<b>对所有登录用户开放</b>——任何人都能派工、批阅、发布到正式区。
              添加第一个成员就会切换为按成员授权。
            </div>
          ) : (
            <p className="hint" style={{ marginTop: 0 }}>
              只有下面列出的人有权限。<code className="mono">maintainer</code> 才能发布和改设置。
            </p>
          )}
          <div className="table-wrap" style={{ marginTop: 10 }}>
            <table className="data">
              <tbody>
                {(cell.members ?? []).map((m) => (
                  <tr key={m.userID}>
                    <td className="mono">{m.userID}</td>
                    <td style={{ width: 150 }}>
                      <select
                        value={m.role}
                        onChange={(e) => member.mutate({ id: m.userID, role: e.target.value })}
                      >
                        <option value="viewer">viewer</option>
                        <option value="member">member</option>
                        <option value="maintainer">maintainer</option>
                      </select>
                    </td>
                    <td style={{ width: 80 }}>
                      <button className="ghost small" onClick={() => removeMember.mutate(m.userID)}>
                        移除
                      </button>
                    </td>
                  </tr>
                ))}
                {(cell.members ?? []).length === 0 && (
                  <tr>
                    <td className="faint">还没有成员</td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
          <div className="row" style={{ marginTop: 10 }}>
            <input
              value={newMember}
              placeholder="用户 id(形如 u-1a2b3c4d,在对方的账号里可见)"
              onChange={(e) => setNewMember(e.target.value)}
            />
            <select value={newRole} onChange={(e) => setNewRole(e.target.value)} style={{ width: 150 }}>
              <option value="viewer">viewer</option>
              <option value="member">member</option>
              <option value="maintainer">maintainer</option>
            </select>
            <button
              className="small"
              disabled={!newMember.trim() || member.isPending}
              onClick={() => member.mutate({ id: newMember.trim(), role: newRole })}
            >
              添加
            </button>
          </div>
        </div>
        <div className="card">
          <h3>产品描述</h3>
          <p className="hint" style={{ marginTop: 0 }}>
            每次派工都会带给 agent。这是你「边看预览边校准」的地方。
          </p>
          <textarea
            rows={5}
            value={desc ?? cell.description}
            onChange={(e) => setDesc(e.target.value)}
          />
          <div className="btn-row" style={{ marginTop: 10 }}>
            <button
              className="primary small"
              disabled={desc === null || saveDesc.isPending}
              onClick={() => saveDesc.mutate(desc ?? '')}
            >
              {saveDesc.isPending ? '保存中…' : '保存'}
            </button>
            {desc !== null && (
              <button className="ghost small" onClick={() => setDesc(null)}>
                取消
              </button>
            )}
          </div>
        </div>
        </>
      )}

      {releasing && (
        <Confirm
          title="发布到正式区?"
          body={
            <>
              会把当前基线分支重新检出到<b>正式区</b>并重启它。开发区的调试完全不受影响 —— 这两个区是隔离的,
              而且正式区只能从这里进。
              <br />
              当前已发布:<code className="mono">{cell.releaseRef || '(还没有发布过)'}</code>
            </>
          }
          confirmText="发布"
          onConfirm={() => {
            release.mutate()
            setReleasing(false)
          }}
          onCancel={() => setReleasing(false)}
        />
      )}
    </>
  )
}
