import { useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import type { Cell } from '../api/types'
import { Badge, useToast } from '../ui/primitives'

/**
 * The three things a project owns, as panels.
 *
 * They live here rather than on one page because they are needed in two:
 * the project's own page, and the workspace — which is where people
 * actually are. Putting them only on the project page meant shipping them
 * somewhere nobody navigates to: the console's whole navigation is 黑板 and
 * 工作台, and everything else is behind an avatar menu. A feature reachable
 * only from a menu nobody opens has, from where the user sits, not shipped.
 */

/**
 * 项目知识库 — what people put here for the agent to work from.
 *
 * The upload API, the text extraction and the delivery into every sandbox
 * were all built and then had no way in: the console never showed a file
 * list, so the only way to put a spec in front of an agent was to paste it
 * into a task. This is that missing door.
 */
export function Knowledge({ cell }: { cell: string }) {
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

  const list = files.data ?? []

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
export function Members({
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
export function ProjectTokens({ cell }: { cell: Cell }) {
  const qc = useQueryClient()
  const toast = useToast()
  const opts = useQuery({ queryKey: ['new-project-options'], queryFn: () => api.newProjectOptions() })
  const [picked, setPicked] = useState<string | null>(null)
  const [username, setUsername] = useState('')
  const [token, setToken] = useState('')

  const done = (msg: string) => {
    toast.success(msg)
    setPicked(null)
    setUsername('')
    setToken('')
    qc.invalidateQueries({ queryKey: ['cell', cell.name] })
    // The new credential has to appear in the picker too, or it looks like
    // nothing was saved.
    qc.invalidateQueries({ queryKey: ['new-project-options'] })
  }
  const save = useMutation({
    mutationFn: (name: string) => api.setRepoCredential(cell.name, name),
    onSuccess: () => done('已切换,锚点会用新凭据重新拉取'),
    onError: (e) => toast.error((e as Error).message),
  })
  const enter = useMutation({
    mutationFn: () => api.setRepoToken(cell.name, username.trim(), token.trim()),
    onSuccess: () => done('令牌已保存并绑到这个项目'),
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

          {/* Typing one in, for the person whose list is empty. Choosing from
              a list of nothing, with "go to another page and come back" as
              the only way forward, is the shape this platform keeps trying
              to remove. */}
          <h3 style={{ marginTop: 22 }}>或者直接填一个</h3>
          <p className="hint" style={{ marginTop: 0 }}>
            存成这个项目自己的凭据(<code className="mono">{cell.name}-git</code>),归你所有。
            和「我的凭据」里那份个人身份是两回事 —— 这里可以放一个部署专用的令牌。
          </p>
          <div className="row" style={{ flexWrap: 'wrap' }}>
            <input
              placeholder="用户名"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              style={{ minWidth: 160 }}
            />
            <input
              type="password"
              placeholder="访问令牌"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              style={{ minWidth: 260 }}
              autoComplete="new-password"
            />
            <button
              disabled={!username.trim() || !token.trim() || enter.isPending}
              onClick={() => enter.mutate()}
            >
              {enter.isPending ? '保存中…' : '保存并使用'}
            </button>
          </div>
        </>
      )}
    </div>
  )
}
