import * as React from "react"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { Copy, Check, Plus, Trash2, Key, Server, ShieldCheck, LogOut, Eye, EyeOff } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useSession } from "@/contexts/auth"
import { apiFetch } from "@/lib/api"
import { toast } from "@/hooks/use-toast"
import type { ListAPIKeysResponse, CreateAPIKeyResponse, HealthResponse } from "@/lib/types"
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

// ─── GPG Keys section (SET-4) ─────────────────────────────────────────────────

function GPGKeysSection() {
  return (
    <section>
      <h2 className="text-sm font-medium mb-4 flex items-center gap-2">
        <ShieldCheck className="h-4 w-4" /> GPG Keys
      </h2>
      <p className="text-sm text-muted-foreground">
        GPG key management is available via the CLI:{" "}
        <code className="font-mono text-xs bg-secondary px-1.5 py-0.5 rounded">
          clustr-serverd gpg import &lt;keyfile&gt;
        </code>
      </p>
    </section>
  )
}
