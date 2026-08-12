import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import { PreviewPane } from '../components/PreviewPane'
import { DispatchForm } from '../components/DispatchForm'
import { SessionList } from '../components/SessionList'

/**
 * The calibration loop: product description and dispatch on the left, the
 * resident live preview on the right — you steer against what the agent is
 * actually building.
 */
export function CellPage() {
  const { name = '' } = useParams()
  const qc = useQueryClient()
  const { data, error } = useQuery({
    queryKey: ['cell', name],
    queryFn: () => api.cell(name),
    enabled: !!name,
  })

  const [description, setDescription] = useState('')
  const [dirty, setDirty] = useState(false)
  // Track the server value unless the user is mid-edit.
  useEffect(() => {
    if (!dirty && data?.cell.description !== undefined) {
      setDescription(data.cell.description)
    }
  }, [data?.cell.description, dirty])

  const save = useMutation({
    mutationFn: () => api.saveDescription(name, description),
    onSuccess: () => {
      setDirty(false)
      qc.invalidateQueries({ queryKey: ['cell', name] })
    },
  })

  const release = useMutation({
    mutationFn: () => api.release(name),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['cell', name] }),
  })

  if (error) return <main><div className="err">{(error as Error).message}</div></main>
  if (!data) return <main><div className="empty">加载中…</div></main>

  const { cell, sessions } = data

  return (
    <main className="split">
      <div className="col">
        <div className="card">
          <h2>产品描述(边看预览边校准)</h2>
          <textarea
            rows={6}
            value={description}
            placeholder="这个产品是什么、给谁用、当前最重要的差距……"
            onChange={(e) => {
              setDescription(e.target.value)
              setDirty(true)
            }}
          />
          <div className="row tight">
            <button onClick={() => save.mutate()} disabled={!dirty || save.isPending}>
              {save.isPending ? '保存中…' : dirty ? '保存描述' : '已保存'}
            </button>
            <span className="spacer" />
            <span className="status">
              {cell.phase} · 槽位 {cell.activeSessions}/{cell.maxSessions}
            </span>
          </div>
          {save.error && <div className="err">{(save.error as Error).message}</div>}
        </div>

        <DispatchForm cell={name} description={description} />

        <div className="card grow">
          <h2>会话</h2>
          <div className="scroll">
            <SessionList cell={name} sessions={sessions} />
          </div>
        </div>
      </div>

      <div className="col">
        <PreviewPane
          cell={cell}
          onRelease={() => {
            if (confirm('把当前主干发布到正式区?(开发区不受影响)')) release.mutate()
          }}
          releasing={release.isPending}
        />
      </div>
    </main>
  )
}
