const BASE = import.meta.env.VITE_API_BASE ?? ""

export async function apiFetch<T>(
  path: string,
  apiKey: string | null,
  init?: RequestInit
): Promise<T> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    Accept: "application/json",
    ...(init?.headers as Record<string, string>),
  }
  if (apiKey) {
    headers["Authorization"] = `Bearer ${apiKey}`
  }
  const res = await fetch(`${BASE}${path}`, { ...init, headers })
  if (!res.ok) {
    const text = await res.text().catch(() => "")
    throw new Error(`${res.status}: ${text}`)
  }
  return res.json() as Promise<T>
}

export function sseUrl(path: string): string {
  return `${BASE}${path}`
}
