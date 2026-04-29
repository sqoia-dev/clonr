import * as React from "react"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { Copy, Check, Plus, Trash2, Key, Server, ShieldCheck, LogOut, Eye, EyeOff } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useSession } from "@/contexts/auth"
import { apiFetch } from "@/lib/api"
import { toast } from "@/hooks/use-toast"
import type { ListAPIKeysResponse, CreateAPIKeyResponse, HealthResponse, ListGPGKeysResponse, GPGKey } from "@/lib/types"
import { cn } from "@/lib/utils"
import { formatDistanceToNow } from "date-fns"

function relativeTime(iso?: string | null) {
  if (!iso) return "—"
  try { return formatDistanceToNow(new Date(iso), { addSuffix: true }) } catch { return "—" }
}

// ─── Settings page ────────────────────────────────────────────────────────────

export function SettingsPage() {
  const { setUnauthed } = useSession()

  async function handleLogout() {
    try {
      await apiFetch("/api/v1/auth/logout", { method: "POST" })
    } catch {
      // Clear session regardless.
    }
    setUnauthed()
  }

  return (
    <div className="max-w-3xl mx-auto p-8 space-y-10">
      <div>
        <h1 className="text-xl font-semibold">Settings</h1>
        <p className="text-sm text-muted-foreground mt-1">Server configuration and API key management</p>
      </div>

      <APIKeysSection />
      <ServerConfigSection />
      <GPGKeysSection />

      {/* SET-5: Logout */}
      <section className="border-t border-border pt-8">
        <h2 className="text-sm font-medium mb-4 flex items-center gap-2">
          <LogOut className="h-4 w-4" /> Session
        </h2>
        <Button variant="outline" onClick={handleLogout} className="text-destructive border-destructive/40 hover:bg-destructive/10">
          Sign out
        </Button>
      </section>
    </div>
  )
}

// ─── API Keys section (SET-2) ─────────────────────────────────────────────────

function APIKeysSection() {
  const qc = useQueryClient()
  const [showCreate, setShowCreate] = React.useState(false)
  const [label, setLabel] = React.useState("")
  const [revokeConfirm, setRevokeConfirm] = React.useState<string | null>(null)
  const [revokeLabel, setRevokeLabel] = React.useState("")
  const [newRawKey, setNewRawKey] = React.useState<string | null>(null)
  const [copiedKey, setCopiedKey] = React.useState(false)
  const [showKey, setShowKey] = React.useState(false)

  const { data } = useQuery<ListAPIKeysResponse>({
    queryKey: ["api-keys"],
    queryFn: () => apiFetch<ListAPIKeysResponse>("/api/v1/admin/api-keys"),
    staleTime: 10000,
  })

  const createMutation = useMutation({
    mutationFn: () =>
      apiFetch<CreateAPIKeyResponse>("/api/v1/admin/api-keys", {
        method: "POST",
        body: JSON.stringify({ scope: "admin", label }),
      }),
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ["api-keys"] })
      setNewRawKey(res.key)
      setLabel("")
      setShowCreate(false)
    },
    onError: (err) => {
      toast({ variant: "destructive", title: "Failed to create key", description: String(err) })
    },
  })

  const revokeMutation = useMutation({
    mutationFn: (id: string) =>
      apiFetch(`/api/v1/admin/api-keys/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["api-keys"] })
      setRevokeConfirm(null)
      setRevokeLabel("")
      toast({ title: "API key revoked" })
    },
    onError: (err) => {
      toast({ variant: "destructive", title: "Failed to revoke key", description: String(err) })
    },
  })

  const keys = data?.api_keys ?? []

  function copyKey() {
    if (!newRawKey) return
    navigator.clipboard.writeText(newRawKey).then(() => {
      setCopiedKey(true)
      setTimeout(() => setCopiedKey(false), 2000)
    })
  }

  return (
    <section>
      <h2 className="text-sm font-medium mb-4 flex items-center gap-2">
        <Key className="h-4 w-4" /> API Keys
      </h2>

      {/* New key reveal */}
      {newRawKey && (
        <div className="mb-4 rounded-md border border-border bg-card p-4 space-y-2">
          <p className="text-sm font-medium">New API key — copy it now. It won't be shown again.</p>
          <div className="flex items-center gap-2">
            <div className="relative flex-1 min-w-0">
              <Input
                readOnly
                type={showKey ? "text" : "password"}
                value={newRawKey}
                className="font-mono text-xs pr-8"
              />
              <button
                className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                onClick={() => setShowKey((v) => !v)}
              >
                {showKey ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
              </button>
            </div>
            <Button variant="outline" size="sm" onClick={copyKey}>
              {copiedKey ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setNewRawKey(null)}>Dismiss</Button>
          </div>
        </div>
      )}

      {/* Inline create form */}
      {showCreate ? (
        <div className="mb-4 rounded-md border border-border bg-card p-4 space-y-3">
          <p className="text-sm font-medium">Create API key</p>
          <Input
            placeholder="Label (e.g. ci-deploy)"
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            autoFocus
          />
          <div className="flex gap-2">
            <Button
              variant="default"
              size="sm"
              onClick={() => createMutation.mutate()}
              disabled={!label.trim() || createMutation.isPending}
            >
              {createMutation.isPending ? "Creating..." : "Create key"}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => { setShowCreate(false); setLabel("") }}>Cancel</Button>
          </div>
        </div>
      ) : (
        <Button variant="outline" size="sm" className="mb-4 gap-1.5" onClick={() => setShowCreate(true)}>
          <Plus className="h-3.5 w-3.5" /> New API key
        </Button>
      )}

      {/* Keys list */}
      {keys.length === 0 ? (
        <p className="text-sm text-muted-foreground">No API keys. Create one to access the API programmatically.</p>
      ) : (
        <div className="space-y-2">
          {keys.map((key) => (
            <div key={key.id} className="rounded-md border border-border bg-card px-4 py-3 space-y-1">
              <div className="flex items-center justify-between gap-3">
                <div className="flex items-center gap-2 min-w-0">
                  <span className="text-sm font-medium truncate">{key.label || "(unlabeled)"}</span>
                  <span className="text-xs text-muted-foreground shrink-0 rounded bg-secondary px-1.5 py-0.5">{key.scope}</span>
                </div>

                {revokeConfirm === key.id ? (
                  <div className="flex items-center gap-2 shrink-0">
                    <span className="text-xs text-muted-foreground">Type label to confirm:</span>
                    <Input
                      className="h-7 text-xs w-32"
                      placeholder={key.label || key.hash_prefix}
                      value={revokeLabel}
                      onChange={(e) => setRevokeLabel(e.target.value)}
                      autoFocus
                    />
                    <Button
                      variant="destructive"
                      size="sm"
                      className="text-xs"
                      disabled={revokeLabel !== (key.label || key.hash_prefix) || revokeMutation.isPending}
                      onClick={() => revokeMutation.mutate(key.id)}
                    >
                      Revoke
                    </Button>
                    <Button variant="ghost" size="sm" className="text-xs" onClick={() => { setRevokeConfirm(null); setRevokeLabel("") }}>
                      Cancel
                    </Button>
                  </div>
                ) : (
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 text-muted-foreground hover:text-destructive shrink-0"
                    onClick={() => { setRevokeConfirm(key.id); setRevokeLabel("") }}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                )}
              </div>
              <div className="flex gap-4 text-xs text-muted-foreground">
                <span className="font-mono">{key.hash_prefix}…</span>
                <span>Created {relativeTime(key.created_at)}</span>
                {key.last_used_at && <span>Last used {relativeTime(key.last_used_at)}</span>}
                {key.expires_at && <span>Expires {relativeTime(key.expires_at)}</span>}
              </div>
            </div>
          ))}
        </div>
      )}
    </section>
  )
}

// ─── Server Config section (SET-3) ───────────────────────────────────────────

function ServerConfigSection() {
  const { data } = useQuery<HealthResponse>({
    queryKey: ["server-health"],
    queryFn: () => apiFetch<HealthResponse>("/api/v1/health"),
    staleTime: 30000,
  })

  return (
    <section>
      <h2 className="text-sm font-medium mb-4 flex items-center gap-2">
        <Server className="h-4 w-4" /> Server Config
      </h2>
      <div className="rounded-md border border-border bg-card p-4 space-y-2">
        <ConfigRow label="Status" value={data?.status ?? "—"} />
        <ConfigRow label="Version" value={data?.version ?? "—"} mono />
        <ConfigRow label="Commit" value={data?.commit_sha ? data.commit_sha.slice(0, 12) : "—"} mono />
        <ConfigRow label="Build time" value={data?.build_time ?? "—"} />
      </div>
    </section>
  )
}

function ConfigRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-4 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className={cn(mono && "font-mono text-xs")}>{value}</span>
    </div>
  )
}

// ─── GPG Keys section (SET-4 / GPG-3) ────────────────────────────────────────

function GPGKeysSection() {
  const qc = useQueryClient()
  const [showAdd, setShowAdd] = React.useState(false)
  const [armoredKey, setArmoredKey] = React.useState("")
  const [owner, setOwner] = React.useState("")
  const [deleteConfirm, setDeleteConfirm] = React.useState<string | null>(null)
  const [deleteTyped, setDeleteTyped] = React.useState("")

  const { data, isLoading, isError } = useQuery<ListGPGKeysResponse>({
    queryKey: ["gpg-keys"],
    queryFn: () => apiFetch<ListGPGKeysResponse>("/api/v1/gpg-keys"),
    staleTime: 30000,
  })

  const importMutation = useMutation({
    mutationFn: () =>
      apiFetch<GPGKey>("/api/v1/gpg-keys", {
        method: "POST",
        body: JSON.stringify({ armored_key: armoredKey.trim(), owner: owner.trim() }),
      }),
    onSuccess: (key) => {
      qc.setQueryData<ListGPGKeysResponse>(["gpg-keys"], (old) =>
        old ? { ...old, keys: [...old.keys, key] } : { keys: [key] }
      )
      setArmoredKey("")
      setOwner("")
      setShowAdd(false)
      toast({ title: `GPG key imported`, description: key.fingerprint.slice(-16) })
    },
    onError: (err) => {
      const msg = err instanceof Error ? err.message : "Failed to import key"
      toast({ title: "Import failed", description: msg, variant: "destructive" })
    },
  })

  const deleteMutation = useMutation({
    mutationFn: (fingerprint: string) =>
      apiFetch(`/api/v1/gpg-keys/${fingerprint}`, { method: "DELETE" }),
    onMutate: async (fingerprint) => {
      // Optimistic: remove from cache immediately.
      await qc.cancelQueries({ queryKey: ["gpg-keys"] })
      const prev = qc.getQueryData<ListGPGKeysResponse>(["gpg-keys"])
      qc.setQueryData<ListGPGKeysResponse>(["gpg-keys"], (old) =>
        old ? { ...old, keys: old.keys.filter((k) => k.fingerprint !== fingerprint) } : old
      )
      return { prev }
    },
    onError: (_err, _fp, ctx) => {
      // Rollback on error.
      if (ctx?.prev) qc.setQueryData(["gpg-keys"], ctx.prev)
      toast({ title: "Delete failed", variant: "destructive" })
    },
    onSuccess: () => {
      setDeleteConfirm(null)
      setDeleteTyped("")
      toast({ title: "GPG key removed" })
    },
  })

  const keys = data?.keys ?? []

  return (
    <section>
      <div className="flex items-center justify-between mb-4">
        <h2 className="text-sm font-medium flex items-center gap-2">
          <ShieldCheck className="h-4 w-4" /> GPG Keys
        </h2>
        <Button
          variant="outline"
          size="sm"
          className="gap-1.5"
          onClick={() => setShowAdd((v) => !v)}
        >
          <Plus className="h-3.5 w-3.5" />
          Add key
        </Button>
      </div>

      {showAdd && (
        <div className="mb-4 rounded-md border border-border bg-card p-4 space-y-3">
          <p className="text-xs text-muted-foreground">
            Paste an ASCII-armored PGP public key block (BEGIN PGP PUBLIC KEY BLOCK).
          </p>
          <textarea
            className="w-full rounded-md border border-border bg-background px-3 py-2 text-xs font-mono resize-none h-32 focus:outline-none focus:ring-1 focus:ring-ring"
            placeholder="-----BEGIN PGP PUBLIC KEY BLOCK-----&#10;...&#10;-----END PGP PUBLIC KEY BLOCK-----"
            value={armoredKey}
            onChange={(e) => setArmoredKey(e.target.value)}
          />
          <Input
            placeholder="Owner / label (optional)"
            value={owner}
            onChange={(e) => setOwner(e.target.value)}
            className="text-sm"
          />
          <div className="flex gap-2">
            <Button
              size="sm"
              onClick={() => importMutation.mutate()}
              disabled={importMutation.isPending || !armoredKey.trim()}
            >
              {importMutation.isPending ? "Importing…" : "Import"}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => { setShowAdd(false); setArmoredKey(""); setOwner("") }}>
              Cancel
            </Button>
          </div>
        </div>
      )}

      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && <p className="text-sm text-destructive">Failed to load GPG keys.</p>}

      {!isLoading && keys.length === 0 && (
        <p className="text-sm text-muted-foreground">No keys yet. Add a key above.</p>
      )}

      {keys.length > 0 && (
        <div className="space-y-2">
          {keys.map((key) => (
            <div key={key.fingerprint} className="rounded-md border border-border bg-card px-4 py-3">
              {deleteConfirm === key.fingerprint ? (
                <div className="space-y-2">
                  <p className="text-xs text-destructive">
                    Type the last 8 chars of the fingerprint to confirm removal:
                    <span className="font-mono ml-1">{key.fingerprint.slice(-8)}</span>
                  </p>
                  <div className="flex gap-2">
                    <Input
                      className="h-7 text-xs font-mono w-32"
                      placeholder={key.fingerprint.slice(-8)}
                      value={deleteTyped}
                      onChange={(e) => setDeleteTyped(e.target.value)}
                    />
                    <Button
                      variant="destructive"
                      size="sm"
                      className="h-7 text-xs"
                      disabled={deleteTyped !== key.fingerprint.slice(-8) || deleteMutation.isPending}
                      onClick={() => deleteMutation.mutate(key.fingerprint)}
                    >
                      Remove
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-7 text-xs"
                      onClick={() => { setDeleteConfirm(null); setDeleteTyped("") }}
                    >
                      Cancel
                    </Button>
                  </div>
                </div>
              ) : (
                <div className="flex items-center justify-between gap-3">
                  <div className="min-w-0">
                    <p className="text-xs font-mono text-muted-foreground truncate">{key.fingerprint}</p>
                    <p className="text-sm font-medium">{key.owner || "—"}</p>
                  </div>
                  <div className="flex items-center gap-2 shrink-0">
                    <span className={cn(
                      "text-xs px-1.5 py-0.5 rounded",
                      key.source === "embedded"
                        ? "bg-secondary text-muted-foreground"
                        : "bg-primary/10 text-primary"
                    )}>
                      {key.source}
                    </span>
                    {key.source === "user" && (
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7 text-muted-foreground hover:text-destructive"
                        onClick={() => { setDeleteConfirm(key.fingerprint); setDeleteTyped("") }}
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    )}
                  </div>
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </section>
  )
}
