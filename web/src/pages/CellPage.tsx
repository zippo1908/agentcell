import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import { DispatchForm } from '../components/DispatchForm'
import { SessionList } from '../components/SessionList'
import { Knowledge, Members, ProjectTokens } from '../components/ProjectPanels'
import { Badge, Confirm, Defs, Spinner, useToast } from '../ui/primitives'
import { NONE, cellTone } from '../lib/format'

// The preview is no longer a tab. It was one of six things competing for the
// same strip while being the one thing that is better looked at full-size in
// its own window — and the workspace already embeds it. What belongs here is
// the project's own material: what it knows, who is on it, what it uses to
// reach its forge.
type Tab = 'overview' | 'sessions' | 'knowledge' | 'members' | 'tokens' | 'settings'

/**
 * Guidance is computed from live state, not decorative.
 *
 * One sentence saying what to do next beats a page of status the reader has
 * to assemble themselves — and it is derived here rather than written into
 * the empty states, so it cannot go stale against the actual condition.
 */
function nextStep(phase: string, sessions: number, hasPreview: boolean, released: boolean) {
  if (phase === 'Error') return { tone: 'red' as const, text: '这个项目处于 Error:先看下面的消息,修好之后控制器会自己重试。' }
  if (phase !== 'Ready') return { tone: 'amber' as const, text: '正在准备:克隆仓库、拉起常驻锚点和预览。通常一两分钟。' }
  if (sessions === 0) return { tone: 'green' as const, text: '第一步:派一单工作给 agent。想边看边改就勾上「常驻会话」。' }
  // Not "go and configure a command" any more: nobody configures one. If
  // there is still no preview, detection looked and found nothing to serve,
  // and the reason is in the anchor's log rather than in a form.
  if (!hasPreview) return { tone: 'amber' as const, text: '还没有可用的预览:平台没在仓库里认出能跑起来的东西(package.json 的 dev/start、Django、静态页)。等代码进来会自动重试。' }
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

  const { data, error, isLoading } = useQuery({
    queryKey: ['cell', name],
    queryFn: () => api.cell(name),
    enabled: !!name,
  })
  const cell = data?.cell
  const sessions = data?.sessions ?? []

  // Placement classes are only needed on the settings tab; a failure here
  // must degrade to "cannot offer a choice", never break the page.
  const pools = useQuery({
    queryKey: ['placementclasses'],
    queryFn: () => api.placementClasses(),
    enabled: tab === 'settings',
    retry: false,
  })



  const savePlacement = useMutation({
    mutationFn: (className: string) => api.savePlacement(name, className),
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
    return <div className="form-error">{(error as Error)?.message ?? '找不到这个项目'}</div>
  }

  const step = nextStep(cell.phase, sessions.filter((s) => s.phase === 'Running').length, !!cell.previewPath, !!cell.releaseRef)

  return (
    <>
      <Link to="/cells" className="back-link">
        ← 返回项目
      </Link>
      <h1 className="page-title">
        {cell.displayName || cell.name}
        <Badge tone={cellTone(cell.phase)}>{cell.phase || 'Unknown'}</Badge>
        <span className="sub">
          槽位 {cell.activeSessions}/{cell.maxSessions}
        </span>
        <span className="spacer" />
        <span className="btn-row">
          {/* Opened, not embedded: the preview serves the agent's unreviewed
              work from a separate origin (ADR-0007), and it is worth a whole
              window rather than a card. */}
          <button
            className="small"
            disabled={!cell.previewURL}
            onClick={() => window.open(cell.previewURL, '_blank', 'noopener')}
          >
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
        <button className={tab === 'knowledge' ? 'active' : ''} onClick={() => setTab('knowledge')}>
          项目知识库
        </button>
        <button className={tab === 'members' ? 'active' : ''} onClick={() => setTab('members')}>
          项目成员管理
        </button>
        <button className={tab === 'tokens' ? 'active' : ''} onClick={() => setTab('tokens')}>
          项目令牌管理
        </button>
        <button className={tab === 'settings' ? 'active' : ''} onClick={() => setTab('settings')}>
          设置
        </button>
      </div>

      {tab === 'overview' && (
        <>
          {/* First on the page when it applies: a project with no repository
              can be looked at but not worked in, and every other card here
              describes work that cannot start until this is answered. */}
          {!cell.repoURL && <AttachRepo cell={cell.name} />}
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
                ['仓库', cell.repoURL ? `${cell.repoURL} (${cell.repoBranch || 'main'})` : '还没关联'],
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

      {tab === 'knowledge' && <Knowledge cell={cell.name} />}
      {tab === 'members' && (
        <Members cell={cell.name} open={cell.access === 'open'} members={cell.members ?? []} />
      )}
      {tab === 'tokens' && <ProjectTokens cell={cell} />}

      {tab === 'settings' && (
        <>
        <div className="card">
          <h3>运行位置</h3>
          <p className="hint" style={{ marginTop: 0 }}>
            一个项目<b>跑在一台机器上</b>——项目卷是 ReadWriteOnce,所有 pod 跟着锚点走。
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
              读不到机器池列表,所以没法在这里选机器。celld 需要{' '}
              <code className="mono">placementclasses</code> 的只读权限 ——用新版 chart 升级即可。
            </p>
          ) : (
            <div className="table-wrap" style={{ marginTop: 10 }}>
              <table className="data">
                <thead>
                  <tr>
                    <th>机器池</th>
                    <th>节点</th>
                    <th>单机可用(最大)</th>
                    <th>类型</th>
                    <th style={{ width: 90 }}></th>
                  </tr>
                </thead>
                <tbody>
                  {(pools.data ?? []).map((p) => {
                    const current = cell.pool === p.selector
                    return (
                      <tr key={p.name}>
                        <td>
                          <div style={{ fontWeight: 600 }}>{p.displayName || p.name}</div>
                          <div className="mono faint">{p.selector}</div>
                        </td>
                        <td>{p.nodes}</td>
                        <td className="mono">{p.free || <span className="faint">{NONE}</span>}</td>
                        <td>
                          {p.tolerated ? <Badge tone="amber">专用</Badge> : <Badge tone="gray">共享</Badge>}
                        </td>
                        <td>
                          <button
                            className="ghost small"
                            disabled={current || savePlacement.isPending}
                            onClick={() => savePlacement.mutate(p.name)}
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
            改动会让锚点重建,这个项目会短暂中断;正在跑的会话会被清算。
          </p>
        </div>

        <div className="card">
          <h3>产品描述</h3>
          <p className="hint" style={{ marginTop: 0 }}>
            每次跟 agent 说话都会带上。这是你「边看预览边校准」的地方。
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

/**
 * Pointing a project at its repository, after the fact.
 *
 * Creating a project no longer demands a URL, because a project is usually
 * agreed on before somebody creates the GitLab repository for it. What was
 * left behind was a project that looks fine and quietly cannot do anything:
 * no checkout, so no worktree, so nothing for an agent to work on. This card
 * exists to say that out loud and to fix it in one step.
 */
function AttachRepo({ cell }: { cell: string }) {
  const qc = useQueryClient()
  const toast = useToast()
  const [url, setUrl] = useState('')
  const [branch, setBranch] = useState('main')
  const [secretName, setSecretName] = useState('')

  const opts = useQuery({ queryKey: ['new-project-options'], queryFn: () => api.newProjectOptions() })

  const attach = useMutation({
    mutationFn: () => api.attachRepo(cell, url.trim(), branch.trim(), secretName),
    onSuccess: () => {
      toast.success('仓库已关联,工作区会重新拉取')
      qc.invalidateQueries({ queryKey: ['cell', cell] })
    },
    onError: (e) => toast.error((e as Error).message),
  })

  return (
    <div className="card">
      <h3>还没有关联仓库</h3>
      <p className="hint" style={{ marginTop: 0 }}>
        这个项目建起来了,但还没有代码。关联之后 agent 才有东西可以干活,
        产出会推成 <code className="mono">session/&lt;id&gt;</code> 分支等你批阅。
      </p>
      <label className="field">
        <span>仓库地址</span>
        <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://git.tinci.com/team/shop.git" />
      </label>
      <div className="row">
        <label className="field" style={{ flex: 1 }}>
          <span>基线分支</span>
          <input value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="main" />
        </label>
        <label className="field" style={{ flex: 1 }}>
          <span>git 凭据</span>
          <select value={secretName} onChange={(e) => setSecretName(e.target.value)}>
            <option value="">不需要(公开仓库)</option>
            {(opts.data?.gitCredentials ?? []).map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
          <div className="hint">在「我的凭据」里绑定过 GitLab 令牌的话,它就在这个列表里。</div>
        </label>
      </div>
      <div className="btn-row" style={{ marginTop: 12 }}>
        <button className="primary" disabled={!url.trim() || attach.isPending} onClick={() => attach.mutate()}>
          {attach.isPending ? '关联中…' : '关联仓库'}
        </button>
      </div>
    </div>
  )
}
