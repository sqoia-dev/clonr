import * as React from "react"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { Copy, Check, Plus, Trash2, Key, Server, ShieldCheck, LogOut, Eye, EyeOff, Pencil, RefreshCw, X, Users, GitCommit, Radio } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { useSession } from "@/contexts/auth"
import { apiFetch } from "@/lib/api"
import { toast } from "@/hooks/use-toast"
import type { ListAPIKeysResponse, CreateAPIKeyResponse, HealthResponse, ListGPGKeysResponse, GPGKey, LocalUser, ListLocalUsersResponse, BootEntry, ListBootEntriesResponse } from "@/lib/types"
import { cn, generateTempPassword } from "@/lib/utils"
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
        <p className="text-sm text-muted-foreground mt-1">Server configuration, user management, and API key management</p>
      </div>

      <APIKeysSection />
      <LocalUsersSection />
      <ServerConfigSection />
      <GPGKeysSection />
      <TwoStageCommitSection />
      <BootMenuSection />

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

// ─── Local Users section (web app accounts) ──────────────────────────────────

function LocalUsersSection() {
  const qc = useQueryClient()
  const [addOpen, setAddOpen] = React.useState(false)
  const [editUser, setEditUser] = React.useState<LocalUser | null>(null)
  const [resetUser, setResetUser] = React.useState<LocalUser | null>(null)
  const [disableConfirm, setDisableConfirm] = React.useState<LocalUser | null>(null)
  const [disableInput, setDisableInput] = React.useState("")
  const [newPassword, setNewPassword] = React.useState<string | null>(null)
  const [copyDone, setCopyDone] = React.useState(false)
  const [showPass, setShowPass] = React.useState(false)

  // Add form state.
  const [newUsername, setNewUsername] = React.useState("")
  const [newRole, setNewRole] = React.useState("operator")
  const [newPass, setNewPass] = React.useState("")
  const [addError, setAddError] = React.useState("")

  const { data, isLoading } = useQuery<ListLocalUsersResponse>({
    queryKey: ["local-users"],
    queryFn: () => apiFetch<ListLocalUsersResponse>("/api/v1/admin/users"),
    staleTime: 10000,
  })

  const createMutation = useMutation({
    mutationFn: () =>
      apiFetch("/api/v1/admin/users", {
        method: "POST",
        body: JSON.stringify({ username: newUsername, password: newPass, role: newRole }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["local-users"] })
      setAddOpen(false)
      setNewUsername(""); setNewPass(""); setNewRole("operator"); setAddError("")
      toast({ title: "User created" })
    },
    onError: (err) => setAddError(String(err)),
  })

  const updateMutation = useMutation({
    mutationFn: (u: LocalUser) =>
      apiFetch(`/api/v1/admin/users/${u.id}`, {
        method: "PUT",
        body: JSON.stringify({ role: u.role }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["local-users"] })
      setEditUser(null)
      toast({ title: "User updated" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Update failed", description: String(err) }),
  })

  const resetPassMutation = useMutation({
    mutationFn: (userId: string) => {
      const pwd = generateTempPassword()
      return apiFetch(`/api/v1/admin/users/${userId}/reset-password`, {
        method: "POST",
        body: JSON.stringify({ password: pwd }),
      }).then(() => pwd)
    },
    onSuccess: (pwd: string) => {
      qc.invalidateQueries({ queryKey: ["local-users"] })
      setNewPassword(pwd)
      toast({ title: "Password reset — show the operator the temp password" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Reset failed", description: String(err) }),
  })

  const disableMutation = useMutation({
    mutationFn: (userId: string) =>
      apiFetch(`/api/v1/admin/users/${userId}`, {
        method: "PUT",
        body: JSON.stringify({ disabled: true }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["local-users"] })
      setDisableConfirm(null); setDisableInput("")
      toast({ title: "User disabled" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Disable failed", description: String(err) }),
  })

  const enableMutation = useMutation({
    mutationFn: (userId: string) =>
      apiFetch(`/api/v1/admin/users/${userId}/enable`, { method: "POST" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["local-users"] })
      toast({ title: "User enabled" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Enable failed", description: String(err) }),
  })

  const users = data?.users ?? []

  return (
    <section id="users">
      <div className="flex items-center justify-between mb-4">
        <h2 className="text-sm font-medium flex items-center gap-2">
          <Users className="h-4 w-4" /> Users
        </h2>
        <Button size="sm" variant="outline" className="gap-1.5" onClick={() => setAddOpen(true)}>
          <Plus className="h-3.5 w-3.5" /> Add user
        </Button>
      </div>

      {/* Temp password panel */}
      {newPassword && (
        <div className="mb-4 rounded-md border border-border bg-amber-500/5 px-4 py-3 space-y-2">
          <p className="text-xs font-medium text-amber-400">Temp password — show this ONCE to the user</p>
          <div className="flex items-center gap-2">
            <code className="font-mono text-sm flex-1">
              {showPass ? newPassword : "••••••••••••••••"}
            </code>
            <Button size="sm" variant="ghost" className="h-6 w-6 p-0" onClick={() => setShowPass((v) => !v)}>
              {showPass ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              className="h-6 px-2 text-[11px]"
              onClick={() => {
                navigator.clipboard.writeText(newPassword)
                setCopyDone(true)
                setTimeout(() => setCopyDone(false), 2000)
              }}
            >
              {copyDone ? "Copied!" : "Copy"}
            </Button>
            <Button size="sm" variant="ghost" className="h-6 w-6 p-0" onClick={() => { setNewPassword(null); setCopyDone(false); setShowPass(false) }}>
              <X className="h-3 w-3" />
            </Button>
          </div>
          <p className="text-[11px] text-muted-foreground">User will be prompted to change on next login.</p>
        </div>
      )}

      {/* Add form */}
      {addOpen && (
        <div className="mb-4 rounded-md border border-border bg-secondary/10 px-4 py-3 space-y-2">
          <p className="text-xs font-medium">New local user</p>
          <div className="grid grid-cols-2 gap-2">
            <Input className="text-xs h-7" placeholder="Username" value={newUsername} onChange={(e) => setNewUsername(e.target.value)} autoFocus />
            <select
              className="text-xs h-7 rounded border border-border bg-background px-2"
              value={newRole}
              onChange={(e) => setNewRole(e.target.value)}
            >
              <option value="admin">admin</option>
              <option value="operator">operator</option>
              <option value="readonly">readonly</option>
            </select>
          </div>
          <Input className="text-xs h-7 font-mono" type="password" placeholder="Initial password" value={newPass} onChange={(e) => setNewPass(e.target.value)} />
          {addError && <p className="text-xs text-destructive">{addError}</p>}
          <div className="flex gap-2">
            <Button size="sm" className="flex-1 text-xs" disabled={!newUsername || !newPass || createMutation.isPending} onClick={() => createMutation.mutate()}>
              {createMutation.isPending ? "Creating…" : "Create user"}
            </Button>
            <Button size="sm" variant="ghost" className="text-xs" onClick={() => { setAddOpen(false); setAddError("") }}>Cancel</Button>
          </div>
        </div>
      )}

      {/* Users table */}
      {isLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-5 w-full" />
          <Skeleton className="h-5 w-3/4" />
        </div>
      ) : users.length === 0 ? (
        <p className="text-sm text-muted-foreground">No local users. Add one above.</p>
      ) : (
        <div className="rounded-md border border-border bg-card overflow-hidden">
          <table className="w-full text-xs">
            <thead>
              <tr className="border-b border-border">
                <th className="px-4 py-2 text-left text-[11px] font-medium text-muted-foreground">Username</th>
                <th className="px-4 py-2 text-left text-[11px] font-medium text-muted-foreground">Role</th>
                <th className="px-4 py-2 text-left text-[11px] font-medium text-muted-foreground">Last login</th>
                <th className="px-4 py-2 text-left text-[11px] font-medium text-muted-foreground">Status</th>
                <th className="px-4 py-2 text-right text-[11px] font-medium text-muted-foreground">Actions</th>
              </tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <React.Fragment key={u.id}>
                  <tr className={cn("border-b border-border/50 hover:bg-secondary/20", u.disabled && "opacity-50")}>
                    <td className="px-4 py-2 font-mono">{u.username}</td>
                    <td className="px-4 py-2">
                      {editUser?.id === u.id ? (
                        <select
                          className="text-xs h-6 rounded border border-border bg-background px-1"
                          value={editUser.role}
                          onChange={(e) => setEditUser({ ...editUser, role: e.target.value })}
                        >
                          <option value="admin">admin</option>
                          <option value="operator">operator</option>
                          <option value="readonly">readonly</option>
                        </select>
                      ) : (
                        <span className="rounded px-1.5 py-0.5 bg-secondary text-[10px]">{u.role}</span>
                      )}
                    </td>
                    <td className="px-4 py-2 text-muted-foreground">{relativeTime(u.last_login_at)}</td>
                    <td className="px-4 py-2">
                      <span className={cn(
                        "rounded px-1.5 py-0.5 text-[10px] font-medium",
                        u.disabled ? "bg-red-500/10 text-red-400" : "bg-green-500/10 text-green-400"
                      )}>
                        {u.disabled ? "disabled" : u.must_change_password ? "must change pwd" : "active"}
                      </span>
                    </td>
                    <td className="px-4 py-2 text-right">
                      <div className="flex items-center justify-end gap-1">
                        {editUser?.id === u.id ? (
                          <>
                            <Button size="sm" className="h-6 px-2 text-[11px]" onClick={() => updateMutation.mutate(editUser)} disabled={updateMutation.isPending}>
                              Save
                            </Button>
                            <Button size="sm" variant="ghost" className="h-6 px-1" onClick={() => setEditUser(null)}>
                              <X className="h-3 w-3" />
                            </Button>
                          </>
                        ) : (
                          <>
                            <Button size="sm" variant="ghost" className="h-6 w-6 p-0" onClick={() => setEditUser(u)} title="Edit role">
                              <Pencil className="h-3 w-3" />
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              className="h-6 px-2 text-[11px]"
                              onClick={() => { setResetUser(u); resetPassMutation.mutate(u.id) }}
                              disabled={resetPassMutation.isPending && resetUser?.id === u.id}
                              title="Reset password"
                            >
                              <RefreshCw className="h-3 w-3" />
                            </Button>
                            {u.disabled ? (
                              <Button size="sm" variant="ghost" className="h-6 px-2 text-[11px] text-green-400" onClick={() => enableMutation.mutate(u.id)}>
                                Enable
                              </Button>
                            ) : (
                              <Button size="sm" variant="ghost" className="h-6 px-2 text-[11px] text-destructive" onClick={() => setDisableConfirm(u)}>
                                Disable
                              </Button>
                            )}
                          </>
                        )}
                      </div>
                    </td>
                  </tr>
                  {/* Inline disable confirm row */}
                  {disableConfirm?.id === u.id && (
                    <tr className="border-b border-border/50 bg-destructive/5">
                      <td colSpan={5} className="px-4 py-2">
                        <div className="flex items-center gap-2 text-xs">
                          <span className="text-muted-foreground">Type <code className="font-mono">{u.username}</code> to confirm disable:</span>
                          <Input
                            className="h-6 w-36 text-[11px] font-mono"
                            value={disableInput}
                            onChange={(e) => setDisableInput(e.target.value)}
                            autoFocus
                          />
                          <Button
                            size="sm"
                            variant="destructive"
                            className="h-6 px-2 text-[11px]"
                            disabled={disableInput !== u.username || disableMutation.isPending}
                            onClick={() => disableMutation.mutate(u.id)}
                          >
                            Disable
                          </Button>
                          <Button size="sm" variant="ghost" className="h-6 px-1" onClick={() => { setDisableConfirm(null); setDisableInput("") }}>
                            <X className="h-3 w-3" />
                          </Button>
                        </div>
                      </td>
                    </tr>
                  )}
                </React.Fragment>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
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

  const { data, isLoading: keysLoading, isError: keysError } = useQuery<ListAPIKeysResponse>({
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
    // POL-5: Optimistic insert — add a placeholder key; replace on success, rollback on error.
    onMutate: async () => {
      await qc.cancelQueries({ queryKey: ["api-keys"] })
      const prev = qc.getQueryData<ListAPIKeysResponse>(["api-keys"])
      return { prev }
    },
    onSuccess: (res, _v, ctx) => {
      // Roll back the placeholder, then let invalidation populate the real entry.
      if (ctx?.prev) qc.setQueryData(["api-keys"], ctx.prev)
      qc.invalidateQueries({ queryKey: ["api-keys"] })
      setNewRawKey(res.key)
      setLabel("")
      setShowCreate(false)
    },
    onError: (_err, _v, ctx) => {
      // POL-5: rollback any optimistic state.
      if (ctx?.prev) qc.setQueryData(["api-keys"], ctx.prev)
      toast({ variant: "destructive", title: "Failed to create key" })
    },
  })

  const revokeMutation = useMutation({
    mutationFn: (id: string) =>
      apiFetch(`/api/v1/admin/api-keys/${id}`, { method: "DELETE" }),
    // POL-5: Optimistic remove — remove from list immediately, rollback on error.
    onMutate: async (id: string) => {
      await qc.cancelQueries({ queryKey: ["api-keys"] })
      const prev = qc.getQueryData<ListAPIKeysResponse>(["api-keys"])
      qc.setQueryData<ListAPIKeysResponse>(["api-keys"], (old) =>
        old ? { ...old, api_keys: old.api_keys.filter((k) => k.id !== id) } : old
      )
      return { prev }
    },
    onSuccess: () => {
      setRevokeConfirm(null)
      setRevokeLabel("")
      toast({ title: "API key revoked" })
    },
    onError: (_err, _id, ctx) => {
      // POL-5: rollback.
      if (ctx?.prev) qc.setQueryData(["api-keys"], ctx.prev)
      toast({ variant: "destructive", title: "Failed to revoke key" })
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

      {/* POL-7: Loading / error / empty states */}
      {keysLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {keysError && <p className="text-sm text-destructive">Failed to load API keys. Reload to retry.</p>}

      {/* Keys list */}
      {!keysLoading && !keysError && keys.length === 0 ? (
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

// ─── Two-stage commit section (#154) ─────────────────────────────────────────

const STAGE_SURFACES: { key: string; label: string; description: string }[] = [
  {
    key: "ldap_user",
    label: "Stage LDAP user changes by default",
    description: "CREATE / UPDATE / PATCH /api/v1/ldap/users/* will write to the pending queue instead of the directory.",
  },
  {
    key: "sudoers_rule",
    label: "Stage sudoers rule changes by default",
    description: "POST /api/v1/nodes/{id}/sudoers will queue instead of applying immediately.",
  },
  {
    key: "node_network",
    label: "Stage network changes by default",
    description: "POST/PUT /api/v1/network/profiles will queue instead of applying immediately.",
  },
]

function TwoStageCommitSection() {
  const qc = useQueryClient()

  const { data, isLoading } = useQuery<{ mode: Record<string, boolean> }>({
    queryKey: ["stage-mode"],
    queryFn: () => apiFetch("/api/v1/changes/mode"),
    staleTime: 30000,
  })

  const setModeMutation = useMutation({
    mutationFn: ({ surface, enabled }: { surface: string; enabled: boolean }) =>
      apiFetch(`/api/v1/changes/mode/${surface}`, {
        method: "PUT",
        body: JSON.stringify({ enabled }),
      }),
    onSuccess: (_res, { surface, enabled }) => {
      qc.setQueryData<{ mode: Record<string, boolean> }>(["stage-mode"], (old) =>
        old ? { mode: { ...old.mode, [surface]: enabled } } : old
      )
      toast({ title: `${surface} stage mode ${enabled ? "enabled" : "disabled"}` })
    },
    onError: (err) =>
      toast({ variant: "destructive", title: "Failed to update stage mode", description: String(err) }),
  })

  const flags = data?.mode ?? {}

  return (
    <section id="two-stage-commit" className="border-t border-border pt-8">
      <h2 className="text-sm font-medium mb-1 flex items-center gap-2">
        <GitCommit className="h-4 w-4" /> Two-stage commit
      </h2>
      <p className="text-xs text-muted-foreground mb-4">
        When enabled for a surface, mutations will write to the pending-changes queue instead of
        applying immediately. Use the Pending Changes drawer (top bar) to commit or discard.
      </p>

      {isLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : (
        <div className="space-y-2">
          {STAGE_SURFACES.map(({ key, label, description }) => {
            const enabled = flags[key] ?? false
            return (
              <div
                key={key}
                className="flex items-start justify-between gap-4 rounded-md border border-border bg-card px-4 py-3"
              >
                <div className="space-y-0.5 min-w-0">
                  <p className="text-sm font-medium">{label}</p>
                  <p className="text-xs text-muted-foreground">{description}</p>
                </div>
                <button
                  role="switch"
                  aria-checked={enabled}
                  onClick={() => setModeMutation.mutate({ surface: key, enabled: !enabled })}
                  disabled={setModeMutation.isPending}
                  className={cn(
                    "relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent transition-colors focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2",
                    enabled ? "bg-primary" : "bg-input"
                  )}
                >
                  <span
                    className={cn(
                      "pointer-events-none inline-block h-4 w-4 rounded-full bg-background shadow-lg ring-0 transition-transform",
                      enabled ? "translate-x-4" : "translate-x-0"
                    )}
                  />
                </button>
              </div>
            )
          })}
        </div>
      )}
    </section>
  )
}

// ─── Boot Menu section (#160) ─────────────────────────────────────────────────

const BOOT_ENTRY_KINDS: { value: string; label: string }[] = [
  { value: "kernel", label: "Generic kernel" },
  { value: "memtest", label: "Memtest" },
  { value: "rescue", label: "Rescue shell" },
  { value: "iso", label: "ISO chainload" },
]

function BootMenuSection() {
  const qc = useQueryClient()
  const [addOpen, setAddOpen] = React.useState(false)
  const [editEntry, setEditEntry] = React.useState<BootEntry | null>(null)
  const [deleteConfirm, setDeleteConfirm] = React.useState<string | null>(null)

  // Add form state.
  const [newName, setNewName] = React.useState("")
  const [newKind, setNewKind] = React.useState("kernel")
  const [newKernelURL, setNewKernelURL] = React.useState("")
  const [newInitrdURL, setNewInitrdURL] = React.useState("")
  const [newCmdline, setNewCmdline] = React.useState("")
  const [addError, setAddError] = React.useState("")

  const { data, isLoading, isError } = useQuery<ListBootEntriesResponse>({
    queryKey: ["boot-entries"],
    queryFn: () => apiFetch<ListBootEntriesResponse>("/api/v1/boot-entries"),
    staleTime: 15000,
  })

  const createMutation = useMutation({
    mutationFn: () =>
      apiFetch("/api/v1/boot-entries", {
        method: "POST",
        body: JSON.stringify({
          name: newName,
          kind: newKind,
          kernel_url: newKernelURL,
          initrd_url: newInitrdURL || undefined,
          cmdline: newCmdline || undefined,
          enabled: true,
        }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["boot-entries"] })
      setAddOpen(false)
      setNewName(""); setNewKind("kernel"); setNewKernelURL(""); setNewInitrdURL(""); setNewCmdline(""); setAddError("")
      toast({ title: "Boot entry created" })
    },
    onError: (err) => setAddError(String(err)),
  })

  const toggleMutation = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) =>
      apiFetch(`/api/v1/boot-entries/${id}`, {
        method: "PUT",
        body: JSON.stringify({ enabled }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["boot-entries"] })
    },
    onError: (err) => toast({ variant: "destructive", title: "Toggle failed", description: String(err) }),
  })

  const updateMutation = useMutation({
    mutationFn: (e: BootEntry) =>
      apiFetch(`/api/v1/boot-entries/${e.id}`, {
        method: "PUT",
        body: JSON.stringify({
          name: e.name,
          kind: e.kind,
          kernel_url: e.kernel_url,
          initrd_url: e.initrd_url,
          cmdline: e.cmdline,
          enabled: e.enabled,
        }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["boot-entries"] })
      setEditEntry(null)
      toast({ title: "Boot entry updated" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Update failed", description: String(err) }),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) =>
      apiFetch(`/api/v1/boot-entries/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["boot-entries"] })
      setDeleteConfirm(null)
      toast({ title: "Boot entry deleted" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Delete failed", description: String(err) }),
  })

  const entries = data?.entries ?? []

  return (
    <section id="boot-menu" className="border-t border-border pt-8">
      <div className="flex items-center justify-between mb-2">
        <h2 className="text-sm font-medium flex items-center gap-2">
          <Radio className="h-4 w-4" /> Boot Menu
        </h2>
        <Button size="sm" variant="outline" className="gap-1.5" onClick={() => setAddOpen(true)}>
          <Plus className="h-3.5 w-3.5" /> Add entry
        </Button>
      </div>
      <p className="text-xs text-muted-foreground mb-4">
        Enabled entries appear in the iPXE boot menu for deployed nodes at PXE-serve time.
        The Rescue Shell entry is disabled by default until a rescue image is available.
      </p>

      {/* Add entry form */}
      {addOpen && (
        <div className="mb-4 rounded-md border border-border bg-card px-4 py-4 space-y-3">
          <p className="text-sm font-medium">New boot entry</p>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="text-xs text-muted-foreground block mb-1">Name</label>
              <Input className="h-8 text-sm" placeholder="My custom entry" value={newName} onChange={(e) => setNewName(e.target.value)} />
            </div>
            <div>
              <label className="text-xs text-muted-foreground block mb-1">Kind</label>
              <select
                className="h-8 w-full rounded-md border border-input bg-background px-3 text-sm"
                value={newKind}
                onChange={(e) => setNewKind(e.target.value)}
              >
                {BOOT_ENTRY_KINDS.map(k => <option key={k.value} value={k.value}>{k.label}</option>)}
              </select>
            </div>
          </div>
          <div>
            <label className="text-xs text-muted-foreground block mb-1">Kernel URL</label>
            <Input className="h-8 text-sm font-mono" placeholder="/api/v1/boot/vmlinuz or https://..." value={newKernelURL} onChange={(e) => setNewKernelURL(e.target.value)} />
          </div>
          <div>
            <label className="text-xs text-muted-foreground block mb-1">Initrd URL <span className="text-muted-foreground/60">(optional)</span></label>
            <Input className="h-8 text-sm font-mono" placeholder="/api/v1/boot/initramfs.img" value={newInitrdURL} onChange={(e) => setNewInitrdURL(e.target.value)} />
          </div>
          <div>
            <label className="text-xs text-muted-foreground block mb-1">Cmdline <span className="text-muted-foreground/60">(optional)</span></label>
            <Input className="h-8 text-sm font-mono" placeholder="console=ttyS0,115200n8" value={newCmdline} onChange={(e) => setNewCmdline(e.target.value)} />
          </div>
          {addError && <p className="text-xs text-destructive">{addError}</p>}
          <div className="flex gap-2">
            <Button size="sm" disabled={!newName || !newKernelURL || createMutation.isPending} onClick={() => createMutation.mutate()}>
              Create
            </Button>
            <Button size="sm" variant="ghost" onClick={() => { setAddOpen(false); setAddError("") }}>Cancel</Button>
          </div>
        </div>
      )}

      {/* Edit entry form */}
      {editEntry && (
        <div className="mb-4 rounded-md border border-border bg-card px-4 py-4 space-y-3">
          <p className="text-sm font-medium">Edit: {editEntry.name}</p>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="text-xs text-muted-foreground block mb-1">Name</label>
              <Input className="h-8 text-sm" value={editEntry.name} onChange={(e) => setEditEntry({ ...editEntry, name: e.target.value })} />
            </div>
            <div>
              <label className="text-xs text-muted-foreground block mb-1">Kind</label>
              <select
                className="h-8 w-full rounded-md border border-input bg-background px-3 text-sm"
                value={editEntry.kind}
                onChange={(e) => setEditEntry({ ...editEntry, kind: e.target.value as BootEntry["kind"] })}
              >
                {BOOT_ENTRY_KINDS.map(k => <option key={k.value} value={k.value}>{k.label}</option>)}
              </select>
            </div>
          </div>
          <div>
            <label className="text-xs text-muted-foreground block mb-1">Kernel URL</label>
            <Input className="h-8 text-sm font-mono" value={editEntry.kernel_url} onChange={(e) => setEditEntry({ ...editEntry, kernel_url: e.target.value })} />
          </div>
          <div>
            <label className="text-xs text-muted-foreground block mb-1">Initrd URL <span className="text-muted-foreground/60">(optional)</span></label>
            <Input className="h-8 text-sm font-mono" value={editEntry.initrd_url ?? ""} onChange={(e) => setEditEntry({ ...editEntry, initrd_url: e.target.value })} />
          </div>
          <div>
            <label className="text-xs text-muted-foreground block mb-1">Cmdline <span className="text-muted-foreground/60">(optional)</span></label>
            <Input className="h-8 text-sm font-mono" value={editEntry.cmdline ?? ""} onChange={(e) => setEditEntry({ ...editEntry, cmdline: e.target.value })} />
          </div>
          <div className="flex gap-2">
            <Button size="sm" disabled={updateMutation.isPending} onClick={() => updateMutation.mutate(editEntry)}>Save</Button>
            <Button size="sm" variant="ghost" onClick={() => setEditEntry(null)}>Cancel</Button>
          </div>
        </div>
      )}

      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && <p className="text-sm text-destructive">Failed to load boot entries.</p>}

      {!isLoading && entries.length === 0 && (
        <p className="text-sm text-muted-foreground">No custom entries. Stock entries (Memtest, Rescue) were seeded by migration.</p>
      )}

      {entries.length > 0 && (
        <div className="space-y-2">
          {entries.map((entry) => (
            <div key={entry.id} className="rounded-md border border-border bg-card px-4 py-3">
              {deleteConfirm === entry.id ? (
                <div className="space-y-2">
                  <p className="text-xs text-destructive">Delete <span className="font-medium">{entry.name}</span>? This cannot be undone.</p>
                  <div className="flex gap-2">
                    <Button variant="destructive" size="sm" className="h-7 text-xs" disabled={deleteMutation.isPending} onClick={() => deleteMutation.mutate(entry.id)}>
                      Delete
                    </Button>
                    <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={() => setDeleteConfirm(null)}>Cancel</Button>
                  </div>
                </div>
              ) : (
                <div className="flex items-center justify-between gap-3">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <p className="text-sm font-medium">{entry.name}</p>
                      <span className="text-xs px-1.5 py-0.5 rounded bg-secondary text-muted-foreground">{entry.kind}</span>
                    </div>
                    <p className="text-xs font-mono text-muted-foreground truncate mt-0.5">{entry.kernel_url}</p>
                  </div>
                  <div className="flex items-center gap-2 shrink-0">
                    {/* Enabled toggle */}
                    <button
                      role="switch"
                      aria-checked={entry.enabled}
                      onClick={() => toggleMutation.mutate({ id: entry.id, enabled: !entry.enabled })}
                      disabled={toggleMutation.isPending}
                      title={entry.enabled ? "Disable" : "Enable"}
                      className={cn(
                        "relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent transition-colors focus:outline-none",
                        entry.enabled ? "bg-primary" : "bg-input"
                      )}
                    >
                      <span className={cn(
                        "pointer-events-none inline-block h-4 w-4 rounded-full bg-background shadow-lg ring-0 transition-transform",
                        entry.enabled ? "translate-x-4" : "translate-x-0"
                      )} />
                    </button>
                    <Button variant="ghost" size="icon" className="h-7 w-7 text-muted-foreground" onClick={() => setEditEntry(entry)}>
                      <Pencil className="h-3.5 w-3.5" />
                    </Button>
                    <Button variant="ghost" size="icon" className="h-7 w-7 text-muted-foreground hover:text-destructive" onClick={() => setDeleteConfirm(entry.id)}>
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
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
