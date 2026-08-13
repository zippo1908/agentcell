import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'

const CUSTOM = '__custom__'

/**
 * Dispatch a task to a slot.
 *
 * The form is driven by the server's catalogue rather than two flat lists:
 * picking a runner narrows the providers to the ones it can actually drive
 * and selects a sensible default, and picking a provider offers its models.
 * The old form let you choose a pairing the API would then refuse, and left
 * the model as an empty box you were expected to already know the answer to.
 *
 * Models stay free-text on purpose. Providers ship new ones far faster than
 * our preset table is updated, so the list is a starting point and never a
 * closed set.
 */
export function DispatchForm({ cell, description }: { cell: string; description: string }) {
  const qc = useQueryClient()
  const { data: meta } = useQuery({
    queryKey: ['meta'],
    queryFn: api.meta,
    refetchInterval: false,
    staleTime: Infinity,
  })

  const [task, setTask] = useState('')
  const [runner, setRunner] = useState(localStorage.getItem('ac.runner') ?? 'claude')
  const [provider, setProvider] = useState(localStorage.getItem('ac.provider') ?? '')
  const [model, setModel] = useState(localStorage.getItem('ac.model') ?? '')
  const [customModel, setCustomModel] = useState(false)
  const [cred, setCred] = useState(localStorage.getItem('ac.cred') ?? '')
  const [follow, setFollow] = useState(true)
  const [resident, setResident] = useState(true)
  // Hours, because the unit people think in for "leave this open" is not
  // seconds. Empty means the default: 2h idle for a resident slot, 1h age
  // for a one-shot.
  const [ttlHours, setTtlHours] = useState('')

  const runners = meta?.runners ?? []
  const providers = meta?.providers ?? []
  const current = useMemo(() => runners.find((r) => r.name === runner), [runners, runner])
  const compatible = useMemo(
    () => providers.filter((p) => current?.providers.includes(p.name)),
    [providers, current],
  )
  const chosen = useMemo(() => providers.find((p) => p.name === provider), [providers, provider])

  // Keep the selection valid: a provider the current runner cannot drive is
  // replaced by its default rather than silently submitted and refused.
  useEffect(() => {
    if (!current) return
    if (!provider || !current.providers.includes(provider)) {
      setProvider(current.defaultProvider ?? current.providers[0] ?? '')
    }
  }, [current, provider])

  // Same for the model: keep it if the new provider also lists it, otherwise
  // fall back to that provider's first, and never strand a typed-in value.
  useEffect(() => {
    if (!chosen || customModel) return
    const models = chosen.models ?? []
    if (models.length && !models.includes(model)) setModel(models[0])
  }, [chosen, customModel, model])

  const crossVendor =
    !!current?.vendor && !!chosen?.vendor && current.vendor !== chosen.vendor

  const dispatch = useMutation({
    mutationFn: () =>
      api.dispatch(cell, {
        task,
        runner,
        provider,
        model,
        credentialSecret: cred,
        followPreview: follow,
        resident,
        ttlSeconds: ttlHours ? Math.round(Number(ttlHours) * 3600) : undefined,
      }),
    onSuccess: () => {
      localStorage.setItem('ac.runner', runner)
      localStorage.setItem('ac.provider', provider)
      localStorage.setItem('ac.model', model)
      localStorage.setItem('ac.cred', cred)
      setTask('')
      qc.invalidateQueries({ queryKey: ['cell', cell] })
    },
  })

  return (
    <div className="card">
      <h2>派工</h2>
      <textarea
        rows={3}
        value={task}
        placeholder="这一单要 agent 做什么"
        onChange={(e) => setTask(e.target.value)}
      />
      <div className="row">
        <select value={runner} onChange={(e) => setRunner(e.target.value)} title="agent CLI">
          {runners.map((r) => (
            <option key={r.name} value={r.name}>
              {r.display}
            </option>
          ))}
        </select>
        <select
          value={provider}
          onChange={(e) => setProvider(e.target.value)}
          title="只列出这个 runner 能驱动的 provider"
        >
          {compatible.length === 0 && <option value="">没有兼容的 provider</option>}
          {compatible.map((p) => (
            <option key={p.name} value={p.name}>
              {p.display}
              {p.region === 'cn' ? ' · 境内' : ''}
            </option>
          ))}
        </select>
      </div>
      <div className="row">
        {customModel || !(chosen?.models?.length ?? 0) ? (
          <input
            value={model}
            placeholder="model(可留空用 provider 默认)"
            onChange={(e) => setModel(e.target.value)}
          />
        ) : (
          <select
            value={model}
            onChange={(e) => {
              if (e.target.value === CUSTOM) {
                setCustomModel(true)
                setModel('')
              } else setModel(e.target.value)
            }}
          >
            {chosen?.models?.map((m) => (
              <option key={m}>{m}</option>
            ))}
            <option value={CUSTOM}>其他(自己填)…</option>
          </select>
        )}
        <input
          value={cred}
          placeholder="凭据 Secret 名"
          onChange={(e) => setCred(e.target.value)}
        />
      </div>
      {current && !current.resumable && (
        <div className="note">
          {current.display} 没有声明会话续接:常驻会话里追加的指令会<b>新开一个对话</b>,而不是接着上一个。
        </div>
      )}
      {crossVendor && (
        <div className="note">
          {current?.display}(来自 {current?.vendor})将驱动 {chosen?.vendor} 的模型,走的是对方提供的
          兼容端点。端点就是为此提供的;CLI 的授权条款由 {current?.vendor} 定义,请自行确认。
          {chosen?.docs && (
            <>
              {' '}
              <a href={chosen.docs} target="_blank" rel="noreferrer">
                provider 文档
              </a>
            </>
          )}
        </div>
      )}
      <div className="row tight">
        <label className="chk">
          <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />
          预览跟随这单
        </label>
        <label className="chk" title={resident ? '空闲多久后自动清算;默认 2 小时' : '跑多久后强制清算;默认 1 小时'}>
          <input
            style={{ width: 56 }}
            value={ttlHours}
            placeholder="2"
            onChange={(e) => setTtlHours(e.target.value.replace(/[^\d.]/g, ''))}
          />
          {resident ? '小时空闲后回收' : '小时后强制清算'}
        </label>
        <label className="chk" title="agent 结束后保留槽位,可以接着说">
          <input
            type="checkbox"
            checked={resident}
            onChange={(e) => setResident(e.target.checked)}
          />
          常驻会话
        </label>
        <button
          className="ghost"
          onClick={() => setTask(description)}
          disabled={!description}
          title="把产品描述带入任务"
        >
          用描述填充
        </button>
        <button
          onClick={() => dispatch.mutate()}
          disabled={!task.trim() || !provider || !cred || dispatch.isPending}
        >
          {dispatch.isPending ? '派工中…' : '派工'}
        </button>
      </div>
      {dispatch.error && <div className="err">{(dispatch.error as Error).message}</div>}
    </div>
  )
}
