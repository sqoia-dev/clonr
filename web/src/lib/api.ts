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
  return res.json() as Promise<T>
}

export function sseUrl(path: string): string {
  return `${BASE}${path}`
}
