export type Health = { status: string; embed?: string; rerank?: string }

export type Interest = { topic: string; weight: number }
export type Verbosity = 'concise' | 'balanced' | 'detailed'
export type Persona = {
  id: string
  name: string
  stance: string
  verbosity: Verbosity
  skepticism: number
  interests: Interest[]
}

export type Message = { id: string; role: 'user' | 'assistant'; content: string; createdAt: string }
export type Conversation = { id: string; personaId: string; title: string; messages: Message[] }

export type Source = {
  chunkId: string
  documentId: string
  title: string
  url: string
  relevance: number
  affinity: number
}

export type ChatEvent =
  | { type: 'token'; text: string }
  | { type: 'sources'; sources: Source[] }
  | { type: 'done'; message: Message }
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
    throw new Error(`${path} responded ${res.status} ${res.statusText}`)
  }
  return (await res.json()) as T
}

export const getHealth = (signal?: AbortSignal) => getJSON<Health>('/api/health', signal)

export const listPersonas = (signal?: AbortSignal) => getJSON<Persona[]>('/api/personas', signal)

export const createConversation = (personaId: string, title = '', signal?: AbortSignal) =>
  postJSON<Conversation>('/api/conversations', { personaId, title }, signal)

export const getConversation = (id: string, signal?: AbortSignal) =>
  getJSON<Conversation>(`/api/conversations/${encodeURIComponent(id)}`, signal)

/**
 * sendMessage streams the reply as an async generator of ChatEvent.
 *
 * Uses fetch + ReadableStream rather than EventSource: EventSource can only
 * issue GET requests, and this endpoint needs a POST body.
 */
export async function* sendMessage(
  conversationId: string,
  content: string,
  signal?: AbortSignal,
): AsyncGenerator<ChatEvent> {
  const res = await fetch(`/api/conversations/${encodeURIComponent(conversationId)}/messages`, {
    method: 'POST',
    signal,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content }),
  })
  if (!res.ok || !res.body) {
    throw new Error(`send message responded ${res.status} ${res.statusText}`)
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

function parseSSEFrame(frame: string): ChatEvent | null {
  let data: string | null = null
  for (const line of frame.split('\n')) {
    if (line.startsWith('data: ')) {
      data = line.slice('data: '.length)
    }
    // Lines starting with ":" (keep-alive comments) and "event: " carry no
    // information the client needs beyond what's already encoded in data.
  }
  if (data === null) return null
  return JSON.parse(data) as ChatEvent
}
