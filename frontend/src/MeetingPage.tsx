import { useEffect, useRef, useState } from 'react'
import { createMeeting, listAgents, sendTurn, type Agent, type Turn } from './api/client'
import { AppHeader } from './AppHeader'
import { Markdown } from './Markdown'

// DisplayTurn extends the persisted Turn shape with in-flight streaming
// state (a turn only exists server-side once speaker_end arrives; until
// then this is a placeholder the UI fills in token by token).
type DisplayTurn = Turn & { pending?: boolean; failed?: boolean }

// RoundMarker is a synthetic list entry — not a Turn — used to render the
// "── Round N ──" separators between rounds without threading round
// boundaries through Turn itself.
type ListItem = { kind: 'turn'; turn: DisplayTurn } | { kind: 'round'; round: number }

function MeetingPage() {
  const [agents, setAgents] = useState<Agent[]>([])
  const [selectedSlugs, setSelectedSlugs] = useState<Set<string>>(new Set())
  const [rounds, setRounds] = useState(2)
  const [meetingId, setMeetingId] = useState<string>('')
  const [items, setItems] = useState<ListItem[]>([])
  const [input, setInput] = useState('')
  const [sending, setSending] = useState(false)
  const [activeSpeakerSlug, setActiveSpeakerSlug] = useState<string>('')
  const [error, setError] = useState('')

  // Guards against React 19 StrictMode's double-invoked effects, and lets a
  // still-streaming turn be canceled if the component unmounts.
  const abortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    const controller = new AbortController()
    listAgents(controller.signal)
      .then((list) => setAgents(list))
      .catch(() => {
        if (!controller.signal.aborted) setError('ペルソナの取得に失敗しました')
      })
    return () => controller.abort()
  }, [])

  function toggleSlug(slug: string) {
    setSelectedSlugs((prev) => {
      const next = new Set(prev)
      if (next.has(slug)) next.delete(slug)
      else next.add(slug)
      return next
    })
  }

  // Starting a meeting is a deliberate user action (the button below), never
  // an effect — creating it as a side effect of participant selection would
  // double-fire under React 19 StrictMode's double-invoked effects, exactly
  // the trap the pre-meeting single-persona version's own comment warned
  // about for its analogous "selecting a persona starts a conversation" effect.
  async function handleStart() {
    const content = input.trim()
    const slugs = [...selectedSlugs]
    if (slugs.length === 0 || !content || sending) return

    setError('')
    setSending(true)
    try {
      // The meeting's topic is the human's opening message itself — there
      // is no separate topic field, matching the plan's UX Design where a
      // single input serves both purposes.
      const meeting = await createMeeting(content, slugs)
      setMeetingId(meeting.id)
      setItems([])
      setInput('')
      await runTurn(meeting.id, content, rounds)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setSending(false)
    }
  }

  async function handleContinue() {
    const content = input.trim()
    if (!content || !meetingId || sending) return
    setInput('')
    setError('')
    setSending(true)
    await runTurn(meetingId, content, rounds)
  }

  async function runTurn(id: string, content: string, roundCount: number) {
    const controller = new AbortController()
    abortRef.current = controller

    // currentIndex tracks the in-flight turn's position in items so token
    // events can append without a full re-scan — same pattern the
    // single-persona version used for its one assistant slot, generalized
    // to whichever speaker is currently active.
    let currentIndex = -1

    try {
      for await (const event of sendTurn(id, content, roundCount, controller.signal)) {
        switch (event.type) {
          case 'speaker_start':
            setActiveSpeakerSlug(event.personaSlug)
            setItems((prev) => {
              currentIndex = prev.length
              return [
                ...prev,
                {
                  kind: 'turn',
                  turn: {
                    id: `pending-${event.personaSlug}-${Date.now()}`,
                    seq: -1,
                    round: event.round,
                    role: 'persona',
                    speakerSlug: event.personaSlug,
                    speakerName: event.personaName,
                    content: '',
                    createdAt: '',
                    pending: true,
                  },
                },
              ]
            })
            break
          case 'sources':
            setItems((prev) => {
              const next = [...prev]
              const item = next[currentIndex]
              if (item?.kind === 'turn') {
                next[currentIndex] = { kind: 'turn', turn: { ...item.turn, citations: event.citations } }
              }
              return next
            })
            break
          case 'token':
            setItems((prev) => {
              const next = [...prev]
              const item = next[currentIndex]
              if (item?.kind === 'turn') {
                next[currentIndex] = {
                  kind: 'turn',
                  turn: { ...item.turn, content: item.turn.content + event.text },
                }
              }
              return next
            })
            break
          case 'speaker_end':
            setItems((prev) => {
              const next = [...prev]
              const item = next[currentIndex]
              if (item?.kind === 'turn') {
                next[currentIndex] = { kind: 'turn', turn: { ...event.turn, citations: item.turn.citations } }
              }
              return next
            })
            setActiveSpeakerSlug('')
            break
          case 'speaker_error':
            setItems((prev) => {
              const next = [...prev]
              const item = next[currentIndex]
              if (item?.kind === 'turn') {
                next[currentIndex] = {
                  kind: 'turn',
                  turn: { ...item.turn, pending: false, failed: true, content: event.error },
                }
              }
              return next
            })
            setActiveSpeakerSlug('')
            break
          case 'round_end':
            setItems((prev) => [...prev, { kind: 'round', round: event.round + 1 }])
            break
          case 'error':
            setError(event.error)
            break
          case 'done':
            break
        }
      }
    } catch (err) {
      if (!controller.signal.aborted) {
        setError(err instanceof Error ? err.message : String(err))
      }
    } finally {
      setSending(false)
      setActiveSpeakerSlug('')
    }
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      void (meetingId ? handleContinue() : handleStart())
    }
  }

  const presentCount = agents.filter((a) => a.present).length

  return (
    <main className="app">
      <AppHeader presence={{ present: presentCount, total: agents.length }} />

      {!meetingId && (
        <section className="setup">
          <div className="participants">
            {agents.map((a) => (
              <label key={a.slug} className={`participant-chip ${a.present ? '' : 'participant-chip--absent'}`}>
                <input
                  type="checkbox"
                  checked={selectedSlugs.has(a.slug)}
                  disabled={!a.present}
                  onChange={() => toggleSlug(a.slug)}
                />
                {a.name}
                {!a.present && <span className="absent-badge">不在</span>}
              </label>
            ))}
          </div>
          <label className="rounds-select">
            ラウンド:
            <select value={rounds} onChange={(e) => setRounds(Number(e.target.value))}>
              {[1, 2, 3].map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </select>
          </label>
        </section>
      )}

      <section className="messages">
        {items.map((item, i) =>
          item.kind === 'round' ? (
            <div key={`round-${item.round}-${i}`} className="round-marker">
              <hr />
              <span>Round {item.round}</span>
              <hr />
            </div>
          ) : (
            <div
              key={item.turn.id}
              className={`message message--${item.turn.role} ${item.turn.failed ? 'message--failed' : ''} ${
                activeSpeakerSlug === item.turn.speakerSlug && item.turn.pending ? 'message--active' : ''
              }`}
            >
              <span className="message-role">{item.turn.role === 'user' ? 'user' : item.turn.speakerName}</span>
              {item.turn.role === 'persona' && !item.turn.failed && item.turn.content ? (
                <Markdown streaming={!!item.turn.pending}>{item.turn.content}</Markdown>
              ) : (
                <div className="message-content">{item.turn.content || (item.turn.pending ? '…' : '')}</div>
              )}
              {item.turn.citations && item.turn.citations.length > 0 && (
                <ol className="sources">
                  {item.turn.citations.map((c) => (
                    <li key={c.chunkId}>
                      <a href={c.url} target="_blank" rel="noreferrer">
                        {c.title}
                      </a>
                      <span className="source-scores">
                        関連 {c.relevance.toFixed(2)} / 関心 {c.affinity >= 0 ? '+' : ''}
                        {c.affinity.toFixed(2)}
                      </span>
                    </li>
                  ))}
                </ol>
              )}
            </div>
          ),
        )}
      </section>

      {error && <p className="error">{error}</p>}

      <div className="composer">
        <textarea
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder={meetingId ? 'メッセージを入力…' : '議題を入力…'}
          disabled={sending || (!meetingId && selectedSlugs.size === 0)}
        />
        <button
          type="button"
          onClick={() => void (meetingId ? handleContinue() : handleStart())}
          disabled={sending || !input.trim() || (!meetingId && selectedSlugs.size === 0)}
        >
          {meetingId ? '送信' : '開始'}
        </button>
      </div>
    </main>
  )
}

export default MeetingPage
