import React from 'react'
import ReactDOM from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createBrowserRouter, RouterProvider, Navigate } from 'react-router-dom'

import './styles.css'
import { Shell } from './components/Shell'
import { CellsPage } from './pages/CellsPage'
import { CellNewPage } from './pages/CellNewPage'
import { CapabilitiesPage } from './pages/CapabilitiesPage'
import { CredentialsPage } from './pages/CredentialsPage'
import { BoardPage } from './pages/BoardPage'
import { PeoplePage } from './pages/PeoplePage'
import { WorkspacePage } from './pages/WorkspacePage'
import { DashboardPage } from './pages/DashboardPage'
import { ToastProvider } from './ui/primitives'
import { CellPage } from './pages/CellPage'
import { ReviewsPage } from './pages/ReviewsPage'

// The platform state lives in Kubernetes; polling keeps the UI honest
// without a websocket. Short interval because a session's phase and a
// preview's readiness are what the user is watching.
const queryClient = new QueryClient({
  defaultOptions: {
    queries: { refetchInterval: 4000, refetchOnWindowFocus: true, retry: 1 },
  },
})

const router = createBrowserRouter([
  {
    element: <Shell />,
    children: [
      { path: '/', element: <Navigate to="/board" replace /> },
      { path: '/dashboard', element: <DashboardPage /> },
      { path: '/cells', element: <CellsPage /> },
      { path: '/cells/new', element: <CellNewPage /> },
      { path: '/capabilities', element: <CapabilitiesPage /> },
      { path: '/credentials', element: <CredentialsPage /> },
      { path: '/board', element: <BoardPage /> },
      { path: '/people', element: <PeoplePage /> },
      { path: '/workspace', element: <WorkspacePage /> },
      { path: '/workspace/:cell', element: <WorkspacePage /> },
      { path: '/cells/:name', element: <CellPage /> },
      { path: '/reviews', element: <ReviewsPage /> },
    ],
  },
])

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>
  </React.StrictMode>,
)
