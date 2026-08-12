import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'

/** Dispatch a task to a slot. Remembers the last runner/provider/cred. */
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
  const [cred, setCred] = useState(localStorage.getItem('ac.cred') ?? '')
  const [follow, setFollow] = useState(true)

  const dispatch = useMutation({
    mutationFn: () =>
      api.dispatch(cell, {
        task,
        runner,
        provider,
        model,
        credentialSecret: cred,
        followPreview: follow,
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

  const providers = meta?.providers ?? []
  const runners = meta?.runners ?? []

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
        <select value={runner} onChange={(e) => setRunner(e.target.value)}>
          {runners.map((r) => (
            <option key={r}>{r}</option>
          ))}
        </select>
        <select value={provider} onChange={(e) => setProvider(e.target.value)}>
          <option value="">选择 provider…</option>
          {providers.map((p) => (
            <option key={p}>{p}</option>
          ))}
        </select>
      </div>
      <div className="row">
        <input
          value={model}
          placeholder="model(可留空)"
          onChange={(e) => setModel(e.target.value)}
        />
        <input
          value={cred}
          placeholder="凭据 Secret 名"
          onChange={(e) => setCred(e.target.value)}
        />
      </div>
      <div className="row tight">
        <label className="chk">
          <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />
          预览跟随这单
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
