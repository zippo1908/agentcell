import { useState } from 'react'
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
