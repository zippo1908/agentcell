import { useEffect, useState } from 'react'
import { Link, NavLink, Outlet, useLocation } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'
import { cellTone } from '../lib/format'
import { getTheme, setTheme, type Theme } from '../lib/theme'

/** 16px stroke icons, inline so there is no icon font to load. */
const icon = (d: string) => (
  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    {d.split('|').map((p, i) => (
      <path key={i} d={p} />
    ))}
  </svg>
)
const IconHome = icon('M3 11l9-8 9 8|M5 10v10h14V10')
const IconPeople = icon('M9 11a3 3 0 1 0 0-6 3 3 0 0 0 0 6z|M3 20a6 6 0 0 1 12 0|M17 11a3 3 0 1 0 0-6|M17 14a6 6 0 0 1 4 6')
const IconCells = icon('M4 4h7v7H4z|M13 4h7v7h-7z|M4 13h7v7H4z|M13 13h7v7h-7z')
const IconReview = icon('M9 11l3 3 8-8|M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11')
const IconKey = icon('M15 7a4 4 0 1 1-3.9 5H7v3H4v-3l3.1-3H11a4 4 0 0 1 4-4z')
const IconWork = icon('M4 6h16v12H4z|M4 10h16|M9 10v8')
const IconBoard = icon('M4 5h16v11H4z|M8 20l4-4 4 4')
const IconCaps = icon('M12 3l8 4.5v9L12 21l-8-4.5v-9z|M12 12l8-4.5|M12 12v9|M12 12L4 7.5')

/**
 * App chrome: a fixed 224px sidebar and a fluid centred content column.
 *
 * The active nav item is a solid pill in the accent ink — the loudest thing
 * in the UI, and one of only two filled surfaces (the other is a primary
 * button). Everything else is hairlines and neutrals, so "where am I" never
 * needs a second look.
 */
export function Shell() {
  // The navigation folds away. On the workspace especially, every column
  // that is not the terminal is in the way — and this one is 224px of
  // links somebody has already learned.
  const [navOpen, setNavOpen] = useState(() => localStorage.getItem('ws-nav') !== 'closed')
  useEffect(() => {
    localStorage.setItem('ws-nav', navOpen ? 'open' : 'closed')
  }, [navOpen])
  // Each group folds on its own, and stays folded across reloads — tidying
  // the nav away once should not have to be redone every visit.
  const [projectsOpen, setProjectsOpen] = useState(() => localStorage.getItem('ws-nav-projects') !== 'closed')
  useEffect(() => {
    localStorage.setItem('ws-nav-projects', projectsOpen ? 'open' : 'closed')
  }, [projectsOpen])
  const [manageOpen, setManageOpen] = useState(() => localStorage.getItem('ws-nav-manage') !== 'closed')
  useEffect(() => {
    localStorage.setItem('ws-nav-manage', manageOpen ? 'open' : 'closed')
  }, [manageOpen])
  const { data: reviews } = useQuery({ queryKey: ['reviews'], queryFn: () => api.reviews() })
  const { data: me } = useQuery({ queryKey: ['me'], queryFn: api.me, staleTime: 60_000, retry: false })
  const pending = reviews?.filter((r) => r.state === 'Pending').length ?? 0

  const link = ({ isActive }: { isActive: boolean }) => (isActive ? 'active' : '')
  const onWorkspace = useLocation().pathname.startsWith('/workspace')
  const [theme, setThemeState] = useState<Theme>(() => getTheme())
  const { data: cells } = useQuery({ queryKey: ['cells'], queryFn: () => api.cells(), refetchInterval: 15000 })

  return (
    <div className="app-shell">
      <aside className={`sidebar ${navOpen ? '' : 'folded'}`}>
        <button
          className="nav-fold"
          onClick={() => setNavOpen(!navOpen)}
          title={navOpen ? '收起菜单' : '展开菜单'}
          aria-label={navOpen ? '收起菜单' : '展开菜单'}
          aria-expanded={navOpen}
        >
          {navOpen ? '‹' : '›'}
        </button>
        <div className="logo-block">
          <span className="logo-glyph">
            <span />
          </span>
          <span>
            <span className="logo-text">AgentCell</span>
            <br />
            <span className="logo-sub">agent 车间</span>
          </span>
        </div>
        <nav>
          {/* The daily loop: ask for work, watch it happen, review what came
              back. Review sits here — not behind a popup — because its
              pending badge is exactly the kind of thing that must be visible
              without opening anything. */}
          <NavLink to="/board" className={link}>
            {IconBoard} 黑板
          </NavLink>
          <NavLink to="/workspace" className={link}>
            {IconWork} 工作台
          </NavLink>
          <NavLink to="/reviews" className={link}>
            {IconReview} 批阅
            {pending > 0 && <span className="nav-count">{pending > 99 ? '99+' : pending}</span>}
          </NavLink>
        </nav>

        {/* Projects live in the navigation, not inside the workspace.
            Choosing what to work on IS navigation; keeping a second list of
            projects inside the page meant the same choice existed twice and
            cost a column of the terminal's width. */}
        <div className="nav-label nav-projects-label">
          <span className="nav-label-group">
            <button
              type="button"
              className="nav-group-fold"
              onClick={() => setProjectsOpen(!projectsOpen)}
              aria-expanded={projectsOpen}
              aria-label={projectsOpen ? '收起项目列表' : '展开项目列表'}
              title={projectsOpen ? '收起项目列表' : '展开项目列表'}
            >
              {projectsOpen ? '▾' : '▸'}
            </button>
            项目
          </span>
          <Link to="/cells/new" className="ws-new" title="新建项目" aria-label="新建项目">
            +
          </Link>
        </div>
        <div className={`nav-projects ${projectsOpen ? '' : 'nav-group-closed'}`}>
          {(cells ?? []).map((c) => (
            <NavLink
              key={c.name}
              to={`/workspace/${c.name}`}
              className={({ isActive }) => `nav-project ${isActive ? 'active' : ''}`}
              title={c.description || c.displayName || c.name}
            >
              <span className={`dot ${cellTone(c.phase)}`} />
              <span className="nav-project-name">{c.displayName || c.name}</span>
            </NavLink>
          ))}
          {(cells ?? []).length === 0 && (
            <Link to="/cells/new" className="nav-project muted">
              还没有项目,建一个
            </Link>
          )}
        </div>

        {/* Administration, not work: these are the pages you visit to
            configure the platform rather than to use it, so they get their
            own group at the bottom of the nav instead of competing with the
            daily loop above. */}
        <div className="nav-label nav-label-group">
          <button
            type="button"
            className="nav-group-fold"
            onClick={() => setManageOpen(!manageOpen)}
            aria-expanded={manageOpen}
            aria-label={manageOpen ? '收起管理列表' : '展开管理列表'}
            title={manageOpen ? '收起管理列表' : '展开管理列表'}
          >
            {manageOpen ? '▾' : '▸'}
          </button>
          管理
        </div>
        <nav className={`nav-manage ${manageOpen ? '' : 'nav-group-closed'}`}>
          <NavLink to="/dashboard" className={link}>
            {IconHome} 概览
          </NavLink>
          <NavLink to="/cells" className={link}>
            {IconCells} 全部项目
          </NavLink>
          <NavLink to="/capabilities" className={link}>
            {IconCaps} 能力
          </NavLink>
          <NavLink to="/credentials" className={link}>
            {IconKey} 凭据
          </NavLink>
          <NavLink to="/people" className={link}>
            {IconPeople} 人员
          </NavLink>
        </nav>

        <span className="spacer" />

        <div className="user-box">
          <div className="theme-switch" role="group" aria-label="界面主题">
            {(
              [
                ['light', '浅色'],
                ['dark', '深色'],
                ['system', '系统'],
              ] as [Theme, string][]
            ).map(([t, label]) => (
              <button
                key={t}
                type="button"
                className={theme === t ? 'on' : ''}
                onClick={() => {
                  setTheme(t)
                  setThemeState(t)
                }}
              >
                {label}
              </button>
            ))}
          </div>
          <div className="user-line">
            <span className="user-mark">{(me?.name ?? '?').slice(0, 2)}</span>
            <span style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis' }}>
              {me?.name ?? '…'}
            </span>
            <form method="post" action="/logout">
              <button className="ghost small" type="submit">
                退出
              </button>
            </form>
          </div>
          {me?.shared && (
            <div className="faint" style={{ fontSize: 11, lineHeight: 1.5 }}>
              共享令牌登录:所有人是同一个主体,会话之间没有私密性。
            </div>
          )}
        </div>
      </aside>
      <main className="main">
        {/* The workspace is an instrument panel, not a document: it gets the
            whole width, and sizes its own margin. */}
        <div className={`main-inner ${onWorkspace ? 'bleed' : ''}`}>
          <Outlet />
        </div>
      </main>
    </div>
  )
}
