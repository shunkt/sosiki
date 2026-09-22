import { useCallback, useEffect, useState } from 'react'
import { listAgents, type Agent } from './api/client'
import { AppHeader } from './AppHeader'
import { PersonaCard } from './PersonaCard'

// Presence is derived from heartbeat TTL on the server, so a persona can drop
// out while this page is open; re-poll rather than showing stale badges.
const REFRESH_INTERVAL_MS = 30_000

export default function PersonasPage() {
  const [agents, setAgents] = useState<Agent[] | null>(null)
  const [error, setError] = useState('')
  const [reloadKey, setReloadKey] = useState(0)

  const load = useCallback((signal: AbortSignal) => {
    listAgents(signal)
      .then((list) => {
        setAgents(list)
        setError('')
      })
      .catch(() => {
        if (!signal.aborted) setError('ペルソナの取得に失敗しました')
      })
  }, [])

  // The controller is aborted in cleanup, which also covers React 19
  // StrictMode's double-invoked effects: the first run's interval is cleared
  // before the second run starts its own.
  useEffect(() => {
    const controller = new AbortController()
    load(controller.signal)
    const timer = setInterval(() => load(controller.signal), REFRESH_INTERVAL_MS)
    return () => {
      clearInterval(timer)
      controller.abort()
    }
  }, [load, reloadKey])

  const presentCount = agents?.filter((a) => a.present).length ?? 0

  return (
    <main className="app app--page">
      <AppHeader presence={agents ? { present: presentCount, total: agents.length } : undefined} />

      {error && (
        <p className="error">
          {error}{' '}
          <button type="button" onClick={() => setReloadKey((k) => k + 1)}>
            再読込
          </button>
        </p>
      )}

      {agents === null && !error && <p>読み込み中…</p>}
      {agents?.length === 0 && <p>登録されているペルソナはありません</p>}

      <section className="persona-list">
        {agents?.map((a) => (
          <PersonaCard key={a.slug} agent={a} />
        ))}
      </section>
    </main>
  )
}
