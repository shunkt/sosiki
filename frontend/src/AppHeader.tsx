import { useEffect, useState } from 'react'
import { NavLink } from 'react-router'
import { getHealth } from './api/client'

type Status = 'checking' | 'ok' | 'degraded' | 'unreachable'

// AppHeader owns the backend health probe so both pages share one header
// instead of each duplicating the effect. Presence counts belong to the page
// (it already holds the agent list), so they arrive as a prop.
export function AppHeader({ presence }: { presence?: { present: number; total: number } }) {
  const [status, setStatus] = useState<Status>('checking')

  useEffect(() => {
    const controller = new AbortController()
    getHealth(controller.signal)
      .then((h) => setStatus(h.status === 'ok' ? 'ok' : 'degraded'))
      .catch(() => {
        if (!controller.signal.aborted) setStatus('unreachable')
      })
    return () => controller.abort()
  }, [])

  return (
    <header className="app-header">
      <h1>kaigi</h1>
      <nav className="app-nav">
        <NavLink to="/" end>
          会議
        </NavLink>
        <NavLink to="/personas">ペルソナ一覧</NavLink>
      </nav>
      <span className={`status status--${status}`}>backend: {status}</span>
      {presence && (
        <span className="presence">
          ● {presence.present}/{presence.total} 在席
        </span>
      )}
    </header>
  )
}
