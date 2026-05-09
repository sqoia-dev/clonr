/**
 * hostlist.ts — pyhostlist-compatible range syntax for node selectors.
 *
 * Supports: compute[01-12,20-25,30]
 * Round-trip-compatible with internal/selector/hostlist.go (Richard's Bundle A backend).
 *
 * Exported:
 *   expandHostlist(pattern)  → string[]   e.g. "node[01-03]" → ["node01","node02","node03"]
 *   compressHostnames(names) → string     e.g. ["node01","node02","node03"] → "node[01-03]"
 */

/**
 * expandHostlist expands a hostlist pattern into an array of hostnames.
 *
 * Supports:
 *   - Single hostname (no brackets): "compute01" → ["compute01"]
 *   - Bracket range: "node[01-03]" → ["node01","node02","node03"]
 *   - Comma-separated list inside brackets: "node[01,03,05]" → ["node01","node03","node05"]
 *   - Mixed ranges and items: "node[01-03,10,20-21]"
 *   - Zero-padded ranges: "node[001-003]" preserves padding width
 *
 * Throws for malformed input: empty brackets, unmatched brackets, invalid range.
 */
export function expandHostlist(pattern: string): string[] {
  const trimmed = pattern.trim()
  if (!trimmed) return []

  const openIdx = trimmed.indexOf("[")
  const closeIdx = trimmed.lastIndexOf("]")

  // No brackets — plain hostname.
  if (openIdx === -1 && closeIdx === -1) {
    return [trimmed]
  }

  // Validate brackets.
  if (openIdx === -1 || closeIdx === -1 || closeIdx < openIdx) {
    throw new Error(`malformed hostlist: unmatched bracket in "${pattern}"`)
  }

  const prefix = trimmed.slice(0, openIdx)
  const suffix = trimmed.slice(closeIdx + 1)
  const inner = trimmed.slice(openIdx + 1, closeIdx)

  if (!inner) {
    throw new Error(`malformed hostlist: empty brackets in "${pattern}"`)
  }

  // Parse comma-separated segments, each is either a single item or a range.
  const segments = inner.split(",").map((s) => s.trim()).filter(Boolean)
  if (segments.length === 0) {
    throw new Error(`malformed hostlist: empty bracket content in "${pattern}"`)
  }

  const expanded: string[] = []

  for (const seg of segments) {
    if (seg.includes("-")) {
      const dashIdx = seg.indexOf("-")
      const startStr = seg.slice(0, dashIdx)
      const endStr = seg.slice(dashIdx + 1)

      if (!startStr || !endStr) {
        throw new Error(`malformed hostlist: invalid range "${seg}" in "${pattern}"`)
      }

      // Validate that both tokens are purely digits before parseInt.
      // parseInt("01a", 10) returns 1, silently accepting malformed input.
      const digitsOnly = /^\d+$/
      if (!digitsOnly.test(startStr) || !digitsOnly.test(endStr)) {
        throw new Error(`malformed hostlist: non-numeric range "${seg}" in "${pattern}"`)
      }

      const startNum = parseInt(startStr, 10)
      const endNum = parseInt(endStr, 10)

      if (isNaN(startNum) || isNaN(endNum)) {
        throw new Error(`malformed hostlist: non-numeric range "${seg}" in "${pattern}"`)
      }

      if (endNum < startNum) {
        throw new Error(`malformed hostlist: range end < start in "${seg}" in "${pattern}"`)
      }

      // Determine zero-padding width from the start token.
      const padLen = startStr.length > 1 && startStr[0] === "0" ? startStr.length : 0

      for (let i = startNum; i <= endNum; i++) {
        const numStr = padLen > 0 ? String(i).padStart(padLen, "0") : String(i)
        expanded.push(prefix + numStr + suffix)
      }
    } else {
      // Single item — preserve as-is (could be zero-padded like "007").
      expanded.push(prefix + seg + suffix)
    }
  }

  return expanded
}

/**
 * compressHostnames compresses a list of hostnames into a hostlist pattern.
 *
 * Groups names that share a common alphabetic prefix and have a numeric suffix.
 * Names that don't fit into any numeric run are left as-is.
 *
 * Example: ["node01","node02","node03","node10"] → "node[01-03,10]"
 */
export function compressHostnames(names: string[]): string {
  if (names.length === 0) return ""
  if (names.length === 1) return names[0]

  // Group by prefix (everything before the trailing digit run).
  const groups = new Map<string, Array<{ suffix: string; num: number }>>()

  for (const name of names) {
    const match = name.match(/^(.*?)(\d+)$/)
    if (match) {
      const prefix = match[1]
      const suffix = match[2]
      const num = parseInt(suffix, 10)
      if (!groups.has(prefix)) groups.set(prefix, [])
      groups.get(prefix)!.push({ suffix, num })
    } else {
      // No trailing digits — treat as own group.
      if (!groups.has(name)) groups.set(name, [])
    }
  }

  const parts: string[] = []

  for (const [prefix, items] of groups) {
    if (items.length === 0) {
      // Plain name without numeric suffix.
      parts.push(prefix)
      continue
    }

    // Sort by numeric value.
    items.sort((a, b) => a.num - b.num)

    // Build runs.
    const padLen = items[0].suffix.length > 1 && items[0].suffix[0] === "0" ? items[0].suffix.length : 0

    const runs: string[] = []
    let runStart = items[0]
    let runEnd = items[0]

    for (let i = 1; i < items.length; i++) {
      const prev = items[i - 1]
      const cur = items[i]
      if (cur.num === prev.num + 1) {
        runEnd = cur
      } else {
        runs.push(formatRun(runStart, runEnd, padLen))
        runStart = cur
        runEnd = cur
      }
    }
    runs.push(formatRun(runStart, runEnd, padLen))

    if (runs.length === 1 && items.length === 1) {
      // Single item — no brackets needed.
      parts.push(prefix + items[0].suffix)
    } else {
      parts.push(`${prefix}[${runs.join(",")}]`)
    }
  }

  return parts.join(",")
}

function formatRun(
  start: { suffix: string; num: number },
  end: { suffix: string; num: number },
  padLen: number,
): string {
  const fmt = (n: number) => (padLen > 0 ? String(n).padStart(padLen, "0") : String(n))
  if (start.num === end.num) return fmt(start.num)
  return `${fmt(start.num)}-${fmt(end.num)}`
}
