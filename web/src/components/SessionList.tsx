import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import type { Session } from '../api/types'
import { Badge, Confirm, EmptyState, Spinner, Tag, useToast } from '../ui/primitives'
import { NONE, ago, sessionTone } from '../lib/format'

/**
 * A session row, and — for a resident one — the controls that make it worth
 * keeping open: is it still working, say one more thing, how to attach.
 *
 * Liveness is asked of the tmux WINDOW, not the pod: a runtime can be up
 * while this session's window is gone, and reporting the pod would call that
 * running.
 */
function ResidentControls({ session }: { session: Session }) {
  const qc = useQueryClient()
  const toast = useToast()
  const [text, setText] = useState('')
  const { data: state } = useQuery({
    queryKey: ['session-state', session.name],
    queryFn: () => api.sessionState(session.name),
    refetchInterval: 5000,
    retry: false,
  })

  const cont = useMutation({
    mutationFn: () => api.continueSession(session.name, text),
    onSuccess: () => {
      setText('')
      toast.success('已发给这个会话')
      qc.invalidateQueries({ queryKey: ['session-state', session.name] })
    },
    onError: (e) => toast.error((e as Error).message),
  })

  if (!state?.resident) return null

  return (
    <div style={{ marginTop: 10, borderLeft: '2px solid var(--border)', paddingLeft: 12 }}>
      <div className="row tight" style={{ marginTop: 0 }}>
        {!state.live ? (
          <Badge tone="gray">窗口已关闭</Badge>
        ) : state.working ? (
          <Badge tone="amber">agent 工作中</Badge>
        ) : (
          <Badge tone="green">等你说下一句{state.exitCode && state.exitCode !== '0' ? `(上次退出码 ${state.exitCode})` : ''}</Badge>
        )}
      </div>
      {state.live && (
        <>
          <div className="row" style={{ marginTop: 8 }}>
            <input
              value={text}
              placeholder="再补一句 —— 会接着同一个对话,而不是新开一个"
              onChange={(e) => setText(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey && text.trim() && !cont.isPending) {
                  e.preventDefault()
                  cont.mutate()
                }
              }}
            />
            <button className="small" disabled={!text.trim() || cont.isPending} onClick={() => cont.mutate()}>
              {cont.isPending ? '发送中…' : '发送'}
            </button>
          </div>
          <details className="advanced" style={{ marginTop: 8, padding: '8px 12px' }}>
            <summary>接入这个终端</summary>
            <pre className="machine" style={{ marginTop: 8 }}>
              {state.attach}
            </pre>
            <div className="hint">
              这是一个真终端:attach 进去之后,这个 CLI 自己的 /命令 和你本地用完全一样。
            </div>
          </details>
        </>
      )}
    </div>
  )
}

export function SessionList({ sessions, cell }: { sessions: Session[]; cell: string }) {
  const qc = useQueryClient()
  const toast = useToast()
  const [confirming, setConfirming] = useState<Session | null>(null)

  const settle = useMutation({
    mutationFn: (name: string) => api.settleSession(name),
    onSuccess: () => {
      toast.success('已开始清算:提交、推送,然后进批阅')
      qc.invalidateQueries({ queryKey: ['cell', cell] })
    },
    onError: (e) => toast.error((e as Error).message),
  })

  if (sessions.length === 0) {
    return (
      <EmptyState
        title="还没有会话"
        hint="派一单工作给 agent。勾上「常驻会话」的话,agent 跑完槽位还在,你可以看完结果再补一句。"
      />
    )
  }

  return (
    <>
      {sessions.map((s) => (
        <div className="item" key={s.name}>
          <div className="title">{s.task || NONE}</div>
          <div className="sub">
            <Badge tone={sessionTone(s.phase)}>{s.phase || 'Unknown'}</Badge>
            <Tag title="agent CLI">{s.runner}</Tag>
            <Tag title="模型来源">{s.provider}</Tag>
            {s.branch && <code className="mono">{s.branch}</code>}
            <span className="faint">{ago(s.started)}</span>
            <span style={{ flex: 1 }} />
            {(s.phase === 'Running' || s.phase === 'Queued') && (
              <button className="ghost small" onClick={() => setConfirming(s)}>
                结束并清算
              </button>
            )}
          </div>
          {s.message && <div className="hint">{s.message}</div>}
          {(s.phase === 'Running' || s.phase === 'Queued') && <ResidentControls session={s} />}
        </div>
      ))}

      {confirming && (
        <Confirm
          title="结束这个会话?"
          body={
            <>
              会立刻清算 <b>{confirming.task || confirming.name}</b>:把 worktree 里的改动提交、推成分支
              <code className="mono"> session/…</code>,然后进批阅队列。
              <br />
              没有产出的话会被丢弃。<b>工作不会丢</b> —— 推送没确认成功,清算就不会算完成。
            </>
          }
          confirmText="结束并清算"
          onConfirm={() => {
            settle.mutate(confirming.name)
            setConfirming(null)
          }}
          onCancel={() => setConfirming(null)}
        />
      )}
      {settle.isPending && (
        <div className="hint" style={{ marginTop: 8 }}>
          <Spinner size={12} /> 清算中…
        </div>
      )}
    </>
  )
}
