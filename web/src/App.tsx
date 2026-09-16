import React from 'react'
import { createBrowserRouter, RouterProvider, Navigate } from 'react-router-dom'
import { ConsoleLayout } from './components/Layout'
import LoginPage from './pages/Login'
import RegisterPage from './pages/Register'
import KeysPage from './pages/Keys'
import ChannelsPage from './pages/Channels'
import ModelsPage from './pages/Models'
import TokensPage from './pages/Tokens'
import GuidePage from './pages/Guide'
import LogsPage from './pages/Logs'
import StatsPage from './pages/Stats'
import AdminPage from './pages/Admin'

const router = createBrowserRouter([
  { path: '/login', element: <LoginPage /> },
  { path: '/register', element: <RegisterPage /> },
  {
    path: '/',
    element: <ConsoleLayout />,
    children: [
      { index: true, element: <Navigate to="/channels" replace /> },
      { path: 'keys', element: <KeysPage /> },
      { path: 'channels', element: <ChannelsPage /> },
      { path: 'channels/:id', element: <ChannelsPage /> },
      { path: 'models', element: <ModelsPage /> },
      { path: 'tokens', element: <TokensPage /> },
      { path: 'guide', element: <GuidePage /> },
      { path: 'logs', element: <LogsPage /> },
      { path: 'stats', element: <StatsPage /> },
      { path: 'admin/*', element: <AdminPage /> },
    ],
  },
])

const App: React.FC = () => <RouterProvider router={router} />

export default App
