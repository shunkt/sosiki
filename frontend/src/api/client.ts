export type Health = { status: string; db?: string }

export type Skill = { id: string; name: string; tags: string[] }
export type Agent = {
  slug: string
  name: string
  personaId: string
  present: boolean
  skills: Skill[]
}

export type Citation = {
  chunkId: string
  documentId: string
  title: string
  url: string
  relevance: number
  affinity: number
}

export type Turn = {
  id: string
  seq: number
  round: number
  role: 'user' | 'persona'
  speakerSlug: string
  speakerName: string
  content: string
  citations?: Citation[]
  createdAt: string
}

export type Participant = { slug: string; name: string; speakingOrder: number }
export type Meeting = { id: string; topic: string; participants: Participant[]; turns: Turn[] }

// MeetingEvent mirrors meeting.Event's JSON shape (backend/internal/meeting/meeting.go).
// personaSlug/personaName identify which participant an event is about — a
// meeting has many speakers, unlike the single-persona chat.Event this
// replaced.
export type MeetingEvent =
  | { type: 'speaker_start'; round: number; personaSlug: string; personaName: string }
  | { type: 'sources'; personaSlug: string; citations: Citation[] }
  | { type: 'token'; personaSlug: string; text: string }
  | { type: 'speaker_end'; personaSlug: string; personaName: string; turn: Turn }
  | { type: 'speaker_error'; personaSlug: string; personaName: string; error: string }
  | { type: 'round_end'; round: number }
  | { type: 'done' }
  | { type: 'error'; error: string }

async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(path, { signal, headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`${path} responded ${res.status} ${res.statusText}`)
  }
  return (await res.json()) as T
}

async function postJSON<T>(path: string, body: unknown, signal?: AbortSignal): Promise<T> {
  const res = await fetch(path, {
    method: 'POST',
    signal,
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const errBody = (await res.json().catch(() => null)) as { error?: string } | null
    throw new Error(errBody?.error ?? `${path} responded ${res.status} ${res.statusText}`)
  }
  return (await res.json()) as T
}

export const getHealth = (signal?: AbortSignal) => getJSON<Health>('/api/health', signal)

export const listAgents = (signal?: AbortSignal) => getJSON<Agent[]>('/api/personas', signal)

export const createMeeting = (topic: string, personaSlugs: string[], signal?: AbortSignal) =>
  postJSON<Meeting>('/api/meetings', { topic, personaSlugs }, signal)

export const getMeeting = (id: string, signal?: AbortSignal) =>
  getJSON<Meeting>(`/api/meetings/${encodeURIComponent(id)}`, signal)

/**
 * sendTurn streams a meeting turn as an async generator of MeetingEvent.
 *
 * Uses fetch + ReadableStream rather than EventSource: EventSource can only
 * issue GET requests, and this endpoint needs a POST body. The frame parser
 * itself is unchanged from the pre-A2A single-persona client — only the
 * event payload shapes differ (see MeetingEvent above).
 */
export async function* sendTurn(
  meetingId: string,
  content: string,
  rounds: number,
  signal?: AbortSignal,
): AsyncGenerator<MeetingEvent> {
  const res = await fetch(`/api/meetings/${encodeURIComponent(meetingId)}/turns`, {
    method: 'POST',
    signal,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content, rounds }),
  })
  if (!res.ok || !res.body) {
    throw new Error(`send turn responded ${res.status} ${res.statusText}`)
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  // SSE frames are separated by a blank line, but chunk boundaries from the
  // network never align with frame boundaries — buffer until a full frame
  // ("...\n\n") has arrived before parsing it.
  let buffer = ''

  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })

      let frameEnd: number
      while ((frameEnd = buffer.indexOf('\n\n')) !== -1) {
        const frame = buffer.slice(0, frameEnd)
        buffer = buffer.slice(frameEnd + 2)
        const event = parseSSEFrame(frame)
        if (event) yield event
      }
    }
  } finally {
    reader.releaseLock()
  }
}

function parseSSEFrame(frame: string): MeetingEvent | null {
  let data: string | null = null
  for (const line of frame.split('\n')) {
    if (line.startsWith('data: ')) {
      data = line.slice('data: '.length)
    }
    // Lines starting with ":" (keep-alive comments) and "event: " carry no
    // information the client needs beyond what's already encoded in data.
  }
  if (data === null) return null
  return JSON.parse(data) as MeetingEvent
}
