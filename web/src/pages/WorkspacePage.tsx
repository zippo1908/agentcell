import { useCallback, useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import type { Branch } from '../api/types'
import { TerminalDeck } from '../components/TerminalDeck'
import { SessionControls } from '../components/SessionControls'
import { Splitter, usePaneWidth } from '../components/Splitter'
import { Badge, Spinner, useToast } from '../ui/primitives'
import { cellTone } from '../lib/format'

/**
 * One screen to work on: projects on the left, the agent's terminal in the
 * middle, the branch tree on the right.
 *
 * The console was a page per noun, so doing anything meant crossing three of
 * them — pick a project, find its session, open a terminal, go elsewhere to
 * see what came out. This is the same information arranged the way the work
 * actually goes: what am I working on, what is it doing, and what has it
 * produced.
 */
export function WorkspacePage() {
  const [cell, setCell] = useState<string>('')
  const [showTree, setShowTree] = useState(true)
  const root = useRef<HTMLDivElement>(null)
  // Both side columns are draggable and both remember their width. The
  // middle takes whatever is left, because the middle is the terminal.
  const [listW, setListW] = usePaneWidth('list', 210, 140, 420)
  const [treeW, setTreeW] = usePaneWidth('tree', 260, 180, 520)

  // -1 from a double-click means "back to the default".
  const dragList = useCallback(
    (x: number) => {
      const left = root.current?.getBoundingClientRect().left ?? 0
      setListW(x < 0 ? 210 : x - left)
    },
    [setListW],
  )
  const dragTree = useCallback(
    (x: number) => {
      const right = root.current?.getBoundingClientRect().right ?? 0
      setTreeW(x < 0 ? 260 : right - x)
    },
    [setTreeW],
  )

  const cells = useQuery({ queryKey: ['cells'], queryFn: () => api.cells(), refetchInterval: 10000 })
  useEffect(() => {
    if (!cell && cells.data?.length) setCell(cells.data[0].name)
  }, [cells.data, cell])

  if (cells.isLoading) return <Spinner />
  if (!cells.data?.length) {
    return (
      <div className="card">
        <p className="hint" style={{ margin: 0 }}>
          还没有项目。<Link to="/cells/new">新建一个</Link>,指一个仓库就行。
        </p>
      </div>
    )
  }

  return (
    <div
      ref={root}
      className={`ws ${showTree ? '' : 'ws-notree'}`}
      style={
        {
          '--ws-list': `${listW}px`,
          '--ws-tree': `${treeW}px`,
        } as React.CSSProperties
      }
    >
      <aside className="ws-list">
        <div className="ws-head">
          <span>项目</span>
          <Link to="/cells/new" className="ws-new" title="新建项目">
            +
          </Link>
        </div>
        {cells.data.map((c) => (
          <button
            key={c.name}
            className={`ws-item ${c.name === cell ? 'on' : ''}`}
            onClick={() => setCell(c.name)}
          >
            <span className="ws-item-name">{c.name}</span>
            <span className={`dot ${cellTone(c.phase)}`} />
            <span className="ws-item-sub">
              {c.activeSessions ?? 0}/{c.maxSessions ?? 0} 在用
            </span>
          </button>
        ))}
      </aside>

      <Splitter onDrag={dragList} title="拖动调整项目栏宽度(双击复位)" />

      <section className="ws-main">{cell && <CellWork cell={cell} />}</section>

      {showTree && <Splitter onDrag={dragTree} side="right" title="拖动调整分支栏宽度(双击复位)" />}

      {showTree ? (
        <aside className="ws-tree">
          <div className="ws-head">
            <span>分支</span>
            <button className="ws-fold" onClick={() => setShowTree(false)} title="收起">
              ›
            </button>
          </div>
          {cell && <BranchTree cell={cell} />}
        </aside>
      ) : (
        <button className="ws-unfold" onClick={() => setShowTree(true)} title="展开分支">
          ‹
        </button>
      )}
    </div>
  )
}

/** The middle column: this project's session, its terminal, and one input. */
function CellWork({ cell }: { cell: string }) {
  const qc = useQueryClient()
  const toast = useToast()
  const [text, setText] = useState('')

  const detail = useQuery({
    queryKey: ['cell', cell],
    queryFn: () => api.cell(cell),
    refetchInterval: 5000,
  })
  // The one session that is mine here. Sessions are one per person per
  // project now, so "which one" is not a question the UI needs to ask.
  const live = (detail.data?.sessions ?? []).find(
    (s) => s.phase === 'Running' || s.phase === 'Queued' || s.phase === 'Dormant',
  )

  // Say a thing. The server fills in the runner, the provider and the key
  // from what the project already decided — none of that is new information
  // at the moment somebody says what they want.
  const say = useMutation({
    mutationFn: async () => {
      if (live) {
        await api.continueSession(live.name, text)
        return
      }
      await api.dispatchSimple(cell, text)
    },
    onSuccess: () => {
      setText('')
      qc.invalidateQueries({ queryKey: ['cell', cell] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  if (detail.isLoading) return <Spinner />

  return (
    <>
      <div className="ws-head ws-main-head">
        <Link to={`/cells/${cell}`} className="ws-title">
          {cell}
        </Link>
        {detail.data?.cell && <Badge tone={cellTone(detail.data.cell.phase)}>{detail.data.cell.phase}</Badge>}
        {live && <span className="faint">{live.phase}</span>}
        <span className="spacer" />
        {detail.data?.cell?.previewURL && (
          <a className="small" href={detail.data.cell.previewURL} target="_blank" rel="noreferrer">
            打开预览 ↗
          </a>
        )}
        {live && <SessionControls session={live.name} phase={live.phase} onDone={() => qc.invalidateQueries({ queryKey: ['cell', cell] })} />}
      </div>

      <div className="ws-term">
        {live ? (
          <TerminalDeck session={live.name} />
        ) : (
          <div className="ws-empty">
            <p>这个项目还没有你的会话。</p>
            <p className="hint">在下面说一句要做什么,就会开一条——之后一直在这条里继续。</p>
          </div>
        )}
      </div>

      <div className="ws-say">
        <textarea
          rows={2}
          value={text}
          placeholder={live ? '接着说 —— 会进同一条对话' : '要做什么?'}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey && text.trim() && !say.isPending) {
              e.preventDefault()
              say.mutate()
            }
          }}
        />
        <button disabled={!text.trim() || say.isPending} onClick={() => say.mutate()}>
          说
        </button>
      </div>
    </>
  )
}

/**
 * The right column: what this project has actually produced.
 *
 * Ahead/behind against the base branch, because that is the real question
 * about a session branch — is it merged, and how far has it drifted. A
 * branch with nothing of its own is marked as such: it is the one piece of
 * advice this panel can give.
 */
function BranchTree({ cell }: { cell: string }) {
  const q = useQuery({
    queryKey: ['branches', cell],
    queryFn: () => api.branches(cell),
    refetchInterval: 20000,
    retry: false,
  })
  if (q.isLoading) return <Spinner />
  const branches = q.data ?? []
  if (!branches.length) {
    return <p className="hint" style={{ padding: '10px 12px' }}>还读不到分支——项目可能还在起。</p>
  }
  // A project group's branches are grouped by repository: they live in
  // different repositories on the forge, so listing them in one flat list
  // would imply a relationship they do not have.
  const repos = [...new Set(branches.map((b) => b.repo ?? ''))]
  return (
    <div className="tree">
      {repos.map((r) => {
        const mine = branches.filter((b) => (b.repo ?? '') === r)
        const base = mine.find((b) => b.base)
        const rest = mine.filter((b) => !b.base)
        return (
          <div key={r}>
            {r && <div className="tree-repo">{r}</div>}
            {base && <BranchRow b={base} cell={cell} />}
            {rest.map((b) => (
              <BranchRow key={r + b.name} b={b} cell={cell} indent />
            ))}
          </div>
        )
      })}
    </div>
  )
}

function BranchRow({ b, cell, indent }: { b: Branch; cell: string; indent?: boolean }) {
  return (
    <div className={`tree-row ${indent ? 'in' : ''} ${b.base ? 'base' : ''}`}>
      <div className="tree-line">
        {indent && <span className="tree-elbow">└</span>}
        <span className="tree-name" title={b.name}>
          {b.session ? b.session.slice(-8) : b.name}
        </span>
        {b.base && <span className="tree-tag">基线</span>}
        {b.merged && <span className="tree-tag merged">已并入</span>}
      </div>
      <div className="tree-meta">
        {!b.base && (
          <span className="mono">
            +{b.ahead} −{b.behind}
          </span>
        )}
        <span className="faint">{b.when}</span>
      </div>
      <div className="tree-subject" title={b.subject}>
        {b.subject}
      </div>
      {b.session && (
        <Link className="tree-link" to={`/cells/${cell}`}>
          看这一单 →
        </Link>
      )}
    </div>
  )
}
