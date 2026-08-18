import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { api } from '../api/client'
import { Confirm, useToast } from '../ui/primitives'

/**
 * Stop, restart, end — the three things somebody needs when a session is
 * not doing what they want.
 *
 * Until now the console offered exactly one of them, and it was the
 * destructive one: 结束并清算 throws away the worktree and the conversation.
 * So "this terminal is stuck" and "this agent is looping on nothing" both
 * had to be answered by ending the work, or by waiting. They are different
 * problems and they get different buttons:
 *
 *   停止   — hand the compute back, keep everything else. Say anything and
 *            it comes back where it was.
 *   重启   — throw away the runtime it lives in. For a wedged terminal or a
 *            CLI that has tied itself in a knot. Interrupts what the agent
 *            is doing right now, which is why it asks.
 *   结束   — really finish: settle the work and let the session go.
 *
 * The two that lose something ask first, and the question says what is
 * lost — not "确定吗?", which tells nobody anything.
 */
export function SessionControls({
  session,
  phase,
  onDone,
}: {
  session: string
  phase: string
  onDone: () => void
}) {
  const toast = useToast()
  const [ask, setAsk] = useState<'restart' | 'settle' | null>(null)

  // Spelled out rather than built by a helper: these are hooks, and a
  // helper that calls them reads like something you could call in a branch.
  const said = (fallback: string) => ({
    onSuccess: (r: unknown) => {
      toast.success((r as { message?: string })?.message ?? fallback)
      onDone()
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const sleep = useMutation({ mutationFn: () => api.sleepSession(session), ...said('已停止') })
  const restart = useMutation({ mutationFn: () => api.restartRuntime(session), ...said('运行时正在重建') })
  const settle = useMutation({ mutationFn: () => api.settleSession(session), ...said('正在清算') })

  const busy = sleep.isPending || restart.isPending || settle.isPending
  const dormant = phase === 'Dormant'

  return (
    <>
      <div className="row" style={{ gap: 6 }}>
        <button
          className="ghost small"
          disabled={busy || dormant}
          title={dormant ? '已经停着了' : '交回算力,worktree 和对话都留着'}
          onClick={() => sleep.mutate()}
        >
          停止
        </button>
        <button
          className="ghost small"
          disabled={busy}
          title="重建运行时——会打断 agent 手上正在做的事"
          onClick={() => setAsk('restart')}
        >
          重启
        </button>
        <button
          className="ghost small danger"
          disabled={busy}
          title="结束这条会话并清算"
          onClick={() => setAsk('settle')}
        >
          结束
        </button>
      </div>

      {ask === 'restart' && (
        <Confirm
          title="重启这条会话的运行时?"
          body={
            <>
              agent 手上正在做的那一步<b>会被打断</b>。worktree、已提交的东西和这条对话都在卷上,
              重建完接着说就能继续。
            </>
          }
          confirmText="重启"
          onConfirm={() => {
            setAsk(null)
            restart.mutate()
          }}
          onCancel={() => setAsk(null)}
        />
      )}
      {ask === 'settle' && (
        <Confirm
          title="结束这条会话?"
          body={
            <>
              会清算这一单:该提的提上去,然后这条会话和它的 worktree 就没了。
              <br />
              只是想让它先停一下、之后再回来的话,用<b>「停止」</b>。
            </>
          }
          confirmText="结束并清算"
          onConfirm={() => {
            setAsk(null)
            settle.mutate()
          }}
          onCancel={() => setAsk(null)}
        />
      )}
    </>
  )
}
