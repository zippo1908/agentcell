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

  // --- @ ---------------------------------------------------------------
  //
  // Mentions were addressed by hashed user id, which nobody types, so in
  // practice nobody was ever addressed: the feature was unreachable rather
  // than broken. The picker is what makes it real — and what it inserts is
  // deliberately something a person could also have typed and can read back
  // in the message afterwards.
  const members = useQuery({
    queryKey: ['members', team],
    queryFn: () => api.members(team),
    enabled: !!team,
  })
  // An open project has no member list, so fall back to everyone on the
  // deployment. An empty picker on the commonest state would read as broken.
  const everyone = useQuery({
    queryKey: ['people'],
    queryFn: api.people,
    enabled: !!members.data?.open,
  })

  const [mention, setMention] = useState<{ start: number; query: string } | null>(null)
  const [pick, setPick] = useState(0)

  function refreshMention(value: string, caret: number) {
    const upto = value.slice(0, caret)
    const at = upto.lastIndexOf('@')
    // An @ only opens the picker at a word boundary — otherwise every email
    // address anybody pastes would open it — and it closes at whitespace.
    if (at < 0 || (at > 0 && /\S/.test(upto[at - 1])) || /\s/.test(upto.slice(at + 1))) {
      setMention(null)
      return
    }
    setMention({ start: at, query: upto.slice(at + 1) })
    setPick(0)
  }

  const people = members.data?.open
    ? (everyone.data ?? []).map((p) => ({ email: p.email, name: p.name }))
    : (members.data?.members ?? []).filter((m) => !m.unknown).map((m) => ({ email: m.email, name: m.name }))

  // What gets inserted: the local part normally, the whole address when two
  // colleagues share one. The server resolves it the same way, and refuses to
  // guess when it is ambiguous — so inserting the short form there would
  // silently reach nobody.
  const shared = new Set<string>()
  const seenLocal = new Set<string>()
  for (const p of people) {
    const local = p.email.split('@')[0].toLowerCase()
    if (seenLocal.has(local)) shared.add(local)
    seenLocal.add(local)
  }

  const q = (mention?.query ?? '').toLowerCase()
  const candidates = [
    { token: '机器人', label: '机器人', sub: '把这件事派给这个项目的 agent' },
    ...people.map((p) => {
      const local = p.email.split('@')[0]
      return {
        token: shared.has(local.toLowerCase()) ? p.email : local,
        label: p.name || local,
        sub: p.email,
      }
    }),
  ]
    .filter((c) => !q || `${c.label} ${c.sub} ${c.token}`.toLowerCase().includes(q))
    .slice(0, 8)

  const menuOpen = mention !== null && candidates.length > 0
  const taRef = useRef<HTMLTextAreaElement>(null)

  function accept(c: { token: string }) {
    if (!mention) return
    const ta = taRef.current
    const caret = ta?.selectionStart ?? text.length
    const inserted = '@' + c.token + ' '
    setText(text.slice(0, mention.start) + inserted + text.slice(caret))
    setMention(null)
    // Put the caret after what was just inserted, on the next frame — before
    // React has re-rendered, setSelectionRange would act on the old value.
    const pos = mention.start + inserted.length
    requestAnimationFrame(() => {
      ta?.focus()
      ta?.setSelectionRange(pos, pos)
    })
  }

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
        {menuOpen && (
          <div className="mention-menu" role="listbox">
            {candidates.map((c, i) => (
              <button
                key={c.token}
                type="button"
                role="option"
                aria-selected={i === pick}
                className={`mention-item ${i === pick ? 'on' : ''}`}
                // mousedown, not click: click fires after the textarea has
                // already lost focus and the caret position with it.
                onMouseDown={(e) => {
                  e.preventDefault()
                  accept(c)
                }}
                onMouseEnter={() => setPick(i)}
              >
                <span className="mention-name">{c.label}</span>
                <span className="mention-sub">{c.sub}</span>
              </button>
            ))}
          </div>
        )}
        <textarea
          ref={taRef}
          value={text}
          rows={2}
          placeholder="@工作区 说要做什么;输入 @ 选人,或 @机器人 让 agent 接单"
          onChange={(e) => {
            setText(e.target.value)
            refreshMention(e.target.value, e.target.selectionStart ?? 0)
          }}
          onClick={(e) => refreshMention(text, e.currentTarget.selectionStart ?? 0)}
          onBlur={() => setMention(null)}
          onKeyDown={(e) => {
            // While the picker is up it owns the arrows and the Enter key —
            // otherwise choosing somebody would send the half-typed message.
            if (menuOpen) {
              if (e.key === 'ArrowDown') {
                e.preventDefault()
                setPick((p) => (p + 1) % candidates.length)
                return
              }
              if (e.key === 'ArrowUp') {
                e.preventDefault()
                setPick((p) => (p - 1 + candidates.length) % candidates.length)
                return
              }
              if (e.key === 'Enter' || e.key === 'Tab') {
                e.preventDefault()
                accept(candidates[pick])
                return
              }
              if (e.key === 'Escape') {
                e.preventDefault()
                setMention(null)
                return
              }
            }
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
