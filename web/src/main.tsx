import React from 'react'
import ReactDOM from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createBrowserRouter, RouterProvider, Navigate } from 'react-router-dom'

import './styles.css'
import { Shell } from './components/Shell'
import { CellsPage } from './pages/CellsPage'
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
      { path: '/', element: <Navigate to="/cells" replace /> },
      { path: '/cells', element: <CellsPage /> },
      { path: '/cells/:name', element: <CellPage /> },
      { path: '/reviews', element: <ReviewsPage /> },
    ],
  },
])

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </React.StrictMode>,
)
