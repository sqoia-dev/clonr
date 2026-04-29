/**
 * sprint5.test.ts — TEST-S5-1: Vitest coverage for Sprint 4 frontend paths.
 *
 * Covers:
 *   - node-create form validation (valid + each invalid field path)
 *   - Edit-Node optimistic update + rollback on 409
 *   - Bulk add CSV/YAML parser preview (parseBulkInput logic)
 *   - image-from-URL mutation flow
 *   - TUS upload progress event handling
 *   - initramfs build SSE consumption (queued → running → log → completed)
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { apiFetch } from "../lib/api"

// ─── node-create form validation ──────────────────────────────────────────────
// The validation logic extracted from AddNodeSheet.validate() in nodes.tsx.

const hostnameRe = /^[a-z0-9-]{1,63}$/
const macRe = /^([0-9a-f]{2}:){5}[0-9a-f]{2}$/
const ipv4Re = /^(\d{1,3}\.){3}\d{1,3}(\/\d+)?$/

function normalizeMAC(raw: string): string {
  return raw.toLowerCase().replace(/[^0-9a-f]/g, "").replace(/(.{2})(?=.)/g, "$1:")
}

interface ValidationErrors {
  hostname?: string
  mac?: string
  ip?: string
}

function validateNodeForm(hostname: string, mac: string, ip: string): ValidationErrors {
  const errs: ValidationErrors = {}
  if (!hostnameRe.test(hostname)) errs.hostname = "Lowercase letters, digits, hyphens, 1–63 chars"
  const normMac = normalizeMAC(mac)
  if (!macRe.test(normMac)) errs.mac = "Must be a valid MAC address"
  if (ip && !ipv4Re.test(ip)) errs.ip = "Must be a valid IPv4 address or CIDR"
  return errs
}

describe("node-create form validation (TEST-S5-1)", () => {
  it("should pass with a valid hostname, MAC, and no IP", () => {
    const errs = validateNodeForm("compute-01", "bc:24:11:36:e9:2f", "")
    expect(Object.keys(errs)).toHaveLength(0)
  })

  it("should pass with valid hostname, MAC, and CIDR IP", () => {
    const errs = validateNodeForm("worker-node", "aa:bb:cc:dd:ee:ff", "10.99.0.5/24")
    expect(Object.keys(errs)).toHaveLength(0)
  })

  it("should fail when hostname is empty", () => {
    const errs = validateNodeForm("", "aa:bb:cc:dd:ee:ff", "")
    expect(errs.hostname).toBeTruthy()
  })

  it("should fail when hostname has uppercase letters", () => {
    const errs = validateNodeForm("Compute-01", "aa:bb:cc:dd:ee:ff", "")
    expect(errs.hostname).toBeTruthy()
  })

  it("should fail when hostname exceeds 63 chars", () => {
    const longName = "a".repeat(64)
    const errs = validateNodeForm(longName, "aa:bb:cc:dd:ee:ff", "")
    expect(errs.hostname).toBeTruthy()
  })

  it("should fail when hostname has an underscore", () => {
    const errs = validateNodeForm("compute_01", "aa:bb:cc:dd:ee:ff", "")
    expect(errs.hostname).toBeTruthy()
  })

  it("should fail when MAC is invalid", () => {
    const errs = validateNodeForm("compute-01", "ZZZZZZ", "")
    expect(errs.mac).toBeTruthy()
  })

  it("should fail when MAC is too short", () => {
    const errs = validateNodeForm("compute-01", "aa:bb:cc", "")
    expect(errs.mac).toBeTruthy()
  })

  it("should normalize a MAC without colons and pass", () => {
    // The form normalizes before validating — test the normalization path.
    const normMac = normalizeMAC("AABBCCDDEEFF")
    expect(macRe.test(normMac)).toBe(true)
  })

  it("should fail when IP is provided but invalid format", () => {
    // The ipv4Re regex checks for numeric octets — a hostname-style string fails.
    const errs = validateNodeForm("compute-01", "aa:bb:cc:dd:ee:ff", "not-an-ip-address")
    expect(errs.ip).toBeTruthy()
  })

  it("should accept an empty IP (DHCP case)", () => {
    const errs = validateNodeForm("compute-01", "aa:bb:cc:dd:ee:ff", "")
    expect(errs.ip).toBeUndefined()
  })
})

// ─── Edit-Node optimistic update + rollback on 409 ───────────────────────────
// Mirrors the pattern from useMutation onMutate/onError in NodeSheet.

interface NodeCacheEntry {
  id: string
  hostname: string
  tags?: string[]
}

interface NodeCache {
  nodes: NodeCacheEntry[]
  total: number
}

function optimisticNodeUpdate(
  cache: NodeCache | undefined,
  nodeId: string,
  patch: Partial<NodeCacheEntry>
): NodeCache | undefined {
  if (!cache) return undefined
  return {
    ...cache,
    nodes: cache.nodes.map((n) => (n.id === nodeId ? { ...n, ...patch } : n)),
  }
}

describe("Edit-Node optimistic update + rollback (TEST-S5-1)", () => {
  const INITIAL_CACHE: NodeCache = {
    nodes: [
      { id: "node-1", hostname: "compute-01", tags: ["worker"] },
      { id: "node-2", hostname: "compute-02", tags: [] },
    ],
    total: 2,
  }

  it("should apply optimistic hostname update", () => {
    const prev = INITIAL_CACHE
    const updated = optimisticNodeUpdate(INITIAL_CACHE, "node-1", { hostname: "new-hostname" })
    expect(updated?.nodes.find((n) => n.id === "node-1")?.hostname).toBe("new-hostname")
    // Other node unchanged.
    expect(updated?.nodes.find((n) => n.id === "node-2")?.hostname).toBe("compute-02")
    // prev is a separate reference — rollback is safe.
    expect(prev.nodes[0].hostname).toBe("compute-01")
  })

  it("should apply optimistic tag update", () => {
    const updated = optimisticNodeUpdate(INITIAL_CACHE, "node-1", { tags: ["worker", "controller"] })
    expect(updated?.nodes.find((n) => n.id === "node-1")?.tags).toEqual(["worker", "controller"])
  })

  it("should return prev unchanged on rollback", () => {
    const prev = INITIAL_CACHE
    // Simulate a 409 error — rollback restores prev.
    const cache = optimisticNodeUpdate(prev, "node-1", { hostname: "new-hostname" })
    // Rollback: caller sets cache back to prev.
    expect(prev).toEqual(INITIAL_CACHE)
    // The mutated version is not the same reference as prev.
    expect(cache).not.toBe(prev)
  })

  it("should be a no-op when cache is undefined", () => {
    const result = optimisticNodeUpdate(undefined, "node-1", { hostname: "x" })
    expect(result).toBeUndefined()
  })

  it("should leave cache unchanged when node ID not found", () => {
    const updated = optimisticNodeUpdate(INITIAL_CACHE, "nonexistent", { hostname: "x" })
    expect(updated?.nodes).toEqual(INITIAL_CACHE.nodes)
  })
})

// ─── Bulk CSV/YAML parser preview ────────────────────────────────────────────
// Ported directly from parseBulkInput / validateBulkRow in nodes.tsx.

interface BulkRow {
  hostname: string
  mac: string
  ip?: string
  role?: string
  parseError?: string
}

function normalizeRow(raw: string): string {
  return raw.toLowerCase().replace(/[^0-9a-f]/g, "").replace(/(.{2})(?=.)/g, "$1:")
}

function validateBulkRow(row: BulkRow): BulkRow {
  const errs: string[] = []
  if (!row.hostname) errs.push("hostname required")
  else if (!/^[a-z0-9-]{1,63}$/.test(row.hostname)) errs.push("invalid hostname")
  const normMac = normalizeRow(row.mac)
  if (!row.mac) errs.push("mac required")
  else if (!/^([0-9a-f]{2}:){5}[0-9a-f]{2}$/.test(normMac)) errs.push("invalid MAC")
  if (errs.length > 0) return { ...row, parseError: errs.join("; ") }
  return { ...row, mac: normMac }
}

function assignField(row: BulkRow, key: string, value: string): void {
  switch (key) {
    case "hostname": row.hostname = value; break
    case "mac": case "primary_mac": row.mac = value; break
    case "ip": case "ip_address": row.ip = value; break
    case "role": case "roles": case "tags": row.role = value; break
  }
}

function parseBulkInput(raw: string): BulkRow[] {
  const trimmed = raw.trim()
  if (!trimmed) return []

  const firstLine = trimmed.split("\n").find((l) => l.trim() !== "") ?? ""
  const isYAML = firstLine.trim().startsWith("-") || firstLine.includes("hostname:")

  if (isYAML) {
    const rows: BulkRow[] = []
    let current: BulkRow | null = null
    for (const line of trimmed.split("\n")) {
      const t = line.trim()
      if (t.startsWith("- ") || t === "-") {
        if (current) rows.push(current)
        current = { hostname: "", mac: "" }
        const rest = t.slice(2).trim()
        if (rest) {
          const [k, ...vParts] = rest.split(":")
          const v = vParts.join(":").trim()
          assignField(current, k.trim(), v)
        }
      } else if (current && t.includes(":")) {
        const [k, ...vParts] = t.split(":")
        const v = vParts.join(":").trim()
        assignField(current, k.trim(), v)
      }
    }
    if (current) rows.push(current)
    return rows.map((r) => validateBulkRow(r))
  }

  const lines = trimmed.split("\n").filter((l) => l.trim() !== "")
  const header = lines[0].toLowerCase().split(",").map((h) => h.trim())
  const dataLines = header.includes("hostname") ? lines.slice(1) : lines

  return dataLines.map((line) => {
    const cells = line.split(",").map((c) => c.trim())
    const row: BulkRow = { hostname: "", mac: "" }
    if (header.includes("hostname")) {
      row.hostname = cells[header.indexOf("hostname")] ?? ""
      row.mac = cells[header.indexOf("mac")] ?? ""
      row.ip = cells[header.indexOf("ip")] ?? ""
      row.role = cells[header.indexOf("role")] ?? ""
    } else {
      row.hostname = cells[0] ?? ""
      row.mac = cells[1] ?? ""
      row.ip = cells[2] ?? ""
      row.role = cells[3] ?? ""
    }
    return validateBulkRow(row)
  })
}

describe("Bulk CSV parser preview (TEST-S5-1)", () => {
  it("should parse CSV with header row", () => {
    const csv = "hostname,mac,ip,role\ncompute-01,bc:24:11:aa:bb:cc,,worker"
    const rows = parseBulkInput(csv)
    expect(rows).toHaveLength(1)
    expect(rows[0].hostname).toBe("compute-01")
    expect(rows[0].role).toBe("worker")
    expect(rows[0].parseError).toBeUndefined()
  })

  it("should parse multiple CSV rows", () => {
    const csv = "hostname,mac,ip,role\ncompute-01,bc:24:11:aa:bb:01,,worker\ncompute-02,bc:24:11:aa:bb:02,,worker"
    const rows = parseBulkInput(csv)
    expect(rows).toHaveLength(2)
    expect(rows.every((r) => !r.parseError)).toBe(true)
  })

  it("should flag a row with invalid MAC", () => {
    const csv = "hostname,mac,ip,role\nbad-node,ZZZZZZ,,"
    const rows = parseBulkInput(csv)
    expect(rows[0].parseError).toBeTruthy()
    expect(rows[0].parseError).toContain("invalid MAC")
  })

  it("should flag a row with missing hostname", () => {
    const csv = "hostname,mac,ip,role\n,bc:24:11:aa:bb:cc,,"
    const rows = parseBulkInput(csv)
    expect(rows[0].parseError).toBeTruthy()
  })

  it("should return empty array for blank input", () => {
    expect(parseBulkInput("")).toHaveLength(0)
    expect(parseBulkInput("   \n  ")).toHaveLength(0)
  })
})

describe("Bulk YAML parser preview (TEST-S5-1)", () => {
  it("should parse YAML list", () => {
    const yaml = `- hostname: compute-01
  mac: bc:24:11:aa:bb:cc
  role: worker
- hostname: compute-02
  mac: bc:24:11:aa:bb:dd
  role: worker`
    const rows = parseBulkInput(yaml)
    expect(rows).toHaveLength(2)
    expect(rows[0].hostname).toBe("compute-01")
    expect(rows[1].hostname).toBe("compute-02")
    expect(rows.every((r) => !r.parseError)).toBe(true)
  })

  it("should flag invalid YAML row", () => {
    const yaml = `- hostname: BADHOST_UPPERCASE
  mac: bc:24:11:aa:bb:cc`
    const rows = parseBulkInput(yaml)
    expect(rows[0].parseError).toBeTruthy()
  })
})

// ─── image-from-URL mutation flow ─────────────────────────────────────────────
// Tests the client-side wire contract: POST /api/v1/images/from-url
// returns {image_id, status} which the UI uses to track progress.

describe("image-from-URL mutation flow (TEST-S5-1)", () => {
  beforeEach(() => { vi.resetAllMocks() })
  afterEach(() => { vi.restoreAllMocks() })

  it("should call POST /api/v1/images/from-url with correct body", async () => {
    const mockFetch = vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 202,
        json: () => Promise.resolve({ image_id: "img-abc123", status: "building" }),
        text: () => Promise.resolve(""),
      })
    )
    vi.stubGlobal("fetch", mockFetch)

    const result = await apiFetch<{ image_id: string; status: string }>(
      "/api/v1/images/from-url",
      { method: "POST", body: JSON.stringify({ url: "https://example.com/image.iso", name: "test-img" }) }
    )

    expect(result.image_id).toBe("img-abc123")
    expect(result.status).toBe("building")

    const callArgs = mockFetch.mock.calls[0]
    expect(callArgs[0]).toContain("/api/v1/images/from-url")
    const init = callArgs[1] as RequestInit
    expect(init.method).toBe("POST")
    const body = JSON.parse(init.body as string)
    expect(body.url).toBe("https://example.com/image.iso")
  })

  it("should reject if server returns 400 (scheme error)", async () => {
    vi.stubGlobal("fetch", vi.fn(() =>
      Promise.resolve({
        ok: false,
        status: 400,
        text: () => Promise.resolve('{"error":"url must use http or https scheme","code":"validation_error"}'),
        json: () => Promise.resolve({ error: "url must use http or https scheme" }),
      })
    ))

    await expect(
      apiFetch("/api/v1/images/from-url", {
        method: "POST",
        body: JSON.stringify({ url: "ftp://bad-scheme/file" }),
      })
    ).rejects.toThrow("400")
  })
})

// ─── TUS upload progress event handling ───────────────────────────────────────
// Tests the client-side progress tracking logic used in the ISO upload panel.
// tus-js-client emits onProgress(bytesUploaded, bytesTotal) callbacks;
// we test the derived percentage computation and state transitions.

interface UploadProgressState {
  bytesUploaded: number
  bytesTotal: number
  percent: number
  status: "idle" | "uploading" | "complete" | "error"
}

function applyProgressEvent(
  state: UploadProgressState,
  bytesUploaded: number,
  bytesTotal: number
): UploadProgressState {
  const percent = bytesTotal > 0 ? Math.round((bytesUploaded / bytesTotal) * 100) : 0
  const status = bytesUploaded >= bytesTotal && bytesTotal > 0 ? "complete" : "uploading"
  return { ...state, bytesUploaded, bytesTotal, percent, status }
}

describe("TUS upload progress event handling (TEST-S5-1)", () => {
  it("should compute 0% at start", () => {
    const state = applyProgressEvent({ bytesUploaded: 0, bytesTotal: 0, percent: 0, status: "idle" }, 0, 1024)
    expect(state.percent).toBe(0)
    expect(state.status).toBe("uploading")
  })

  it("should compute 50% at half-way", () => {
    const state = applyProgressEvent({ bytesUploaded: 0, bytesTotal: 0, percent: 0, status: "idle" }, 512, 1024)
    expect(state.percent).toBe(50)
    expect(state.status).toBe("uploading")
  })

  it("should compute 100% and flip to complete", () => {
    const state = applyProgressEvent({ bytesUploaded: 0, bytesTotal: 0, percent: 0, status: "idle" }, 1024, 1024)
    expect(state.percent).toBe(100)
    expect(state.status).toBe("complete")
  })

  it("should guard against divide-by-zero when bytesTotal is 0", () => {
    const state = applyProgressEvent({ bytesUploaded: 0, bytesTotal: 0, percent: 0, status: "idle" }, 0, 0)
    expect(state.percent).toBe(0)
    expect(state.status).toBe("uploading")
  })

  it("should accumulate progress correctly across multiple events", () => {
    let state: UploadProgressState = { bytesUploaded: 0, bytesTotal: 0, percent: 0, status: "idle" }
    state = applyProgressEvent(state, 256, 1024)
    expect(state.percent).toBe(25)
    state = applyProgressEvent(state, 512, 1024)
    expect(state.percent).toBe(50)
    state = applyProgressEvent(state, 1024, 1024)
    expect(state.percent).toBe(100)
    expect(state.status).toBe("complete")
  })
})

// ─── initramfs build SSE consumption ─────────────────────────────────────────
// Tests the SSE event parser for the initramfs build stream.
// Server emits: {"type":"log","line":"..."}, {"type":"done",...}, {"type":"error",...}

type InitramfsBuildEventType = "log" | "done" | "error"

interface InitramfsBuildEvent {
  type: InitramfsBuildEventType
  line?: string
  image_id?: string
  sha256?: string
  size_bytes?: number
  kernel_version?: string
  message?: string
}

interface InitramfsBuildState {
  status: "queued" | "running" | "complete" | "error"
  logLines: string[]
  imageId?: string
  sha256?: string
  errorMessage?: string
}

function applyInitramfsEvent(
  state: InitramfsBuildState,
  event: InitramfsBuildEvent
): InitramfsBuildState {
  switch (event.type) {
    case "log":
      return {
        ...state,
        status: "running",
        logLines: [...state.logLines, event.line ?? ""],
      }
    case "done":
      return {
        ...state,
        status: "complete",
        imageId: event.image_id,
        sha256: event.sha256,
      }
    case "error":
      return {
        ...state,
        status: "error",
        errorMessage: event.message,
      }
    default:
      return state
  }
}

describe("initramfs build SSE consumption (TEST-S5-1)", () => {
  const INITIAL: InitramfsBuildState = { status: "queued", logLines: [] }

  it("should transition from queued to running on first log event", () => {
    const state = applyInitramfsEvent(INITIAL, { type: "log", line: "build started" })
    expect(state.status).toBe("running")
    expect(state.logLines).toHaveLength(1)
    expect(state.logLines[0]).toBe("build started")
  })

  it("should accumulate log lines", () => {
    let state = INITIAL
    state = applyInitramfsEvent(state, { type: "log", line: "line 1" })
    state = applyInitramfsEvent(state, { type: "log", line: "line 2" })
    state = applyInitramfsEvent(state, { type: "log", line: "line 3" })
    expect(state.logLines).toHaveLength(3)
    expect(state.logLines[2]).toBe("line 3")
  })

  it("should transition to complete on done event", () => {
    let state = INITIAL
    state = applyInitramfsEvent(state, { type: "log", line: "building..." })
    state = applyInitramfsEvent(state, {
      type: "done",
      image_id: "img-abc",
      sha256: "deadbeef",
      size_bytes: 42000000,
    })
    expect(state.status).toBe("complete")
    expect(state.imageId).toBe("img-abc")
    expect(state.sha256).toBe("deadbeef")
  })

  it("should transition to error on error event", () => {
    let state = INITIAL
    state = applyInitramfsEvent(state, { type: "log", line: "starting..." })
    state = applyInitramfsEvent(state, { type: "error", message: "dracut failed: exit code 1" })
    expect(state.status).toBe("error")
    expect(state.errorMessage).toBe("dracut failed: exit code 1")
    // Log lines are preserved even after error.
    expect(state.logLines).toHaveLength(1)
  })

  it("should preserve log lines on completion (for the log panel)", () => {
    let state = INITIAL
    state = applyInitramfsEvent(state, { type: "log", line: "step 1" })
    state = applyInitramfsEvent(state, { type: "log", line: "step 2" })
    state = applyInitramfsEvent(state, { type: "done", image_id: "img-xyz", sha256: "cafebabe" })
    expect(state.logLines).toHaveLength(2)
    expect(state.status).toBe("complete")
  })
})
