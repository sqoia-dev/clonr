/**
 * sseReconnectDelay — jittered exponential backoff for SSE reconnects.
 *
 * Attempt 1: 750–1250ms   (1s  ± 25%)
 * Attempt 2: 1500–2500ms  (2s  ± 25%)
 * Attempt 3: 3000–5000ms  (4s  ± 25%)
 * Attempt 4: 6000–10000ms (8s  ± 25%)
 * Attempt 5+: 22500–37500ms (30s ± 25%)
 *
 * Jitter prevents thundering-herd reconnects when a server restart kicks
 * many clients simultaneously.
 *
 * @param attempt - 1-based reconnect attempt number
 * @returns delay in milliseconds
 */
export function sseReconnectDelay(attempt: number): number {
  const bases = [1000, 2000, 4000, 8000, 30000]
  const base = bases[Math.min(attempt - 1, bases.length - 1)]
  // ±25% jitter: multiply by a value in [0.75, 1.25)
  const jitter = 0.75 + Math.random() * 0.5
  return Math.round(base * jitter)
}
