import { useEffect, useState } from 'react'
import { getHello, getHealth } from './api/client'
import './App.css'

type Status = 'checking' | 'ok' | 'unreachable'

function App() {
  const [status, setStatus] = useState<Status>('checking')
  const [name, setName] = useState('kaigi')
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')

  useEffect(() => {
    const controller = new AbortController()
    getHealth(controller.signal)
      .then((h) => setStatus(h.status === 'ok' ? 'ok' : 'unreachable'))
      .catch(() => {
        if (!controller.signal.aborted) setStatus('unreachable')
      })
    return () => controller.abort()
  }, [])

  async function sayHello() {
    setError('')
    try {
      const { message } = await getHello(name)
      setMessage(message)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  return (
    <main className="app">
      <h1>kaigi</h1>
      <p className={`status status--${status}`}>backend: {status}</p>

      <div className="row">
        <input
          aria-label="Name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="name"
        />
        <button type="button" onClick={sayHello}>
          Call /api/hello
        </button>
      </div>

      {message && <p className="result">{message}</p>}
      {error && <p className="error">{error}</p>}
    </main>
  )
}

export default App
