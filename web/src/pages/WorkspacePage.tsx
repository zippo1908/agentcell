import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import type { Branch } from '../api/types'
import { TerminalDeck } from '../components/TerminalDeck'
import { Knowledge, Members, ProjectTokens } from '../components/ProjectPanels'
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
  // The project comes from the URL, because choosing one is navigation and
  // the sidebar is where navigation lives. Keeping a second list in here
  // meant the same choice existed twice and cost a column of the terminal's
  // width.
  const { cell: routeCell } = useParams()
  const navigate = useNavigate()
  const cell = routeCell ?? ''

  const [showTree, setShowTree] = useState(true)
  // Which pane the middle is showing. The parent needs to know because a
  // preview takes the branch column's width as well.
  // Which pane the workspace is showing. The preview is no longer one of
  // them — it opens in its own window from the header.
  const [mainView, setMainView] = useState<WorkView>('terminal')
  const root = useRef<HTMLDivElement>(null)
  // Both side columns are draggable and both remember their width. The
  // middle takes whatever is left, because the middle is the terminal.
  const [treeW, setTreeW] = usePaneWidth('tree', 260, 180, 520)
  // The page's own side margin. On a wide screen the default gutter throws
  // away exactly the width a terminal wants; on a narrow one, removing it
  // makes the columns touch the edge. So it is a setting, and it is
  // remembered.
  const [gutter, setGutter] = usePaneWidth('gutter', 16, 0, 160)

  // -1 from a double-click means "back to the default".
  // Dragging the page edge itself. Measured from the window, not the grid,
  // because the thing being sized IS the space outside the grid.
  const dragGutter = useCallback(
    (x: number) => setGutter(x < 0 ? 16 : Math.max(0, Math.min(160, x))),
    [setGutter],
  )
  const dragTree = useCallback(
    (x: number) => {
      const right = root.current?.getBoundingClientRect().right ?? 0
      setTreeW(x < 0 ? 260 : right - x)
    },
    [setTreeW],
  )

  const cells = useQuery({ queryKey: ['cells'], queryFn: () => api.cells(), refetchInterval: 10000 })
  // Land on something rather than on nothing: with no project in the URL,
  // go to the first one.
  useEffect(() => {
    if (!cell && cells.data?.length) navigate(`/workspace/${cells.data[0].name}`, { replace: true })
  }, [cells.data, cell, navigate])

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
      className={`ws ${showTree && mainView === 'terminal' ? '' : 'ws-notree'}`}
      style={
        {
          '--ws-tree': `${treeW}px`,
          '--ws-gutter': `${gutter}px`,
        } as React.CSSProperties
      }
    >
      {/* The gutter track needs an element of its own: grid items fill
          tracks in order, and a track with no child shifts every column
          after it one place to the left — which put the terminal into a
          five-pixel divider. */}
      <div className="ws-gutter" />
      <Splitter onDrag={dragGutter} title="拖动调整页面左右留白(双击复位)" />


      <section className="ws-main">
        {cell && <CellWork cell={cell} onView={setMainView} />}
      </section>

      {showTree && mainView === 'terminal' && (
        <Splitter onDrag={dragTree} side="right" title="拖动调整分支栏宽度(双击复位)" />
      )}

      {showTree && mainView === 'terminal' ? (
        <aside className="ws-tree">
          <div className="ws-head">
            <span>分支</span>
            <button className="ws-fold" onClick={() => setShowTree(false)} title="收起">
              ›
            </button>
          </div>
          {cell && <BranchTree cell={cell} />}
        </aside>
      ) : mainView === 'terminal' ? (
        <button className="ws-unfold" onClick={() => setShowTree(true)} title="展开分支">
          ‹
        </button>
      ) : null}
    </div>
  )
}

/** The middle column: this project's session, its terminal, and one input. */
type WorkView = 'terminal' | 'knowledge' | 'members' | 'tokens'

function CellWork({ cell, onView }: { cell: string; onView: (v: WorkView) => void }) {
  const qc = useQueryClient()
  const toast = useToast()
  const [text, setText] = useState('')
  const [view, setViewRaw] = useState<WorkView>('terminal')
  // Dropping a file on the terminal puts it in the project's library, which
  // is the directory the agent already reads. Uploading used to mean leaving
  // the terminal, finding the right tab, and coming back — and then the file
  // still could not be seen until the session restarted.
  const [dropping, setDropping] = useState(false)
  const [uploading, setUploading] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)

  async function takeFiles(list: FileList | null) {
    if (!list?.length) return
    setUploading(true)
    try {
      for (const f of Array.from(list)) await api.uploadFile(cell, f)
      qc.invalidateQueries({ queryKey: ['files', cell] })
      toast.success(
        list.length > 1
          ? `已上传 ${list.length} 个文件,agent 几秒后就能读到`
          : `已上传 ${list[0].name},agent 几秒后就能读到`,
      )
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setUploading(false)
      if (fileRef.current) fileRef.current.value = ''
    }
  }
  const setView = (v: WorkView) => {
    setViewRaw(v)
    onView(v)
  }

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

  // Switching project lands you in its terminal, not wherever you happened
  // to be looking in the last one.
  useEffect(() => {
    setViewRaw('terminal')
  }, [cell])

  const opening = useRef('')
  // The reason opening failed, told to the person who can fix it. Leaving
  // this in a catch meant "正在给你开" stayed up forever — slow and broken
  // read exactly the same, and broken is the common case (no key yet).
  const [openError, setOpenError] = useState<string | null>(null)
  const tryOpen = useCallback(() => {
    if (!cell) return
    opening.current = cell
    setOpenError(null)
    api
      .openCell(cell)
      .then(() => qc.invalidateQueries({ queryKey: ['cell', cell] }))
      .catch((e: Error) => {
        opening.current = ''
        setOpenError(e.message)
      })
  }, [cell, qc])
  useEffect(() => {
    setOpenError(null)
    if (!cell || live || detail.isLoading || opening.current === cell) return
    tryOpen()
  }, [cell, live, detail.isLoading, tryOpen])

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

  // Terminal or preview, in the same pane.
  //
  // The preview used to open in another tab, which meant leaving the work to
  // look at the result of the work and finding your place again on the way
  // back. In here it gets the full width — the branch column folds away
  // while it is showing, because a preview is a page and pages want room.
  if (detail.isLoading) return <Spinner />

  return (
    <>
      <div className="ws-head ws-main-head">
        <Link to={`/cells/${cell}`} className="ws-title">
          {detail.data?.cell?.displayName || cell}
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

      <div className="ws-tabs">
        <button className={`ws-tab ${view === 'terminal' ? 'on' : ''}`} onClick={() => setView('terminal')}>
          终端
        </button>
        {/* The preview left this strip: it is better as a whole window than
            as a pane beside a terminal, and the button for it is in the header
            above. What takes its place is what a project actually owns. */}
        <button className={`ws-tab ${view === 'knowledge' ? 'on' : ''}`} onClick={() => setView('knowledge')}>
          知识库
        </button>
        <button className={`ws-tab ${view === 'members' ? 'on' : ''}`} onClick={() => setView('members')}>
          成员
        </button>
        <button className={`ws-tab ${view === 'tokens' ? 'on' : ''}`} onClick={() => setView('tokens')}>
          令牌
        </button>
        <span className="spacer" />
        <input
          ref={fileRef}
          type="file"
          multiple
          hidden
          onChange={(e) => takeFiles(e.target.files)}
        />
        <button className="ws-tab" disabled={uploading} onClick={() => fileRef.current?.click()}>
          {uploading ? '上传中…' : '＋ 文件'}
        </button>
      </div>

      <div
        className={`ws-term ${dropping ? 'dropping' : ''}`}
        hidden={view !== 'terminal'}
        onDragOver={(e) => {
          // Both are required, or the browser navigates to the file instead
          // of letting the page have it.
          e.preventDefault()
          setDropping(true)
        }}
        onDragLeave={(e) => {
          // Only when the pointer actually left the pane: dragging over a
          // child fires dragleave for the parent and the hint flickers.
          if (!e.currentTarget.contains(e.relatedTarget as Node)) setDropping(false)
        }}
        onDrop={(e) => {
          e.preventDefault()
          setDropping(false)
          takeFiles(e.dataTransfer.files)
        }}
      >
        {dropping && (
          <div className="ws-drop">
            <p>松手就传进这个项目的知识库</p>
            <p className="hint">agent 在 .agentcell/library/ 里直接读得到,不用重开会话</p>
          </div>
        )}
        {live ? (
          <TerminalDeck session={live.name} />
        ) : openError ? (
          <div className="ws-empty">
            <p>终端没开起来:{openError}</p>
            <div className="btn-row" style={{ marginTop: 8 }}>
              <Link to="/credentials">
                <button className="small">去凭据页</button>
              </Link>
              <button className="ghost small" onClick={tryOpen}>
                重试
              </button>
            </div>
          </div>
        ) : (
          <div className="ws-empty">
            <p>正在给你开这个项目的终端……</p>
            <p className="hint">开好之后就一直在,说话都进同一条。开不出来的话,多半是还没配模型 key。</p>
          </div>
        )}
      </div>

      {view !== 'terminal' && (
        <div className="ws-panel">
          {view === 'knowledge' && <Knowledge cell={cell} />}
          {view === 'members' && detail.data?.cell && (
            <Members
              cell={cell}
              open={detail.data.cell.access === 'open'}
              members={detail.data.cell.members ?? []}
            />
          )}
          {view === 'tokens' && detail.data?.cell && <ProjectTokens cell={detail.data.cell} />}
        </div>
      )}

      <div className="ws-say" hidden={view !== 'terminal'}>
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
