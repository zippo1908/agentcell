import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import { Tag, useToast } from '../ui/primitives'

/**
 * Who is on this deployment, and how somebody new gets in.
 *
 * There is no self-registration: an account here comes with a shell inside
 * the cluster, so somebody already inside has to hand it over deliberately.
 * The invitation is a one-time link — the platform cannot mail it (an
 * internal deployment has no mail server it may assume), so whoever invites
 * passes it on, and a lost link is replaced rather than looked up.
 */
export function PeoplePage() {
  const qc = useQueryClient()
  const toast = useToast()
  const [email, setEmail] = useState('')
  const [name, setName] = useState('')
  const [admin, setAdmin] = useState(false)
  const [link, setLink] = useState('')

  const me = useQuery({ queryKey: ['me'], queryFn: api.me })
  const people = useQuery({ queryKey: ['people'], queryFn: api.people })

  const invite = useMutation({
    mutationFn: () => api.createInvite(email.trim(), name.trim(), admin),
    onSuccess: (r) => {
      // Shown once, and only here. Nothing stores the token: a copy sitting
      // in the database would be a second way into the platform.
      setLink(location.origin + r.path)
      setEmail('')
      setName('')
      setAdmin(false)
      qc.invalidateQueries({ queryKey: ['people'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <>
      <h1 className="page-title">人员</h1>

      {me.data?.admin && (
        <div className="card">
          <h3>邀请一个人</h3>
          <p className="hint" style={{ marginTop: 0 }}>
            这个平台交给账号持有者的是集群里的一个 shell,所以没有自助注册。链接 7 天内有效、只能用一次。
          </p>
          <div className="row" style={{ marginTop: 10, flexWrap: 'wrap' }}>
            <input
              type="email"
              placeholder="邮箱"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              style={{ minWidth: 240 }}
            />
            <input
              placeholder="名字(可留空)"
              value={name}
              onChange={(e) => setName(e.target.value)}
              style={{ minWidth: 160 }}
            />
            <label className="row" style={{ gap: 6 }}>
              <input type="checkbox" checked={admin} onChange={(e) => setAdmin(e.target.checked)} />
              <span className="hint" style={{ margin: 0 }}>设为管理员</span>
            </label>
            <button disabled={!email.trim() || invite.isPending} onClick={() => invite.mutate()}>
              {invite.isPending ? '生成中…' : '生成邀请链接'}
            </button>
          </div>

          {link && (
            <div className="note" style={{ marginTop: 12 }}>
              <p style={{ margin: '0 0 6px' }}>把这个链接发给他 —— 只显示这一次:</p>
              <code className="mono" style={{ wordBreak: 'break-all' }}>{link}</code>
              <div className="row" style={{ marginTop: 8 }}>
                <button
                  className="ghost small"
                  onClick={() => {
                    navigator.clipboard?.writeText(link)
                    toast.success('已复制')
                  }}
                >
                  复制
                </button>
              </div>
            </div>
          )}
        </div>
      )}

      <div className="card">
        <h3>已有账号</h3>
        <table className="grid" style={{ marginTop: 8 }}>
          <thead>
            <tr>
              <th>邮箱</th>
              <th>名字</th>
              <th>角色</th>
            </tr>
          </thead>
          <tbody>
            {(people.data ?? []).map((p) => (
              <tr key={p.email}>
                <td className="mono">{p.email}</td>
                <td>{p.name || '—'}</td>
                <td>
                  {p.admin && <Tag>管理员</Tag>}
                  {p.disabled && <Tag>已停用</Tag>}
                  {!p.admin && !p.disabled && <span className="faint">成员</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {(people.data ?? []).length === 0 && (
          <p className="hint">还没有别人。上面邀请一个。</p>
        )}
      </div>
    </>
  )
}
