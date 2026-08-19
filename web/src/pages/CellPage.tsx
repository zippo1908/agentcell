import { useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import type { Cell } from '../api/types'
import { DispatchForm } from '../components/DispatchForm'
import { SessionList } from '../components/SessionList'
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
  if (phase === 'Error') return { tone: 'red' as const, text: '这个工作区处于 Error:先看下面的消息,修好之后控制器会自己重试。' }
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

  // Node pools are only needed on the settings tab, and reading them needs a
  // cluster-scoped permission an operator may not have granted — so a
  // failure here must degrade to "cannot offer a choice", never break the page.
  const pools = useQuery({
    queryKey: ['nodepools'],
    queryFn: () => api.nodePools(),
    enabled: tab === 'settings',
    retry: false,
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

/**
 * 项目知识库 — what people put here for the agent to work from.
 *
 * The upload API, the text extraction and the delivery into every sandbox
 * were all built and then had no way in: the console never showed a file
 * list, so the only way to put a spec in front of an agent was to paste it
 * into a task. This is that missing door.
 */
function Knowledge({ cell }: { cell: string }) {
  const qc = useQueryClient()
  const toast = useToast()
  const [busy, setBusy] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  const files = useQuery({ queryKey: ['files', cell], queryFn: () => api.files(cell) })

  const remove = useMutation({
    mutationFn: (path: string) => api.deleteFile(cell, path),
    onSuccess: () => {
      toast.success('已删除')
      qc.invalidateQueries({ queryKey: ['files', cell] })
    },
    onError: (e) => toast.error((e as Error).message),
  })

  async function upload(list: FileList | null) {
    if (!list?.length) return
    setBusy(true)
    try {
      for (const f of Array.from(list)) await api.uploadFile(cell, f)
      toast.success(list.length > 1 ? `已上传 ${list.length} 个文件` : '已上传')
      qc.invalidateQueries({ queryKey: ['files', cell] })
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setBusy(false)
      if (inputRef.current) inputRef.current.value = ''
    }
  }

  const list = files.data?.files ?? []

  return (
    <div className="card">
      <h3>项目知识库</h3>
      <p className="hint" style={{ marginTop: 0 }}>
        放在这里的东西会跟着每一次派工进到沙盒里(<code className="mono">.agentcell/library/</code>),
        agent 不用你在任务里重复贴一遍。规格、截图、导出的表格、会议纪要都行。
      </p>

      <div className="row" style={{ marginTop: 10 }}>
        <input ref={inputRef} type="file" multiple disabled={busy} onChange={(e) => upload(e.target.files)} />
        {busy && <span className="faint">上传中…</span>}
      </div>

      <div className="table-wrap" style={{ marginTop: 12 }}>
        <table className="data">
          <thead>
            <tr>
              <th>文件</th>
              <th style={{ width: 90 }}>大小</th>
              <th style={{ width: 110 }}>agent 可读</th>
              <th style={{ width: 80 }} />
            </tr>
          </thead>
          <tbody>
            {list.map((f) => (
              <tr key={f.path}>
                <td>
                  <a href={api.fileURL(cell, f.path)} download className="mono">
                    {f.path}
                  </a>
                </td>
                <td className="mono faint">{humanSize(f.size)}</td>
                <td>
                  {/* Being explicit about this matters: an unreadable file is
                      still delivered, but the agent sees only its name — and
                      "I uploaded it and it was ignored" is the confusion that
                      follows from not saying so. */}
                  {f.readable ? <Badge tone="green">正文可读</Badge> : <span className="faint">只有文件名</span>}
                </td>
                <td>
                  <button className="ghost small" onClick={() => remove.mutate(f.path)}>
                    删除
                  </button>
                </td>
              </tr>
            ))}
            {list.length === 0 && !files.isLoading && (
              <tr>
                <td className="faint" colSpan={4}>
                  还没有东西。上传的第一份规格,下一次派工就带进去了。
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function humanSize(n: number) {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

/**
 * 项目成员管理.
 *
 * It lived in a corner of the settings tab and listed hashed ids — an access
 * list nobody can read is not one anybody checks. Names now, and adding
 * somebody is done by their address, which is what a person actually knows.
 */
function Members({
  cell,
  open,
  members,
}: {
  cell: string
  open: boolean
  members: { userID: string; role: string }[]
}) {
  const qc = useQueryClient()
  const toast = useToast()
  const [who, setWho] = useState('')
  const [role, setRole] = useState('member')

  const list = useQuery({ queryKey: ['members', cell], queryFn: () => api.members(cell) })
  const done = () => {
    qc.invalidateQueries({ queryKey: ['members', cell] })
    qc.invalidateQueries({ queryKey: ['cell', cell] })
  }
  const put = useMutation({
    mutationFn: (v: { id: string; role: string }) => api.putMember(cell, v.id, v.role),
    onSuccess: () => {
      setWho('')
      toast.success('成员已更新')
      done()
    },
    onError: (e) => toast.error((e as Error).message),
  })
  const drop = useMutation({
    mutationFn: (id: string) => api.removeMember(cell, id),
    onSuccess: () => {
      toast.success('成员已移除')
      done()
    },
    onError: (e) => toast.error((e as Error).message),
  })

  const rows = list.data?.members ?? []

  return (
    <div className="card">
      <h3>项目成员管理</h3>
      {open ? (
        <div className="note">
          这个项目<b>对所有登录用户开放</b> —— 任何人都能在里面干活、批阅、发布到正式区。
          加进第一个人就会切换成按成员授权。
        </div>
      ) : (
        <p className="hint" style={{ marginTop: 0 }}>
          只有下面的人有权限。<code className="mono">maintainer</code> 才能发布和改设置。
        </p>
      )}

      <div className="table-wrap" style={{ marginTop: 10 }}>
        <table className="data">
          <tbody>
            {rows.map((m) => (
              <tr key={m.email}>
                <td>
                  {m.name ? (
                    <>
                      {m.name} <span className="faint mono">{m.email}</span>
                    </>
                  ) : (
                    <span className="mono">{m.email}</span>
                  )}
                  {/* An id matching no account: somebody removed from the
                      platform, or a project from before accounts existed.
                      Shown rather than hidden — a silent gap in an access
                      list is the one nobody notices. */}
                  {m.unknown && <span className="faint"> (查无此人)</span>}
                </td>
                <td style={{ width: 150 }}>
                  <select value={m.role} onChange={(e) => put.mutate({ id: m.email, role: e.target.value })}>
                    <option value="viewer">viewer</option>
                    <option value="member">member</option>
                    <option value="maintainer">maintainer</option>
                  </select>
                </td>
                <td style={{ width: 80 }}>
                  <button className="ghost small" onClick={() => drop.mutate(m.email)}>
                    移除
                  </button>
                </td>
              </tr>
            ))}
            {rows.length === 0 && members.length === 0 && (
              <tr>
                <td className="faint">还没有成员</td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="row" style={{ marginTop: 10 }}>
        <input value={who} placeholder="邮箱" onChange={(e) => setWho(e.target.value)} style={{ minWidth: 240 }} />
        <select value={role} onChange={(e) => setRole(e.target.value)} style={{ width: 150 }}>
          <option value="viewer">viewer</option>
          <option value="member">member</option>
          <option value="maintainer">maintainer</option>
        </select>
        <button className="small" disabled={!who.trim() || put.isPending} onClick={() => put.mutate({ id: who.trim(), role })}>
          添加
        </button>
      </div>
      <p className="hint">这里加进来的人,在黑板上输入 @ 就能选到。</p>
    </div>
  )
}

/**
 * 项目令牌管理 — which credential this project uses to reach its forge.
 *
 * Distinct from a personal token (bound in 我的凭据) and from the grant that
 * lets somebody create projects at all. This one answers a narrower
 * question: when THIS project clones and pushes, whose credential does it
 * use. Swapping it is safe — the codebase does not move — so it is a control
 * rather than a migration.
 */
function ProjectTokens({ cell }: { cell: Cell }) {
  const qc = useQueryClient()
  const toast = useToast()
  const opts = useQuery({ queryKey: ['new-project-options'], queryFn: () => api.newProjectOptions() })
  const [picked, setPicked] = useState<string | null>(null)

  const save = useMutation({
    mutationFn: (name: string) => api.setRepoCredential(cell.name, name),
    onSuccess: () => {
      toast.success('已切换,锚点会用新凭据重新拉取')
      setPicked(null)
      qc.invalidateQueries({ queryKey: ['cell', cell.name] })
    },
    onError: (e) => toast.error((e as Error).message),
  })

  const current = cell.repoSecretName ?? ''
  const value = picked ?? current

  return (
    <div className="card">
      <h3>项目令牌管理</h3>
      <p className="hint" style={{ marginTop: 0 }}>
        这个项目 clone 和 push 时用哪份凭据。列表里只有<b>你自己的</b>和平台公用的 ——
        在「我的凭据」里绑过 GitLab 令牌的话,它就在这里。
      </p>

      {!cell.repoURL ? (
        <div className="note">这个项目还没有关联仓库,所以还用不上凭据。先去「概览」关联。</div>
      ) : (
        <>
          <div className="note" style={{ marginTop: 10 }}>
            仓库 <code className="mono">{cell.repoURL}</code>
            {' · 当前凭据 '}
            <code className="mono">{current || '(无,按公开仓库处理)'}</code>
          </div>
          <div className="row" style={{ marginTop: 12 }}>
            <select value={value} onChange={(e) => setPicked(e.target.value)} style={{ minWidth: 260 }}>
              <option value="">不需要(公开仓库)</option>
              {(opts.data?.gitCredentials ?? []).map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </select>
            <button
              className="primary small"
              disabled={picked === null || picked === current || save.isPending}
              onClick={() => save.mutate(picked ?? '')}
            >
              {save.isPending ? '切换中…' : '换成这个'}
            </button>
          </div>
        </>
      )}
    </div>
  )
}
