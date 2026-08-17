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

      <div className="card">
        <h3>已有凭据</h3>
        {(creds ?? []).length === 0 ? (
          <EmptyState
            title="还没有凭据"
            hint="加一把模型 API key,派工时选它。key 只进不出——存进去之后谁也读不回来,包括你自己。"
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
