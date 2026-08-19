import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import { EmptyState, Tag, useToast } from '../ui/primitives'

/**
 * Your own model keys.
 *
 * These were Kubernetes Secrets created with kubectl, so "bring your own
 * key" required cluster access — a colleague could be handed a console and
 * still not be able to do the one thing that makes it useful.
 *
 * A key goes in and never comes back: the list shows the last four
 * characters, which is enough to tell three keys apart and nothing more.
 */
export function CredentialsPage() {
  const qc = useQueryClient()
  const toast = useToast()
  const [name, setName] = useState('')
  const [key, setKey] = useState('')

  const { data: creds } = useQuery({ queryKey: ['credentials'], queryFn: api.credentials })

  const save = useMutation({
    mutationFn: () => api.putCredential(name.trim(), key),
    onSuccess: () => {
      setName('')
      setKey('')
      toast.success('凭据已保存')
      qc.invalidateQueries({ queryKey: ['credentials'] })
    },
    onError: (e) => toast.error((e as Error).message),
  })
  const remove = useMutation({
    mutationFn: (n: string) => api.deleteCredential(n),
    onSuccess: () => {
      toast.success('已删除')
      qc.invalidateQueries({ queryKey: ['credentials'] })
    },
    onError: (e) => toast.error((e as Error).message),
  })

  return (
    <>
      <h1 className="page-title">
        我的凭据
        <span className="sub">模型 API key,只有你能用</span>
      </h1>
      <KimiAccount />
      <Lending />
      <ForgeToken />

      <div className="card">
        <h3>已有凭据</h3>
        {(creds ?? []).length === 0 ? (
          <EmptyState
            title="还没有凭据"
            hint="加一把模型 API key,跟 agent 说话时就用它。key 只进不出——存进去之后谁也读不回来,包括你自己。"
          />
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>名称</th>
                  <th>key</th>
                  <th>创建于</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {(creds ?? []).map((c) => (
                  <tr key={c.name}>
                    <td style={{ fontWeight: 600 }}>{c.name}</td>
                    <td>
                      <Tag>{c.hint}</Tag>
                    </td>
                    <td className="muted">{c.created}</td>
                    <td>
                      <button className="ghost small" onClick={() => remove.mutate(c.name)}>
                        删除
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <div className="card">
        <h3>添加</h3>
        <div className="row">
          <input
            value={name}
            placeholder="名称,例如 my-kimi"
            onChange={(e) => setName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ''))}
          />
          <input
            value={key}
            type="password"
            placeholder="API key"
            onChange={(e) => setKey(e.target.value)}
          />
          <button
            className="primary small"
            disabled={!name.trim() || !key || save.isPending}
            onClick={() => save.mutate()}
          >
            {save.isPending ? '保存中…' : '保存'}
          </button>
        </div>
        <div className="hint" style={{ marginTop: 10 }}>
          存进去之后<b>读不回来</b>——列表只显示后四位。它只会被注入到你自己的会话里,
          同一个 runtime 里别人的窗口读不到(注意:同一用户的多个窗口共享 uid,细节见 SECURITY.md)。
        </div>
      </div>
    </>
  )
}


/**
 * Connecting a Kimi account instead of pasting a key.
 *
 * Kimi authenticates with a device-code flow — the OAuth shape built for a
 * machine with no browser, which is exactly what a pod is. The platform runs
 * the login somewhere it controls and shows you the code; you approve it in
 * your own browser; the credential is stored as yours and handed to every
 * session you start after that.
 */
function KimiAccount() {
  const toast = useToast()
  const [state, setState] = useState<{ url?: string; code?: string; status: string; message?: string } | null>(null)
  const [busy, setBusy] = useState(false)

  // Ask once on load: whether this account is already connected is a fact
  // about the person, not about a login in progress.
  useEffect(() => {
    api.kimiLoginPoll().then(setState).catch(() => {})
  }, [])

  // After that, poll only while a login is actually in flight: an idle page
  // has nothing to ask about.
  useEffect(() => {
    if (state?.status !== 'pending') return
    const t = setInterval(async () => {
      try {
        const s = await api.kimiLoginPoll()
        setState(s)
        if (s.status === 'connected') toast.success(s.message ?? 'Kimi 账号已连接')
      } catch {
        /* the helper pod may already be gone; the next poll settles it */
      }
    }, 3000)
    return () => clearInterval(t)
  }, [state?.status, toast])

  return (
    <div className="card">
      <h3>Kimi 账号</h3>
      <p className="hint" style={{ marginTop: 0 }}>
        连一次账号,之后所有会话都用它——不用再管 API key。授权在你自己的浏览器里完成。
      </p>
      {state?.status === 'pending' && state.url ? (
        <div className="note" style={{ marginTop: 10 }}>
          <p style={{ margin: '0 0 6px' }}>
            打开这个链接并确认设备码 <b className="mono">{state.code}</b>:
          </p>
          <a href={state.url} target="_blank" rel="noreferrer" className="mono">
            {state.url}
          </a>
          <p className="hint" style={{ marginBottom: 0 }}>批准之后这里会自动变成「已连接」。</p>
        </div>
      ) : state?.status === 'connected' ? (
        <div className="row" style={{ marginTop: 10 }}>
          <Tag>已连接</Tag>
          <button
            className="ghost"
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              try {
                const s = await api.kimiDisconnect()
                setState(s)
                toast.success(s.message ?? '已断开')
              } catch (e) {
                toast.error((e as Error).message)
              } finally {
                setBusy(false)
              }
            }}
          >
            断开
          </button>
        </div>
      ) : (
        <div className="row" style={{ marginTop: 10 }}>
          <button
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              try {
                setState(await api.kimiLoginStart())
              } catch (e) {
                toast.error((e as Error).message)
              } finally {
                setBusy(false)
              }
            }}
          >
            连接 Kimi 账号
          </button>
          {state?.message && <span className="faint">{state.message}</span>}
        </div>
      )}
    </div>
  )
}

/**
 * Your own GitLab (or GitHub) token.
 *
 * Until now a project could only use a forge credential somebody had created
 * by hand as a Kubernetes Secret, which meant onboarding a colleague ended
 * with "ask an administrator to make you one". Bound here, it shows up in
 * the credential picker on the project forms as your own.
 *
 * It is never shared and never lent. A commit pushed with somebody else's
 * token is that person's commit as far as GitLab is concerned, and an audit
 * trail that attributes work to the wrong human is worse than none.
 */
function ForgeToken() {
  const qc = useQueryClient()
  const toast = useToast()
  const [provider, setProvider] = useState('gitlab')
  const [username, setUsername] = useState('')
  const [token, setToken] = useState('')

  const bound = useQuery({ queryKey: ['git-identities'], queryFn: api.gitIdentities })

  const bind = useMutation({
    mutationFn: () => api.bindGitIdentity(provider, username.trim(), token.trim()),
    onSuccess: () => {
      setUsername('')
      setToken('')
      toast.success('令牌已绑定,新建项目时可以直接选它')
      qc.invalidateQueries({ queryKey: ['git-identities'] })
      // The project forms list credentials; a freshly bound one has to
      // appear there without a reload, or it reads as not having worked.
      qc.invalidateQueries({ queryKey: ['new-project-options'] })
    },
    onError: (e) => toast.error((e as Error).message),
  })
  const unbind = useMutation({
    mutationFn: (p: string) => api.unbindGitIdentity(p),
    onSuccess: () => {
      toast.success('已解绑')
      qc.invalidateQueries({ queryKey: ['git-identities'] })
      qc.invalidateQueries({ queryKey: ['new-project-options'] })
    },
    onError: (e) => toast.error((e as Error).message),
  })

  const list = bound.data?.identities ?? []

  return (
    <div className="card">
      <h3>我的代码仓库令牌</h3>
      <p className="hint" style={{ marginTop: 0 }}>
        绑定后,新建项目和关联仓库时可以直接选「我的令牌」,不用再找管理员建凭据。
        令牌只属于你 —— 它推出去的提交,在 GitLab 那边算你的。
      </p>

      {list.length > 0 && (
        <table className="grid" style={{ marginTop: 8, marginBottom: 12 }}>
          <thead>
            <tr>
              <th>平台</th>
              <th>用户名</th>
              <th>凭据名</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {list.map((g) => (
              <tr key={g.provider}>
                <td>
                  <Tag>{g.provider}</Tag>
                </td>
                <td>{g.username}</td>
                <td className="mono faint">{g.secretName}</td>
                <td style={{ textAlign: 'right' }}>
                  <button
                    className="ghost small"
                    disabled={unbind.isPending}
                    onClick={() => unbind.mutate(g.provider)}
                  >
                    解绑
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <div className="row" style={{ flexWrap: 'wrap' }}>
        <select value={provider} onChange={(e) => setProvider(e.target.value)}>
          <option value="gitlab">GitLab</option>
          <option value="github">GitHub</option>
        </select>
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
        <button disabled={!username.trim() || !token.trim() || bind.isPending} onClick={() => bind.mutate()}>
          {bind.isPending ? '保存中…' : list.some((g) => g.provider === provider) ? '替换' : '绑定'}
        </button>
      </div>
    </div>
  )
}

/**
 * 借出凭据 — hand a colleague a long-lived key so they can start working.
 *
 * A new colleague cannot do anything at all until they can pay for a turn:
 * dispatch refuses before it even looks at the project. "Add your own key" and
 * "connect your own account" are both fine answers and both a wall on
 * somebody's first afternoon.
 *
 * A connected OAuth account is deliberately not lendable — its refresh token
 * rotates, so two people holding it knock each other offline. The server
 * refuses and says why; this list simply never offers one.
 */
function Lending() {
  const qc = useQueryClient()
  const toast = useToast()
  const [credential, setCredential] = useState('')
  const [email, setEmail] = useState('')

  const grants = useQuery({ queryKey: ['grants'], queryFn: api.grants })
  const mine = grants.data?.lendable ?? []
  const chosen = mine.find((c) => c.name === credential) ?? mine[0]
  // Sharing a rotating login is a different decision from lending a static
  // key, so it gets a different button and a sentence rather than a silent
  // success.
  const isAccount = chosen?.kind === 'kimi-oauth'

  const done = () => {
    qc.invalidateQueries({ queryKey: ['grants'] })
    qc.invalidateQueries({ queryKey: ['credentials'] })
  }
  const lend = useMutation({
    mutationFn: () => api.lendCredential(chosen?.name ?? '', email.trim(), isAccount),
    onSuccess: () => {
      setEmail('')
      toast.success('借出了。对方现在可以派工了')
      done()
    },
    onError: (e) => toast.error((e as Error).message),
  })
  const revoke = useMutation({
    mutationFn: (v: { credential: string; who: string }) => api.revokeGrant(v.credential, v.who),
    onSuccess: () => {
      toast.success('已收回')
      done()
    },
    onError: (e) => toast.error((e as Error).message),
  })

  const lent = grants.data?.lent ?? []
  const borrowed = grants.data?.borrowed ?? []

  return (
    <div className="card">
      <h3>借出凭据</h3>
      <p className="hint" style={{ marginTop: 0 }}>
        把你的一把 key 借给同事,他就能在项目里派工——花的是<b>你的</b>额度,所以借给谁是件要想一下的事。
        随时可以收回。
      </p>

      {borrowed.length > 0 && (
        <div className="note" style={{ marginTop: 10 }}>
          <b>别人借给你的:</b>
          {borrowed.map((b) => (
            <div key={b.credential} className="mono" style={{ marginTop: 4 }}>
              {b.credential} {b.hint && <span className="faint">{b.hint}</span>}
              <span className="faint"> —— 来自 {b.from}</span>
            </div>
          ))}
        </div>
      )}

      {lent.length > 0 && (
        <table className="grid" style={{ marginTop: 12 }}>
          <thead>
            <tr>
              <th>key</th>
              <th>借给了</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {lent.map((g) => (
              <tr key={g.credential + g.email}>
                <td className="mono">{g.credential}</td>
                <td>
                  {g.name ? `${g.name} ` : ''}
                  <span className="mono faint">{g.email}</span>
                  {g.unknown && <span className="faint"> (查无此人)</span>}
                </td>
                <td style={{ textAlign: 'right' }}>
                  <button
                    className="ghost small"
                    disabled={revoke.isPending}
                    onClick={() => revoke.mutate({ credential: g.credential, who: g.email })}
                  >
                    收回
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {mine.length === 0 ? (
        <p className="hint">你还没有可以借出去的 key。上面先加一把。</p>
      ) : (
        <div className="row" style={{ marginTop: 12, flexWrap: 'wrap' }}>
          <select value={chosen?.name ?? ''} onChange={(e) => setCredential(e.target.value)}>
            {mine.map((c) => (
              <option key={c.name} value={c.name}>
                {c.kind === 'kimi-oauth' ? `${c.name}(已连接的账号)` : `${c.name} ${c.hint ?? ''}`}
              </option>
            ))}
          </select>
          <input
            type="email"
            placeholder="借给谁(邮箱)"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            style={{ minWidth: 240 }}
          />
          <button
            disabled={!email.trim() || lend.isPending}
            onClick={() => lend.mutate()}
          >
            {lend.isPending ? '借出中…' : isAccount ? '共享这个账号' : '借出'}
          </button>
        </div>
      )}
      {/* Said here rather than only in the server's refusal: somebody looking
          for a way to share their Kimi login should find the answer before
          they try it, not after. */}
      {isAccount ? (
        <p className="hint">
          借的是<b>已连接的账号</b>:对方名下会放一份拷贝(会话控制器只认按自己名字命名的那一份,
          光记一笔账等于没借)。两边之后各自刷新、各走各的令牌链 —— 2026-08-19 实测过,
          Kimi 换发新令牌<b>不会作废旧的</b>,所以这不影响使用。借出去的那份记着是谁借的,
          万一哪天真有人被登出,原因能一眼看出来。
        </p>
      ) : (
        <p className="hint">
          静态 API key 每次都是同一个字符串,借出去不会互相影响。
        </p>
      )}
    </div>
  )
}
