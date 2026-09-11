import { useEffect, useRef, useState } from 'react'
import {
  createConversation,
  getHealth,
  listPersonas,
  sendMessage,
  type Message,
  type Persona,
  type Source,
} from './api/client'
import './App.css'

type Status = 'checking' | 'ok' | 'degraded' | 'unreachable'

// DisplayMessage extends the persisted Message shape with per-turn UI state
// (sources arrive as a separate SSE event, before the message itself is
// saved) so a single list can render both historical and in-flight turns.
type DisplayMessage = Message & { sources?: Source[] }

function App() {
  const [status, setStatus] = useState<Status>('checking')
  const [personas, setPersonas] = useState<Persona[]>([])
  const [personaId, setPersonaId] = useState<string>('')
  const [conversationId, setConversationId] = useState<string>('')
  const [messages, setMessages] = useState<DisplayMessage[]>([])
  const [input, setInput] = useState('')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState('')

  // Guards against React 19 StrictMode's double-invoked effects starting two
  // overlapping streams, and lets a persona switch cancel an in-flight reply.
  const abortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    const controller = new AbortController()
    getHealth(controller.signal)
      .then((h) => setStatus(h.status === 'ok' ? 'ok' : 'degraded'))
      .catch(() => {
        if (!controller.signal.aborted) setStatus('unreachable')
      })
    listPersonas(controller.signal)
      .then((list) => {
        setPersonas(list)
        if (list.length > 0) setPersonaId(list[0].id)
      })
      .catch(() => {
        if (!controller.signal.aborted) setError('ペルソナの取得に失敗しました')
      })
    return () => controller.abort()
  }, [])

  // Selecting a persona starts a fresh conversation — cancel any reply still
  // streaming for the previous one first.
  useEffect(() => {
    abortRef.current?.abort()
    setConversationId('')
    setMessages([])
    if (!personaId) return

    const controller = new AbortController()
    createConversation(personaId, '', controller.signal)
      .then((conv) => {
        if (!controller.signal.aborted) setConversationId(conv.id)
      })
      .catch(() => {
        if (!controller.signal.aborted) setError('会話の作成に失敗しました')
      })
    return () => controller.abort()
  }, [personaId])

  async function handleSend() {
    const content = input.trim()
    if (!content || !conversationId || sending) return

    setInput('')
    setError('')
    setSending(true)
    setMessages((prev) => [
      ...prev,
      { id: `pending-user-${Date.now()}`, role: 'user', content, createdAt: '' },
    ])

    const controller = new AbortController()
    abortRef.current = controller

    // The assistant turn accumulates token-by-token into the same list entry
    // rather than as a separate buffer, so streaming and history rendering
    // share one code path.
    let assistantIndex = -1
    setMessages((prev) => {
      assistantIndex = prev.length
      return [...prev, { id: `pending-assistant-${Date.now()}`, role: 'assistant', content: '', createdAt: '' }]
    })

    try {
      for await (const event of sendMessage(conversationId, content, controller.signal)) {
        switch (event.type) {
          case 'sources':
            setMessages((prev) => {
              const next = [...prev]
              next[assistantIndex] = { ...next[assistantIndex], sources: event.sources }
              return next
            })
            break
          case 'token':
            setMessages((prev) => {
              const next = [...prev]
              next[assistantIndex] = {
                ...next[assistantIndex],
                content: next[assistantIndex].content + event.text,
              }
              return next
            })
            break
          case 'done':
            setMessages((prev) => {
              const next = [...prev]
              next[assistantIndex] = { ...event.message, sources: next[assistantIndex].sources }
              return next
            })
            break
          case 'error':
            setError(event.error)
            break
        }
      }
    } catch (err) {
      if (!controller.signal.aborted) {
        setError(err instanceof Error ? err.message : String(err))
      }
    } finally {
      setSending(false)
    }
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      void handleSend()
    }
  }

  const selectedPersona = personas.find((p) => p.id === personaId)

  return (
    <main className="app">
      <header className="app-header">
        <h1>kaigi</h1>
        <span className={`status status--${status}`}>backend: {status}</span>
        <select
          className="persona-select"
          value={personaId}
          onChange={(e) => setPersonaId(e.target.value)}
          disabled={personas.length === 0}
        >
          {personas.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>
      </header>

      {selectedPersona && <p className="persona-stance">{selectedPersona.stance}</p>}

      <section className="messages">
        {messages.map((m) => (
          <div key={m.id} className={`message message--${m.role}`}>
            <span className="message-role">{m.role === 'user' ? 'user' : selectedPersona?.name ?? 'assistant'}</span>
            <p className="message-content">{m.content || (sending && m.role === 'assistant' ? '…' : '')}</p>
            {m.sources && m.sources.length > 0 && (
              <ol className="sources">
                {m.sources.map((s) => (
                  <li key={s.chunkId}>
                    <a href={s.url} target="_blank" rel="noreferrer">
                      {s.title}
                    </a>
                    <span className="source-scores">
                      関連 {s.relevance.toFixed(2)} / 関心 {s.affinity >= 0 ? '+' : ''}
                      {s.affinity.toFixed(2)}
                    </span>
                  </li>
                ))}
              </ol>
            )}
          </div>
        ))}
      </section>

      {error && <p className="error">{error}</p>}

      <div className="composer">
        <textarea
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder="メッセージを入力…"
          disabled={!conversationId || sending}
        />
        <button type="button" onClick={() => void handleSend()} disabled={!conversationId || sending || !input.trim()}>
          送信
        </button>
      </div>
    </main>
  )
}

export default App
