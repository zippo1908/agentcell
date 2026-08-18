import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import type { Post } from '../api/types'
import { Spinner, useToast } from '../ui/primitives'

/**
 * The board — where work is asked for and where it comes back.
 *
 * The console used to have a page per noun and nowhere to stand: a dashboard
 * counting three numbers, and the dispatch form two tabs deep inside a
 * project. You could operate the whole system without ever seeing what was
 * happening in it.
 *
 * Here you type `@cell 把商品卡片改成两列` and the agent answers in the same
 * stream — first that it took the job, then what came of it, with a link to
 * the diff. Asking and answering are one conversation.
 */
export function BoardPage() {
  const qc = useQueryClient()
  const toast = useToast()
  // The board belongs to a PROJECT. There is no team layer: whoever may
  // work on the project is whoever may see the conversation about it.
  const [team, setTeam] = useState<string>('')
  const [text, setText] = useState('')
  const endRef = useRef<HTMLDivElement>(null)

  const teams = useQuery({ queryKey: ['cells'], queryFn: () => api.cells() })
  useEffect(() => {
    if (!team && teams.data?.length) setTeam(teams.data[0].name)
  }, [teams.data, team])

  const board = useQuery({
    queryKey: ['board', team],
    queryFn: () => api.board(team),
    enabled: !!team,
    // An agent answering takes seconds to minutes; polling is the honest
    // shape for that and costs one small request.
    refetchInterval: 4000,
  })

  const post = useMutation({
    mutationFn: () => api.postToBoard(team, text),
    onSuccess: () => {
      setText('')
      qc.invalidateQueries({ queryKey: ['board', team] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const posts = board.data?.posts ?? []
  useEffect(() => {
    endRef.current?.scrollIntoView({ block: 'end' })
  }, [posts.length])

  if (teams.isLoading) return <Spinner />

  if (!teams.data?.length) {
    return (
      <div className="stack">
        <div className="page-head">
          <h2>黑板</h2>
        </div>
        <div className="card">
          <p className="hint" style={{ margin: 0 }}>
            黑板挂在项目上。先<Link to="/cells/new"> 建一个项目 </Link>
            ,之后在这里说话就是对它的 agent 说 —— 不用点名,一个项目一块黑板。
          </p>
        </div>
      </div>
    )
  }

  return (
    <div className="board-page">
      <div className="page-head board-head">
        <h2>黑板</h2>
        {teams.data.length > 1 ? (
          <select value={team} onChange={(e) => setTeam(e.target.value)} style={{ width: 200 }}>
            {teams.data.map((t) => (
              <option key={t.name} value={t.name}>
                {t.name}
              </option>
            ))}
          </select>
        ) : (
          <span className="faint">{teams.data[0].name}</span>
        )}
      </div>

      <div className="board-stream">
        {posts.length === 0 && (
          <div className="board-empty">
            <p>还没有人说话。</p>
            <p className="hint">
              试试 <code className="mono">@工作区名 把商品卡片改成两列</code> —— agent 会接单,
              做完在这里回你,附上分支和 diff。
            </p>
          </div>
        )}
        {posts.map((p) => (
          <PostRow key={p.id} post={p} />
        ))}
        <div ref={endRef} />
      </div>

      <div className="board-composer">
        <textarea
          value={text}
          rows={2}
          placeholder="@工作区 说要做什么;@某人 提醒他看一眼"
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            // Enter sends, Shift+Enter is a newline: this is a conversation,
            // not a form.
            if (e.key === 'Enter' && !e.shiftKey && text.trim() && !post.isPending) {
              e.preventDefault()
              post.mutate()
            }
          }}
        />
        <button disabled={!text.trim() || post.isPending} onClick={() => post.mutate()}>
          发出
        </button>
      </div>
    </div>
  )
}

function PostRow({ post }: { post: Post }) {
  const isAgent = post.kind === 'agent'
  const isSystem = post.kind === 'system'
  return (
    <div className={`post ${post.mine ? 'mine' : ''} ${isSystem ? 'system' : ''}`}>
      <div className={`post-mark ${isAgent ? 'agent' : isSystem ? 'sys' : 'user'}`}>
        {isAgent ? '◆' : isSystem ? '!' : post.author.slice(2, 4)}
      </div>
      <div className="post-body">
        <div className="post-meta">
          <span className="post-author">
            {isAgent ? post.cell : isSystem ? 'AgentCell' : post.author}
          </span>
          <span className="faint">{post.at}</span>
          {post.session && (
            <Link className="post-link" to={`/cells/${post.cell}`}>
              看这一单 →
            </Link>
          )}
        </div>
        <div className="post-text">{post.body}</div>
      </div>
    </div>
  )
}
