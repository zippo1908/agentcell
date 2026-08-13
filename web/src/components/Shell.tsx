import { NavLink, Outlet } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'

/** 16px stroke icons, inline so there is no icon font to load. */
const icon = (d: string) => (
  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    {d.split('|').map((p, i) => (
      <path key={i} d={p} />
    ))}
  </svg>
)
const IconHome = icon('M3 11l9-8 9 8|M5 10v10h14V10')
const IconCells = icon('M4 4h7v7H4z|M13 4h7v7h-7z|M4 13h7v7H4z|M13 13h7v7h-7z')
const IconReview = icon('M9 11l3 3 8-8|M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11')
const IconKey = icon('M15 7a4 4 0 1 1-3.9 5H7v3H4v-3l3.1-3H11a4 4 0 0 1 4-4z')
const IconCaps = icon('M12 3l8 4.5v9L12 21l-8-4.5v-9z|M12 12l8-4.5|M12 12v9|M12 12L4 7.5')

/**
 * App chrome: a fixed 224px sidebar and a fluid centred content column.
 *
 * The active nav item is a solid black pill — the loudest thing in the UI,
 * and one of only two filled-black surfaces (the other is a primary button).
 * Everything else is hairlines and grays, so "where am I" never needs a
 * second look.
 */
export function Shell() {
  const { data: reviews } = useQuery({ queryKey: ['reviews'], queryFn: () => api.reviews() })
  const { data: me } = useQuery({ queryKey: ['me'], queryFn: api.me, staleTime: 60_000, retry: false })
  const pending = reviews?.filter((r) => r.state === 'Pending').length ?? 0

  const link = ({ isActive }: { isActive: boolean }) => (isActive ? 'active' : '')

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="logo-block">
          <span className="logo-glyph">
            <span />
          </span>
          <span>
            <span className="logo-text">AgentCell</span>
            <br />
            <span className="logo-sub">agent 工作台</span>
          </span>
        </div>
        <nav>
          <div className="nav-label">导航</div>
          <NavLink to="/dashboard" className={link}>
            {IconHome} 工作台
          </NavLink>
          <NavLink to="/cells" className={link}>
            {IconCells} 工作区
          </NavLink>
          <NavLink to="/reviews" className={link}>
            {IconReview} 批阅
            {pending > 0 && <span className="nav-count">{pending > 99 ? '99+' : pending}</span>}
          </NavLink>
          <NavLink to="/capabilities" className={link}>
            {IconCaps} 能力
          </NavLink>
          <NavLink to="/credentials" className={link}>
            {IconKey} 我的凭据
          </NavLink>
          <NavLink to="/teams" className={link}>
            团队
          </NavLink>
        </nav>
        <span className="spacer" />
        <div className="user-box">
          <div className="user-line">
            <span className="user-mark">{(me?.name ?? '?').slice(0, 2)}</span>
            <span style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis' }}>
              {me?.name ?? '…'}
            </span>
          </div>
          {/* A console that implies per-user privacy it does not have is worse
              than one that admits it. */}
          {me?.shared && (
            <div className="faint" style={{ fontSize: 11, lineHeight: 1.5 }}>
              共享令牌登录:所有人是同一个主体,会话之间没有私密性。配置 OIDC 后每人独立。
            </div>
          )}
        </div>
      </aside>
      <main className="main">
        <div className="main-inner">
          <Outlet />
        </div>
      </main>
    </div>
  )
}
