export type Health = { status: string }
export type Hello = { message: string }

async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(path, { signal, headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`${path} responded ${res.status} ${res.statusText}`)
  }
  return (await res.json()) as T
}

export const getHealth = (signal?: AbortSignal) => getJSON<Health>('/api/health', signal)

export const getHello = (name: string, signal?: AbortSignal) =>
  getJSON<Hello>(`/api/hello?name=${encodeURIComponent(name)}`, signal)
