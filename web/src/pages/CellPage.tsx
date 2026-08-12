import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import { DispatchForm } from '../components/DispatchForm'
import { PreviewPane } from '../components/PreviewPane'
import { SessionList } from '../components/SessionList'
import { Badge, Confirm, Defs, Spinner, useToast } from '../ui/primitives'
import { NONE, cellTone } from '../lib/format'

type Tab = 'overview' | 'sessions' | 'preview' | 'settings'

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
  if (!hasPreview) return { tone: 'amber' as const, text: '没有配置预览命令,所以看不到跑起来的产品。在设置里补一条即可。' }
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
          <button className="small" disabled={!cell.previewPath} onClick={() => setTab('preview')}>
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
        <button className={tab === 'preview' ? 'active' : ''} onClick={() => setTab('preview')}>
          预览
        </button>
        <button className={tab === 'settings' ? 'active' : ''} onClick={() => setTab('settings')}>
          设置
        </button>
      </div>

      {tab === 'overview' && (
        <>
          <div className="card">
            <h3>下一步</h3>
            <div className="row" style={{ alignItems: 'flex-start', gap: 8 }}>
              <Badge tone={step.tone}>{cell.phase || 'Unknown'}</Badge>
            </div>
            <p style={{ margin: '10px 0 0', fontSize: 13 }}>{step.text}</p>
          </div>
          <div className="card">
            <h3>派工</h3>
            <DispatchForm cell={cell.name} description={cell.description} />
          </div>
          <div className="card">
            <h3>事实</h3>
            <Defs
              items={[
                ['状态', cell.phase || NONE],
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

      {tab === 'preview' && (
        <div className="card">
          <h3>预览</h3>
          <PreviewPane cell={cell} onRelease={() => setReleasing(true)} releasing={release.isPending} />
        </div>
      )}

      {tab === 'settings' && (
        <div className="card">
          <h3>产品描述</h3>
          <p className="hint" style={{ marginTop: 0 }}>
            每次派工都会带给 agent。这是你「边看预览边校准」的地方。
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
