import { NavLink, Outlet, useMatch } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'

/** App chrome: brand, primary nav, and a pending-review count badge. */
export function Shell() {
  const cellDetail = useMatch('/cells/:name')
  const { data: reviews } = useQuery({
    queryKey: ['reviews'],
    queryFn: () => api.reviews(),
  })
  const pending = reviews?.filter((r) => r.state === 'Pending').length ?? 0

  return (
    <div className="app">
      <header className="topbar">
        <span className="brand">AgentCell</span>
        <nav>
          <NavLink to="/cells" className={({ isActive }) => (isActive ? 'active' : '')}>
            Cells
          </NavLink>
          <NavLink to="/reviews" className={({ isActive }) => (isActive ? 'active' : '')}>
            批阅{pending > 0 ? ` (${pending})` : ''}
          </NavLink>
        </nav>
        <span className="spacer" />
        <span className="status">
          {cellDetail ? `cell/${cellDetail.params.name}` : ''}
        </span>
      </header>
      <Outlet />
    </div>
  )
}
