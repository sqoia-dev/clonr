const BASE = import.meta.env.VITE_API_BASE ?? ""

// Session-expired event — dispatched when any API call returns 401.
// useSession() listens to this and flips the session state to `unauthed`.
export const SESSION_EXPIRED_EVENT = "clustr:session-expired"

/**
 * apiFetch wraps fetch with:
 *  - Always `credentials: "include"` (cookie-based sessions, no API keys from web UI)
 *  - On 401: dispatch SESSION_EXPIRED_EVENT then throw
 *  - On non-ok: throw with status + body text
 */
export async function apiFetch<T>(
  path: string,
  init?: RequestInit
): Promise<T> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    Accept: "application/json",
    ...(init?.headers as Record<string, string>),
  }
  const res = await fetch(`${BASE}${path}`, {
    ...init,
    headers,
    credentials: "include",
  })
  if (res.status === 401) {
    window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT))
    const text = await res.text().catch(() => "")
    throw new Error(`401: ${text}`)
  }
  if (!res.ok) {
    const text = await res.text().catch(() => "")
    throw new Error(`${res.status}: ${text}`)
  }
  // 204 No Content — by spec the body is absent; never call .json()/.text().
  if (res.status === 204) return undefined as T
  // Explicit empty body declared by server.
  if (res.headers.get("Content-Length") === "0") return undefined as T
  // Read as text first so we can handle an empty body without a SyntaxError,
  // and emit a descriptive error when the server sends non-JSON unexpectedly.
  const text = await res.text()
  if (text === "") return undefined as T
  try {
    return JSON.parse(text) as T
  } catch {
    throw new Error(`apiFetch: server returned non-JSON body for ${path}: ${text.slice(0, 200)}`)
  }
}

export function sseUrl(path: string): string {
  return `${BASE}${path}`
}

// wsUrl returns a WebSocket URL for the given path.
// In dev (Vite proxy) the host is localhost:5173 but the proxy forwards to :8080.
// In production the server and UI share the same origin.
export function wsUrl(path: string): string {
  const base = BASE || window.location.origin
  const url = new URL(path, base)
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:"
  return url.toString()
}
