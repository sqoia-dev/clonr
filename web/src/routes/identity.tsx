import * as React from "react"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { Search, Plus, Pencil, Trash2, X, Eye, EyeOff, RefreshCw, ShieldCheck, Users, Settings, Database, AlertTriangle, CheckCircle2, ToggleLeft, ToggleRight, KeyRound } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { apiFetch } from "@/lib/api"
import { toast } from "@/hooks/use-toast"
import { UserPicker } from "@/components/UserPicker"
import { cn } from "@/lib/utils"
import type {
  LocalUser,
  ListLocalUsersResponse,
  LDAPUser,
  LDAPConfigResponse,
  LDAPTestResponse,
  SpecialtyGroup,
  ListSpecialtyGroupsResponse,
  LDAPGroup,
  ListLDAPGroupsResponse,
  GroupOverlay,
  UserSearchResult,
  LDAPGroupModeResponse,
  LDAPResetPasswordResponse,
} from "@/lib/types"
import { formatDistanceToNow } from "date-fns"

function relTime(iso?: string | null) {
  if (!iso) return "—"
  try { return formatDistanceToNow(new Date(iso), { addSuffix: true }) } catch { return "—" }
}

// ─── Identity page ────────────────────────────────────────────────────────────

export function IdentityPage() {
  return (
    <div className="max-w-3xl mx-auto p-8 space-y-12">
      <div>
        <h1 className="text-xl font-semibold flex items-center gap-2">
          <ShieldCheck className="h-5 w-5 text-primary" />
          Identity
        </h1>
        <p className="text-sm text-muted-foreground mt-1">
          User management, group configuration, system accounts, and LDAP.
        </p>
      </div>

      {/* Anchored sections in prescribed order: Users / Groups / System accounts / LDAP config */}
      <UsersSection />
      <GroupsSection />
      <SystemAccountsSection />
      <LDAPConfigSection />
    </div>
  )
}

// ─── Section wrapper ──────────────────────────────────────────────────────────

function Section({ title, icon: Icon, id, children }: { title: string; icon: React.ComponentType<{ className?: string }>; id?: string; children: React.ReactNode }) {
  return (
    <section id={id} className="space-y-4">
      <h2 className="text-sm font-medium flex items-center gap-2 border-b border-border pb-2">
        <Icon className="h-4 w-4" />
        {title}
      </h2>
      {children}
    </section>
  )
}

// ─── Users section (USERS-1..4) ───────────────────────────────────────────────

function UsersSection() {
  return (
    <Section title="Users" icon={Users} id="users">
      <LocalUsersCard />
      <LDAPUsersCard />
    </Section>
  )
}

function LocalUsersCard() {
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
      // Generate a random temp password.
      const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
      let pwd = ""
      for (let i = 0; i < 16; i++) pwd += chars[Math.floor(Math.random() * chars.length)]
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
    <div className="rounded-md border border-border bg-card">
      <div className="flex items-center justify-between px-4 py-3 border-b border-border">
        <span className="text-xs font-medium text-muted-foreground">Local users</span>
        <Button size="sm" variant="outline" className="text-xs h-7" onClick={() => setAddOpen(true)}>
          <Plus className="h-3 w-3 mr-1" /> Add user
        </Button>
      </div>

      {/* Temp password panel */}
      {newPassword && (
        <div className="px-4 py-3 border-b border-border bg-amber-500/5 space-y-2">
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
        <div className="px-4 py-3 border-b border-border bg-secondary/10 space-y-2">
          <p className="text-xs font-medium">New local user</p>
          <div className="grid grid-cols-2 gap-2">
            <Input className="text-xs h-7" placeholder="Username" value={newUsername} onChange={(e) => setNewUsername(e.target.value)} />
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
        <div className="p-4 space-y-2">
          <Skeleton className="h-5 w-full" />
          <Skeleton className="h-5 w-3/4" />
        </div>
      ) : users.length === 0 ? (
        <p className="px-4 py-3 text-xs text-muted-foreground">No local users. Add one above.</p>
      ) : (
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
                  <td className="px-4 py-2 text-muted-foreground">{relTime(u.last_login_at)}</td>
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
      )}
    </div>
  )
}

// ─── Write-mode banner (WRITE-SAFETY-2) ──────────────────────────────────────

function WriteModeBanner({ config }: { config?: LDAPConfigResponse }) {
  if (!config?.enabled) return null
  // No write bind set at all — no banner
  if (!config.write_bind_dn_set && config.write_capable === undefined) return null

  const isGreen = config.write_capable === true
  const isYellow = config.write_bind_dn_set && !config.write_capable

  if (!isGreen && !isYellow) return null

  return (
    <div className={cn(
      "flex items-center gap-2 px-3 py-1.5 text-[11px] rounded border mb-3",
      isGreen
        ? "border-green-500/30 bg-green-500/5 text-green-400"
        : "border-amber-500/30 bg-amber-500/5 text-amber-400"
    )}>
      {isGreen
        ? <CheckCircle2 className="h-3 w-3 shrink-0" />
        : <AlertTriangle className="h-3 w-3 shrink-0" />
      }
      {isGreen
        ? "Writes go directly to your LDAP directory."
        : "Write bind configured but write probe not verified — save config to re-probe."
      }
    </div>
  )
}

// ─── LDAP Users Card (WRITE-USER-5) ──────────────────────────────────────────

function LDAPUsersCard() {
  const qc = useQueryClient()
  const [q, setQ] = React.useState("")
  const [searched, setSearched] = React.useState(false)
  const [addOpen, setAddOpen] = React.useState(false)
  const [editUser, setEditUser] = React.useState<LDAPUser | null>(null)
  const [deleteConfirm, setDeleteConfirm] = React.useState<LDAPUser | null>(null)
  const [deleteInput, setDeleteInput] = React.useState("")
  const [resetResult, setResetResult] = React.useState<{ uid: string; pwd: string } | null>(null)
  const [showTempPwd, setShowTempPwd] = React.useState(false)
  const [copyDone, setCopyDone] = React.useState(false)

  // Add form state
  const [fUID, setFUID] = React.useState("")
  const [fCN, setFCN] = React.useState("")
  const [fSN, setFSN] = React.useState("")
  const [fUID_num, setFUID_num] = React.useState("")
  const [fGID_num, setFGID_num] = React.useState("")
  const [fHome, setFHome] = React.useState("")
  const [fShell, setFShell] = React.useState("/bin/bash")
  const [fPassword, setFPassword] = React.useState("")
  const [addError, setAddError] = React.useState("")

  // Edit form state
  const [eCN, setECN] = React.useState("")
  const [eSN, setESN] = React.useState("")
  const [eHome, setEHome] = React.useState("")
  const [eShell, setEShell] = React.useState("")

  // Get write-capable status for banner
  const { data: configData } = useQuery<LDAPConfigResponse>({
    queryKey: ["ldap-config"],
    queryFn: () => apiFetch<LDAPConfigResponse>("/api/v1/ldap/config"),
    staleTime: 15000,
    retry: false,
  })

  const { data, isFetching, refetch } = useQuery<{ users: LDAPUser[]; total: number }>({
    queryKey: ["ldap-users-search", q],
    queryFn: () => apiFetch<{ users: LDAPUser[]; total: number }>(`/api/v1/ldap/users/search?q=${encodeURIComponent(q)}`),
    enabled: searched,
    staleTime: 30000,
  })

  const createMutation = useMutation({
    mutationFn: () => apiFetch("/api/v1/ldap/users", {
      method: "POST",
      body: JSON.stringify({
        uid: fUID, cn: fCN || fUID, sn: fSN || fUID,
        uid_number: Number(fUID_num) || 0, gid_number: Number(fGID_num) || 0,
        home_directory: fHome || `/home/${fUID}`, login_shell: fShell,
        password: fPassword,
      }),
    }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ldap-users-search"] })
      setAddOpen(false)
      setFUID(""); setFCN(""); setFSN(""); setFUID_num(""); setFGID_num("")
      setFHome(""); setFShell("/bin/bash"); setFPassword(""); setAddError("")
      toast({ title: "LDAP user created" })
      if (searched) refetch()
    },
    onError: (err) => setAddError(String(err)),
  })

  const updateMutation = useMutation({
    mutationFn: (uid: string) => apiFetch(`/api/v1/ldap/users/${encodeURIComponent(uid)}`, {
      method: "PUT",
      body: JSON.stringify({ cn: eCN, sn: eSN, home_directory: eHome, login_shell: eShell }),
    }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ldap-users-search"] })
      setEditUser(null)
      toast({ title: "User updated" })
      refetch()
    },
    onError: (err) => toast({ variant: "destructive", title: "Update failed", description: String(err) }),
  })

  const deleteMutation = useMutation({
    mutationFn: (uid: string) => apiFetch(`/api/v1/ldap/users/${encodeURIComponent(uid)}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ldap-users-search"] })
      setDeleteConfirm(null); setDeleteInput("")
      toast({ title: "LDAP user deleted" })
      refetch()
    },
    onError: (err) => toast({ variant: "destructive", title: "Delete failed", description: String(err) }),
  })

  const resetPwdMutation = useMutation({
    mutationFn: (uid: string) => apiFetch<LDAPResetPasswordResponse>(
      `/api/v1/ldap/users/${encodeURIComponent(uid)}/reset-password`, { method: "POST" }
    ),
    onSuccess: (res) => {
      setResetResult({ uid: res.uid, pwd: res.temp_password })
      setShowTempPwd(false)
      toast({ title: "Password reset — show the operator the temp password" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Reset failed", description: String(err) }),
  })

  function handleSearch() {
    setSearched(true)
    refetch()
  }

  function startEdit(u: LDAPUser) {
    setEditUser(u)
    setECN(u.cn ?? "")
    setESN(u.sn ?? "")
    setEHome(u.home_directory ?? "")
    setEShell(u.login_shell ?? "/bin/bash")
  }

  const users = data?.users ?? []
  const writeCapable = configData?.write_capable ?? false

  return (
    <div className="rounded-md border border-border bg-card">
      <div className="flex items-center justify-between px-4 py-3 border-b border-border">
        <span className="text-xs font-medium text-muted-foreground">LDAP users</span>
        {writeCapable && (
          <Button size="sm" variant="outline" className="h-7 text-xs" onClick={() => setAddOpen(true)}>
            <Plus className="h-3 w-3 mr-1" /> Add LDAP user
          </Button>
        )}
      </div>

      {/* Write-mode banner */}
      {configData && <div className="px-4 pt-3"><WriteModeBanner config={configData} /></div>}

      {/* Temp password panel (WRITE-USER-5 — show once) */}
      {resetResult && (
        <div className="px-4 py-3 border-b border-border bg-amber-500/5 space-y-2">
          <p className="text-xs font-medium text-amber-400">Temp password for {resetResult.uid} — show ONCE to user</p>
          <div className="flex items-center gap-2">
            <code className="font-mono text-sm flex-1">
              {showTempPwd ? resetResult.pwd : "••••••••••••••••••••"}
            </code>
            <Button size="sm" variant="ghost" className="h-6 w-6 p-0" onClick={() => setShowTempPwd((v) => !v)}>
              {showTempPwd ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
            </Button>
            <Button size="sm" variant="ghost" className="h-6 px-2 text-[11px]"
              onClick={() => { navigator.clipboard.writeText(resetResult.pwd); setCopyDone(true); setTimeout(() => setCopyDone(false), 2000) }}>
              {copyDone ? "Copied!" : "Copy"}
            </Button>
            <Button size="sm" variant="ghost" className="h-6 w-6 p-0" onClick={() => { setResetResult(null); setCopyDone(false) }}>
              <X className="h-3 w-3" />
            </Button>
          </div>
          <p className="text-[11px] text-muted-foreground">User must change password at next login.</p>
        </div>
      )}

      {/* Add form */}
      {addOpen && (
        <div className="px-4 py-3 border-b border-border bg-secondary/10 space-y-2">
          <p className="text-xs font-medium">New LDAP user</p>
          <div className="grid grid-cols-3 gap-2">
            <Input className="text-xs h-7 font-mono" placeholder="UID (username)" value={fUID} onChange={(e) => setFUID(e.target.value)} autoFocus />
            <Input className="text-xs h-7" placeholder="Display name (CN)" value={fCN} onChange={(e) => setFCN(e.target.value)} />
            <Input className="text-xs h-7" placeholder="Surname (SN)" value={fSN} onChange={(e) => setFSN(e.target.value)} />
          </div>
          <div className="grid grid-cols-2 gap-2">
            <Input className="text-xs h-7 font-mono" placeholder="UID number (0=auto)" type="number" value={fUID_num} onChange={(e) => setFUID_num(e.target.value)} />
            <Input className="text-xs h-7 font-mono" placeholder="GID number (0=auto)" type="number" value={fGID_num} onChange={(e) => setFGID_num(e.target.value)} />
          </div>
          <div className="grid grid-cols-2 gap-2">
            <Input className="text-xs h-7 font-mono" placeholder={`Home dir (/home/${fUID})`} value={fHome} onChange={(e) => setFHome(e.target.value)} />
            <Input className="text-xs h-7 font-mono" placeholder="Shell (/bin/bash)" value={fShell} onChange={(e) => setFShell(e.target.value)} />
          </div>
          <Input className="text-xs h-7 font-mono" type="password" placeholder="Initial password (optional)" value={fPassword} onChange={(e) => setFPassword(e.target.value)} />
          {addError && <p className="text-xs text-destructive">{addError}</p>}
          <div className="flex gap-2">
            <Button size="sm" className="flex-1 text-xs" disabled={!fUID || createMutation.isPending} onClick={() => createMutation.mutate()}>
              {createMutation.isPending ? "Creating…" : "Create LDAP user"}
            </Button>
            <Button size="sm" variant="ghost" className="text-xs" onClick={() => { setAddOpen(false); setAddError("") }}>Cancel</Button>
          </div>
        </div>
      )}

      {/* Search */}
      <div className="px-4 py-3 space-y-3">
        <div className="flex gap-2">
          <Input
            className="text-xs h-7 flex-1"
            placeholder="Search by uid, name, or email…"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && handleSearch()}
          />
          <Button size="sm" variant="outline" className="h-7 text-xs" onClick={handleSearch} disabled={isFetching}>
            <Search className="h-3 w-3 mr-1" /> {isFetching ? "Searching…" : "Search"}
          </Button>
        </div>

        {!searched && <p className="text-xs text-muted-foreground">Search to browse LDAP directory users.</p>}
        {searched && !isFetching && users.length === 0 && (
          <p className="text-xs text-muted-foreground">No LDAP users found. Check that the LDAP module is enabled and ready.</p>
        )}

        {users.length > 0 && (
          <table className="w-full text-xs">
            <thead>
              <tr className="border-b border-border">
                <th className="py-1.5 text-left text-[11px] font-medium text-muted-foreground">UID</th>
                <th className="py-1.5 text-left text-[11px] font-medium text-muted-foreground">Name</th>
                <th className="py-1.5 text-left text-[11px] font-medium text-muted-foreground">Email</th>
                <th className="py-1.5 text-left text-[11px] font-medium text-muted-foreground">GID</th>
                {writeCapable && <th className="py-1.5 text-right text-[11px] font-medium text-muted-foreground">Actions</th>}
              </tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <React.Fragment key={u.uid}>
                  <tr className="border-b border-border/50 hover:bg-secondary/20">
                    <td className="py-1.5 font-mono pr-4">{u.uid}</td>
                    <td className="py-1.5 pr-4">
                      {editUser?.uid === u.uid ? (
                        <Input className="h-6 text-[11px] w-full" value={eCN} onChange={(e) => setECN(e.target.value)} placeholder="Display name" />
                      ) : (
                        [u.given_name, u.sn].filter(Boolean).join(" ") || "—"
                      )}
                    </td>
                    <td className="py-1.5 pr-4 text-muted-foreground">{u.mail || "—"}</td>
                    <td className="py-1.5 font-mono text-muted-foreground">{u.gid_number ?? "—"}</td>
                    {writeCapable && (
                      <td className="py-1.5 text-right">
                        <div className="flex items-center justify-end gap-1">
                          {editUser?.uid === u.uid ? (
                            <>
                              <Button size="sm" className="h-6 px-2 text-[11px]" onClick={() => updateMutation.mutate(u.uid)} disabled={updateMutation.isPending}>Save</Button>
                              <Button size="sm" variant="ghost" className="h-6 px-1" onClick={() => setEditUser(null)}><X className="h-3 w-3" /></Button>
                            </>
                          ) : (
                            <>
                              <Button size="sm" variant="ghost" className="h-6 w-6 p-0" onClick={() => startEdit(u)} title="Edit user">
                                <Pencil className="h-3 w-3" />
                              </Button>
                              <Button size="sm" variant="ghost" className="h-6 w-6 p-0" onClick={() => resetPwdMutation.mutate(u.uid)} disabled={resetPwdMutation.isPending} title="Reset password">
                                <KeyRound className="h-3 w-3" />
                              </Button>
                              <Button size="sm" variant="ghost" className="h-6 w-6 p-0 text-destructive hover:text-destructive" onClick={() => setDeleteConfirm(u)} title="Delete user">
                                <Trash2 className="h-3 w-3" />
                              </Button>
                            </>
                          )}
                        </div>
                      </td>
                    )}
                  </tr>
                  {/* Delete confirm row (WRITE-USER-6) */}
                  {deleteConfirm?.uid === u.uid && (
                    <tr className="border-b border-border/50 bg-destructive/5">
                      <td colSpan={writeCapable ? 5 : 4} className="px-1 py-2">
                        <div className="flex items-center gap-2 text-xs">
                          <span className="text-muted-foreground">Type <code className="font-mono">{u.uid}</code> to delete from directory:</span>
                          <Input className="h-6 w-32 text-[11px] font-mono" value={deleteInput} onChange={(e) => setDeleteInput(e.target.value)} autoFocus />
                          <Button size="sm" variant="destructive" className="h-6 px-2 text-[11px]"
                            disabled={deleteInput !== u.uid || deleteMutation.isPending}
                            onClick={() => deleteMutation.mutate(u.uid)}>
                            Delete
                          </Button>
                          <Button size="sm" variant="ghost" className="h-6 px-1" onClick={() => { setDeleteConfirm(null); setDeleteInput("") }}>
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
        )}
      </div>
    </div>
  )
}

// ─── Groups section (GRP-*) ────────────────────────────────────────────────────

function GroupsSection() {
  const [tab, setTab] = React.useState<"ldap" | "specialty">("ldap")
  return (
    <Section title="Groups" icon={Users} id="groups">
      <div className="flex gap-2 border-b border-border">
        <button
          className={cn("px-3 py-1.5 text-xs border-b-2 transition-colors", tab === "ldap" ? "border-primary text-foreground" : "border-transparent text-muted-foreground hover:text-foreground")}
          onClick={() => setTab("ldap")}
        >
          LDAP groups
        </button>
        <button
          className={cn("px-3 py-1.5 text-xs border-b-2 transition-colors", tab === "specialty" ? "border-primary text-foreground" : "border-transparent text-muted-foreground hover:text-foreground")}
          onClick={() => setTab("specialty")}
        >
          Specialty groups
        </button>
      </div>
      {tab === "ldap" ? <LDAPGroupsCard /> : <SpecialtyGroupsCard />}
    </Section>
  )
}

function LDAPGroupsCard() {
  const qc = useQueryClient()
  const [expandedCN, setExpandedCN] = React.useState<string | null>(null)
  const [addGroupOpen, setAddGroupOpen] = React.useState(false)
  const [deleteConfirm, setDeleteConfirm] = React.useState<LDAPGroup | null>(null)
  const [deleteInput, setDeleteInput] = React.useState("")

  // Add group form
  const [fCN, setFCN] = React.useState("")
  const [fGID, setFGID] = React.useState("")
  const [fDesc, setFDesc] = React.useState("")
  const [addError, setAddError] = React.useState("")

  const { data: configData } = useQuery<LDAPConfigResponse>({
    queryKey: ["ldap-config"],
    queryFn: () => apiFetch<LDAPConfigResponse>("/api/v1/ldap/config"),
    staleTime: 15000,
    retry: false,
  })
  const writeCapable = configData?.write_capable ?? false

  const { data, isLoading, refetch } = useQuery<ListLDAPGroupsResponse>({
    queryKey: ["ldap-groups"],
    queryFn: () => apiFetch<ListLDAPGroupsResponse>("/api/v1/ldap/groups"),
    staleTime: 30000,
    retry: false,
  })

  const createGroupMutation = useMutation({
    mutationFn: () => apiFetch("/api/v1/ldap/groups", {
      method: "POST",
      body: JSON.stringify({ cn: fCN, gid_number: Number(fGID) || 0, description: fDesc }),
    }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ldap-groups"] })
      setAddGroupOpen(false)
      setFCN(""); setFGID(""); setFDesc(""); setAddError("")
      toast({ title: "LDAP group created" })
    },
    onError: (err) => setAddError(String(err)),
  })

  const deleteGroupMutation = useMutation({
    mutationFn: (cn: string) => apiFetch(`/api/v1/ldap/groups/${encodeURIComponent(cn)}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ldap-groups"] })
      setDeleteConfirm(null); setDeleteInput("")
      toast({ title: "LDAP group deleted" })
      refetch()
    },
    onError: (err) => toast({ variant: "destructive", title: "Delete failed", description: String(err) }),
  })

  const groups = data?.groups ?? []

  return (
    <div className="rounded-md border border-border bg-card">
      <div className="flex items-center justify-between px-4 py-3 border-b border-border">
        <span className="text-xs font-medium text-muted-foreground">LDAP groups</span>
        {writeCapable && (
          <Button size="sm" variant="outline" className="h-7 text-xs" onClick={() => setAddGroupOpen(true)}>
            <Plus className="h-3 w-3 mr-1" /> Add LDAP group
          </Button>
        )}
      </div>

      {/* Write-mode banner */}
      {configData && <div className="px-4 pt-3"><WriteModeBanner config={configData} /></div>}

      {/* Add group form (WRITE-GRP-5) */}
      {addGroupOpen && (
        <div className="px-4 py-3 border-b border-border bg-secondary/10 space-y-2">
          <p className="text-xs font-medium">New LDAP group</p>
          <div className="grid grid-cols-2 gap-2">
            <Input className="text-xs h-7 font-mono" placeholder="CN (group name)" value={fCN} onChange={(e) => setFCN(e.target.value)} autoFocus />
            <Input className="text-xs h-7 font-mono" placeholder="GID number (0=auto)" type="number" value={fGID} onChange={(e) => setFGID(e.target.value)} />
          </div>
          <Input className="text-xs h-7" placeholder="Description (optional)" value={fDesc} onChange={(e) => setFDesc(e.target.value)} />
          {addError && <p className="text-xs text-destructive">{addError}</p>}
          <div className="flex gap-2">
            <Button size="sm" className="flex-1 text-xs" disabled={!fCN || createGroupMutation.isPending} onClick={() => createGroupMutation.mutate()}>
              {createGroupMutation.isPending ? "Creating…" : "Create group"}
            </Button>
            <Button size="sm" variant="ghost" className="text-xs" onClick={() => { setAddGroupOpen(false); setAddError("") }}>Cancel</Button>
          </div>
        </div>
      )}

      {isLoading ? (
        <div className="p-4 space-y-2"><Skeleton className="h-5 w-full" /><Skeleton className="h-5 w-2/3" /></div>
      ) : groups.length === 0 ? (
        <p className="px-4 py-3 text-xs text-muted-foreground">No LDAP groups found. Ensure the LDAP module is enabled.</p>
      ) : (
        <table className="w-full text-xs">
          <thead>
            <tr className="border-b border-border">
              <th className="px-4 py-2 text-left text-[11px] font-medium text-muted-foreground">CN</th>
              <th className="px-4 py-2 text-left text-[11px] font-medium text-muted-foreground">GID</th>
              <th className="px-4 py-2 text-left text-[11px] font-medium text-muted-foreground">Members</th>
              <th className="px-4 py-2 text-left text-[11px] font-medium text-muted-foreground">Mode</th>
              {writeCapable && <th className="px-4 py-2 text-right text-[11px] font-medium text-muted-foreground">Actions</th>}
            </tr>
          </thead>
          <tbody>
            {groups.map((g) => (
              <React.Fragment key={g.cn}>
                <tr
                  className="border-b border-border/50 hover:bg-secondary/20 cursor-pointer"
                  onClick={() => setExpandedCN(expandedCN === g.cn ? null : g.cn)}
                >
                  <td className="px-4 py-2 font-mono">{g.cn}</td>
                  <td className="px-4 py-2 font-mono text-muted-foreground">{g.gid_number}</td>
                  <td className="px-4 py-2 text-muted-foreground">{g.member_uids?.length ?? 0}</td>
                  <td className="px-4 py-2" onClick={(e) => e.stopPropagation()}>
                    {writeCapable
                      ? <GroupModeToggle cn={g.cn} />
                      : <span className="rounded px-1.5 py-0.5 text-[10px] bg-blue-500/10 text-blue-400">LDAP</span>
                    }
                  </td>
                  {writeCapable && (
                    <td className="px-4 py-2 text-right" onClick={(e) => e.stopPropagation()}>
                      <Button size="sm" variant="ghost" className="h-6 w-6 p-0 text-destructive hover:text-destructive"
                        onClick={() => setDeleteConfirm(g)} title="Delete group">
                        <Trash2 className="h-3 w-3" />
                      </Button>
                    </td>
                  )}
                </tr>
                {/* Delete confirm */}
                {deleteConfirm?.cn === g.cn && (
                  <tr className="border-b border-border/50 bg-destructive/5">
                    <td colSpan={writeCapable ? 5 : 4} className="px-4 py-2">
                      <div className="flex items-center gap-2 text-xs">
                        <span className="text-muted-foreground">Type <code className="font-mono">{g.cn}</code> to delete from directory:</span>
                        <Input className="h-6 w-32 text-[11px] font-mono" value={deleteInput} onChange={(e) => setDeleteInput(e.target.value)} autoFocus />
                        <Button size="sm" variant="destructive" className="h-6 px-2 text-[11px]"
                          disabled={deleteInput !== g.cn || deleteGroupMutation.isPending}
                          onClick={() => deleteGroupMutation.mutate(g.cn)}>
                          Delete
                        </Button>
                        <Button size="sm" variant="ghost" className="h-6 px-1" onClick={() => { setDeleteConfirm(null); setDeleteInput("") }}>
                          <X className="h-3 w-3" />
                        </Button>
                      </div>
                    </td>
                  </tr>
                )}
                {expandedCN === g.cn && (
                  <tr className="border-b border-border/50">
                    <td colSpan={writeCapable ? 5 : 4} className="px-4 py-3 bg-secondary/5">
                      <LDAPGroupDetail group={g} />
                    </td>
                  </tr>
                )}
              </React.Fragment>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

// ─── Group mode toggle (WRITE-GRP-4) ─────────────────────────────────────────

function GroupModeToggle({ cn: groupCN }: { cn: string }) {
  const qc = useQueryClient()
  const { data } = useQuery<LDAPGroupModeResponse>({
    queryKey: ["ldap-group-mode", groupCN],
    queryFn: () => apiFetch<LDAPGroupModeResponse>(`/api/v1/ldap/groups/${encodeURIComponent(groupCN)}/mode`),
    staleTime: 10000,
    retry: false,
  })

  const toggleMutation = useMutation({
    mutationFn: (newMode: "overlay" | "direct") =>
      apiFetch(`/api/v1/ldap/groups/${encodeURIComponent(groupCN)}/mode`, {
        method: "PUT",
        body: JSON.stringify({ mode: newMode }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ldap-group-mode", groupCN] })
      toast({ title: "Group mode updated" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Mode change failed", description: String(err) }),
  })

  const isDirect = data?.mode === "direct"

  return (
    <button
      className={cn(
        "inline-flex items-center gap-1 text-[10px] rounded px-1.5 py-0.5 transition-colors",
        isDirect
          ? "bg-amber-500/10 text-amber-400 hover:bg-amber-500/20"
          : "bg-blue-500/10 text-blue-400 hover:bg-blue-500/20"
      )}
      onClick={() => toggleMutation.mutate(isDirect ? "overlay" : "direct")}
      title={isDirect ? "Switch to overlay mode (no directory writes)" : "Switch to direct mode (writes to directory)"}
      disabled={toggleMutation.isPending}
    >
      {isDirect ? <ToggleRight className="h-3 w-3" /> : <ToggleLeft className="h-3 w-3" />}
      {isDirect ? "direct" : "overlay"}
    </button>
  )
}

function LDAPGroupDetail({ group }: { group: LDAPGroup }) {
  const qc = useQueryClient()
  const groupDN = encodeURIComponent(`cn=${group.cn}`)

  const { data: overlayData } = useQuery<{ members: GroupOverlay[] }>({
    queryKey: ["group-overlay", group.cn],
    queryFn: () => apiFetch<{ members: GroupOverlay[] }>(`/api/v1/groups/${encodeURIComponent(`cn=${group.cn}`)}/supplementary-members`),
    staleTime: 10000,
    retry: false,
  })

  const [addingMember, setAddingMember] = React.useState(false)

  const addOverlayMutation = useMutation({
    mutationFn: (u: UserSearchResult) =>
      apiFetch(`/api/v1/groups/${groupDN}/supplementary-members`, {
        method: "POST",
        body: JSON.stringify({ user_identifier: u.identifier, source: u.source }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["group-overlay", group.cn] })
      setAddingMember(false)
      toast({ title: "Supplementary member added" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Failed", description: String(err) }),
  })

  const removeOverlayMutation = useMutation({
    mutationFn: (uid: string) =>
      apiFetch(`/api/v1/groups/${groupDN}/supplementary-members/${encodeURIComponent(uid)}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["group-overlay", group.cn] })
      toast({ title: "Member removed" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Failed", description: String(err) }),
  })

  const overlayMembers = overlayData?.members ?? []

  return (
    <div className="space-y-3">
      {/* LDAP-native members */}
      <div>
        <p className="text-[11px] font-medium text-muted-foreground mb-1">LDAP-native members ({group.member_uids?.length ?? 0})</p>
        {(group.member_uids ?? []).length === 0 ? (
          <p className="text-[11px] text-muted-foreground italic">None</p>
        ) : (
          <div className="flex flex-wrap gap-1">
            {(group.member_uids ?? []).map((uid) => (
              <span key={uid} className="font-mono text-[11px] rounded bg-secondary px-1.5 py-0.5">{uid}</span>
            ))}
          </div>
        )}
      </div>

      {/* Clustr supplementary overlay */}
      <div>
        <p className="text-[11px] font-medium text-muted-foreground mb-1">Clustr supplementary members ({overlayMembers.length})</p>
        {overlayMembers.length === 0 ? (
          <p className="text-[11px] text-muted-foreground italic">None. These are added without writing the LDAP directory.</p>
        ) : (
          <div className="flex flex-wrap gap-1">
            {overlayMembers.map((m) => (
              <span key={m.user_identifier} className="inline-flex items-center gap-1 font-mono text-[11px] rounded bg-secondary px-1.5 py-0.5">
                {m.user_identifier}
                <button
                  className="text-muted-foreground hover:text-destructive"
                  onClick={() => removeOverlayMutation.mutate(m.user_identifier)}
                >
                  <X className="h-2.5 w-2.5" />
                </button>
              </span>
            ))}
          </div>
        )}
        {addingMember ? (
          <div className="mt-2 flex gap-2 items-center">
            <UserPicker
              onSelect={(u) => addOverlayMutation.mutate(u)}
              placeholder="Add supplementary member…"
              className="flex-1"
            />
            <Button size="sm" variant="ghost" className="h-7 text-xs" onClick={() => setAddingMember(false)}>Cancel</Button>
          </div>
        ) : (
          <Button size="sm" variant="outline" className="mt-2 h-6 text-[11px]" onClick={() => setAddingMember(true)}>
            <Plus className="h-3 w-3 mr-1" /> Add member
          </Button>
        )}
      </div>
    </div>
  )
}

function SpecialtyGroupsCard() {
  const qc = useQueryClient()
  const [createOpen, setCreateOpen] = React.useState(false)
  const [expandedId, setExpandedId] = React.useState<string | null>(null)
  const [deleteConfirm, setDeleteConfirm] = React.useState<SpecialtyGroup | null>(null)
  const [deleteInput, setDeleteInput] = React.useState("")

  // Create form
  const [newName, setNewName] = React.useState("")
  const [newGID, setNewGID] = React.useState("")
  const [newDesc, setNewDesc] = React.useState("")
  const [createError, setCreateError] = React.useState("")

  const { data, isLoading } = useQuery<ListSpecialtyGroupsResponse>({
    queryKey: ["specialty-groups"],
    queryFn: () => apiFetch<ListSpecialtyGroupsResponse>("/api/v1/groups/specialty"),
    staleTime: 10000,
  })

  const createMutation = useMutation({
    mutationFn: () =>
      apiFetch("/api/v1/groups/specialty", {
        method: "POST",
        body: JSON.stringify({ name: newName, gid_number: Number(newGID), description: newDesc }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["specialty-groups"] })
      setCreateOpen(false)
      setNewName(""); setNewGID(""); setNewDesc(""); setCreateError("")
      toast({ title: "Specialty group created" })
    },
    onError: (err) => setCreateError(String(err)),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) =>
      apiFetch(`/api/v1/groups/specialty/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["specialty-groups"] })
      setDeleteConfirm(null); setDeleteInput("")
      toast({ title: "Group deleted" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Delete failed", description: String(err) }),
  })

  const groups = data?.groups ?? []

  return (
    <div className="rounded-md border border-border bg-card">
      <div className="flex items-center justify-between px-4 py-3 border-b border-border">
        <span className="text-xs font-medium text-muted-foreground">Specialty groups (clustr-managed)</span>
        <Button size="sm" variant="outline" className="h-7 text-xs" onClick={() => setCreateOpen(true)}>
          <Plus className="h-3 w-3 mr-1" /> Create group
        </Button>
      </div>

      {/* Create form */}
      {createOpen && (
        <div className="px-4 py-3 border-b border-border bg-secondary/10 space-y-2">
          <p className="text-xs font-medium">New specialty group</p>
          <div className="grid grid-cols-2 gap-2">
            <Input className="text-xs h-7" placeholder="Name" value={newName} onChange={(e) => setNewName(e.target.value)} />
            <Input className="text-xs h-7 font-mono" placeholder="GID number" type="number" value={newGID} onChange={(e) => setNewGID(e.target.value)} />
          </div>
          <Input className="text-xs h-7" placeholder="Description (optional)" value={newDesc} onChange={(e) => setNewDesc(e.target.value)} />
          {createError && <p className="text-xs text-destructive">{createError}</p>}
          <div className="flex gap-2">
            <Button size="sm" className="flex-1 text-xs" disabled={!newName || !newGID || createMutation.isPending} onClick={() => createMutation.mutate()}>
              {createMutation.isPending ? "Creating…" : "Create"}
            </Button>
            <Button size="sm" variant="ghost" className="text-xs" onClick={() => { setCreateOpen(false); setCreateError("") }}>Cancel</Button>
          </div>
        </div>
      )}

      {isLoading ? (
        <div className="p-4 space-y-2"><Skeleton className="h-5 w-full" /></div>
      ) : groups.length === 0 ? (
        <p className="px-4 py-3 text-xs text-muted-foreground">No specialty groups. Create one above.</p>
      ) : (
        <table className="w-full text-xs">
          <thead>
            <tr className="border-b border-border">
              <th className="px-4 py-2 text-left text-[11px] font-medium text-muted-foreground">Name</th>
              <th className="px-4 py-2 text-left text-[11px] font-medium text-muted-foreground">GID</th>
              <th className="px-4 py-2 text-left text-[11px] font-medium text-muted-foreground">Members</th>
              <th className="px-4 py-2 text-left text-[11px] font-medium text-muted-foreground">Source</th>
              <th className="px-4 py-2"></th>
            </tr>
          </thead>
          <tbody>
            {groups.map((g) => (
              <React.Fragment key={g.id}>
                <tr
                  className="border-b border-border/50 hover:bg-secondary/20 cursor-pointer"
                  onClick={() => setExpandedId(expandedId === g.id ? null : g.id)}
                >
                  <td className="px-4 py-2 font-mono">{g.name}</td>
                  <td className="px-4 py-2 font-mono text-muted-foreground">{g.gid_number}</td>
                  <td className="px-4 py-2 text-muted-foreground">{g.members?.length ?? 0}</td>
                  <td className="px-4 py-2">
                    <span className="rounded px-1.5 py-0.5 text-[10px] bg-purple-500/10 text-purple-400">Specialty</span>
                  </td>
                  <td className="px-4 py-2 text-right">
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-6 w-6 p-0 text-destructive hover:text-destructive"
                      onClick={(e) => { e.stopPropagation(); setDeleteConfirm(g) }}
                    >
                      <Trash2 className="h-3 w-3" />
                    </Button>
                  </td>
                </tr>
                {/* Delete confirm */}
                {deleteConfirm?.id === g.id && (
                  <tr className="border-b border-border/50 bg-destructive/5">
                    <td colSpan={5} className="px-4 py-2">
                      <div className="flex items-center gap-2 text-xs">
                        <span className="text-muted-foreground">Type <code className="font-mono">{g.name}</code> to delete:</span>
                        <Input className="h-6 w-36 text-[11px] font-mono" value={deleteInput} onChange={(e) => setDeleteInput(e.target.value)} autoFocus />
                        <Button size="sm" variant="destructive" className="h-6 px-2 text-[11px]" disabled={deleteInput !== g.name || deleteMutation.isPending} onClick={() => deleteMutation.mutate(g.id)}>
                          Delete
                        </Button>
                        <Button size="sm" variant="ghost" className="h-6 px-1" onClick={() => { setDeleteConfirm(null); setDeleteInput("") }}>
                          <X className="h-3 w-3" />
                        </Button>
                      </div>
                    </td>
                  </tr>
                )}
                {/* Expanded member management */}
                {expandedId === g.id && (
                  <tr className="border-b border-border/50">
                    <td colSpan={5} className="px-4 py-3 bg-secondary/5">
                      <SpecialtyGroupDetail group={g} />
                    </td>
                  </tr>
                )}
              </React.Fragment>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

function SpecialtyGroupDetail({ group }: { group: SpecialtyGroup }) {
  const qc = useQueryClient()
  const [addingMember, setAddingMember] = React.useState(false)

  const addMutation = useMutation({
    mutationFn: (u: UserSearchResult) =>
      apiFetch(`/api/v1/groups/specialty/${group.id}/members`, {
        method: "POST",
        body: JSON.stringify({ user_identifier: u.identifier, source: u.source }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["specialty-groups"] })
      setAddingMember(false)
      toast({ title: "Member added" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Failed", description: String(err) }),
  })

  const removeMutation = useMutation({
    mutationFn: (uid: string) =>
      apiFetch(`/api/v1/groups/specialty/${group.id}/members/${encodeURIComponent(uid)}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["specialty-groups"] })
      toast({ title: "Member removed" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Failed", description: String(err) }),
  })

  return (
    <div className="space-y-2">
      {group.description && <p className="text-[11px] text-muted-foreground">{group.description}</p>}
      <div className="flex flex-wrap gap-1">
        {(group.members ?? []).map((uid) => (
          <span key={uid} className="inline-flex items-center gap-1 font-mono text-[11px] rounded bg-secondary px-1.5 py-0.5">
            {uid}
            <button className="text-muted-foreground hover:text-destructive" onClick={() => removeMutation.mutate(uid)}>
              <X className="h-2.5 w-2.5" />
            </button>
          </span>
        ))}
        {(group.members ?? []).length === 0 && <span className="text-[11px] text-muted-foreground italic">No members</span>}
      </div>
      {addingMember ? (
        <div className="flex gap-2 items-center mt-1">
          <UserPicker onSelect={(u) => addMutation.mutate(u)} placeholder="Add member…" className="flex-1" />
          <Button size="sm" variant="ghost" className="h-7 text-xs" onClick={() => setAddingMember(false)}>Cancel</Button>
        </div>
      ) : (
        <Button size="sm" variant="outline" className="h-6 text-[11px] mt-1" onClick={() => setAddingMember(true)}>
          <Plus className="h-3 w-3 mr-1" /> Add member
        </Button>
      )}
    </div>
  )
}

// ─── System accounts section (SYSACCT-*) ─────────────────────────────────────

interface SysAccount {
  id: string
  username: string
  uid: number
  primary_gid: number
  shell: string
  home_dir: string
  create_home: boolean
  system_account: boolean
  comment: string
}

function SystemAccountsSection() {
  const qc = useQueryClient()
  const [addOpen, setAddOpen] = React.useState(false)
  const [deleteConfirm, setDeleteConfirm] = React.useState<SysAccount | null>(null)
  const [deleteInput, setDeleteInput] = React.useState("")
  const [addError, setAddError] = React.useState("")

  // Form state
  const [fUsername, setFUsername] = React.useState("")
  const [fUID, setFUID] = React.useState("")
  const [fGID, setFGID] = React.useState("")
  const [fShell, setFShell] = React.useState("/bin/bash")
  const [fHome, setFHome] = React.useState("")
  const [fComment, setFComment] = React.useState("")

  const { data, isLoading } = useQuery<{ accounts: SysAccount[] }>({
    queryKey: ["system-accounts"],
    queryFn: () => apiFetch<{ accounts: SysAccount[] }>("/api/v1/system/accounts"),
    staleTime: 10000,
  })

  const createMutation = useMutation({
    mutationFn: () =>
      apiFetch("/api/v1/system/accounts", {
        method: "POST",
        body: JSON.stringify({
          username: fUsername,
          uid: Number(fUID) || 0,
          primary_gid: Number(fGID) || 0,
          shell: fShell,
          home_dir: fHome || `/home/${fUsername}`,
          comment: fComment,
          system_account: true,
        }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["system-accounts"] })
      setAddOpen(false)
      setFUsername(""); setFUID(""); setFGID(""); setFShell("/bin/bash"); setFHome(""); setFComment(""); setAddError("")
      toast({ title: "System account created" })
    },
    onError: (err) => setAddError(String(err)),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) =>
      apiFetch(`/api/v1/system/accounts/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["system-accounts"] })
      setDeleteConfirm(null); setDeleteInput("")
      toast({ title: "Account deleted" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Delete failed", description: String(err) }),
  })

  const accounts = data?.accounts ?? []

  return (
    <Section title="System accounts" icon={Database} id="system-accounts">
      <div className="rounded-md border border-border bg-card">
        <div className="flex items-center justify-between px-4 py-3 border-b border-border">
          <span className="text-xs font-medium text-muted-foreground">Linux accounts provisioned on nodes during cloning</span>
          <Button size="sm" variant="outline" className="h-7 text-xs" onClick={() => setAddOpen(true)}>
            <Plus className="h-3 w-3 mr-1" /> Add account
          </Button>
        </div>

        {addOpen && (
          <div className="px-4 py-3 border-b border-border bg-secondary/10 space-y-2">
            <p className="text-xs font-medium">New system account</p>
            <div className="grid grid-cols-3 gap-2">
              <Input className="text-xs h-7 font-mono" placeholder="Username" value={fUsername} onChange={(e) => setFUsername(e.target.value)} />
              <Input className="text-xs h-7 font-mono" placeholder="UID (0=auto)" type="number" value={fUID} onChange={(e) => setFUID(e.target.value)} />
              <Input className="text-xs h-7 font-mono" placeholder="Primary GID" type="number" value={fGID} onChange={(e) => setFGID(e.target.value)} />
            </div>
            <div className="grid grid-cols-2 gap-2">
              <Input className="text-xs h-7 font-mono" placeholder="Shell" value={fShell} onChange={(e) => setFShell(e.target.value)} />
              <Input className="text-xs h-7 font-mono" placeholder={`Home dir (/home/${fUsername})`} value={fHome} onChange={(e) => setFHome(e.target.value)} />
            </div>
            <Input className="text-xs h-7" placeholder="Comment (optional)" value={fComment} onChange={(e) => setFComment(e.target.value)} />
            {addError && <p className="text-xs text-destructive">{addError}</p>}
            <div className="flex gap-2">
              <Button size="sm" className="flex-1 text-xs" disabled={!fUsername || createMutation.isPending} onClick={() => createMutation.mutate()}>
                {createMutation.isPending ? "Creating…" : "Create account"}
              </Button>
              <Button size="sm" variant="ghost" className="text-xs" onClick={() => { setAddOpen(false); setAddError("") }}>Cancel</Button>
            </div>
          </div>
        )}

        {isLoading ? (
          <div className="p-4 space-y-2"><Skeleton className="h-5 w-full" /></div>
        ) : accounts.length === 0 ? (
          <p className="px-4 py-3 text-xs text-muted-foreground">No system accounts. These are provisioned on every deployed node.</p>
        ) : (
          <table className="w-full text-xs">
            <thead>
              <tr className="border-b border-border">
                <th className="px-4 py-2 text-left text-[11px] font-medium text-muted-foreground">Username</th>
                <th className="px-4 py-2 text-left text-[11px] font-medium text-muted-foreground">UID</th>
                <th className="px-4 py-2 text-left text-[11px] font-medium text-muted-foreground">GID</th>
                <th className="px-4 py-2 text-left text-[11px] font-medium text-muted-foreground">Shell</th>
                <th className="px-4 py-2 text-left text-[11px] font-medium text-muted-foreground">Home</th>
                <th className="px-4 py-2"></th>
              </tr>
            </thead>
            <tbody>
              {accounts.map((a) => (
                <React.Fragment key={a.id}>
                  <tr className="border-b border-border/50 hover:bg-secondary/20">
                    <td className="px-4 py-2 font-mono">{a.username}</td>
                    <td className="px-4 py-2 font-mono text-muted-foreground">{a.uid || "auto"}</td>
                    <td className="px-4 py-2 font-mono text-muted-foreground">{a.primary_gid || "auto"}</td>
                    <td className="px-4 py-2 font-mono text-muted-foreground text-[11px]">{a.shell}</td>
                    <td className="px-4 py-2 font-mono text-muted-foreground text-[11px] max-w-[120px] truncate">{a.home_dir}</td>
                    <td className="px-4 py-2 text-right">
                      <Button size="sm" variant="ghost" className="h-6 w-6 p-0 text-destructive hover:text-destructive" onClick={() => setDeleteConfirm(a)}>
                        <Trash2 className="h-3 w-3" />
                      </Button>
                    </td>
                  </tr>
                  {deleteConfirm?.id === a.id && (
                    <tr className="border-b border-border/50 bg-destructive/5">
                      <td colSpan={6} className="px-4 py-2">
                        <div className="flex items-center gap-2 text-xs">
                          <span className="text-muted-foreground">Type <code className="font-mono">{a.username}</code> to delete:</span>
                          <Input className="h-6 w-36 text-[11px] font-mono" value={deleteInput} onChange={(e) => setDeleteInput(e.target.value)} autoFocus />
                          <Button size="sm" variant="destructive" className="h-6 px-2 text-[11px]" disabled={deleteInput !== a.username || deleteMutation.isPending} onClick={() => deleteMutation.mutate(a.id)}>
                            Delete
                          </Button>
                          <Button size="sm" variant="ghost" className="h-6 px-1" onClick={() => { setDeleteConfirm(null); setDeleteInput("") }}>
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
        )}
      </div>
    </Section>
  )
}

// ─── LDAP config section (LDAP-1..5, WRITE-CFG-3) ────────────────────────────

function LDAPConfigSection() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery<LDAPConfigResponse>({
    queryKey: ["ldap-config"],
    queryFn: () => apiFetch<LDAPConfigResponse>("/api/v1/ldap/config"),
    staleTime: 15000,
    retry: false,
  })

  const [testResult, setTestResult] = React.useState<LDAPTestResponse | null>(null)
  const [testing, setTesting] = React.useState(false)

  // Write-bind form state (WRITE-CFG-3)
  const [writeBindOpen, setWriteBindOpen] = React.useState(false)
  const [wbDN, setWbDN] = React.useState("")
  const [wbPass, setWbPass] = React.useState("")
  const [wbSaving, setWbSaving] = React.useState(false)
  const [wbResult, setWbResult] = React.useState<string | null>(null)

  async function handleTest() {
    setTesting(true)
    setTestResult(null)
    try {
      const res = await apiFetch<LDAPTestResponse>("/api/v1/ldap/test", { method: "POST" })
      setTestResult(res)
    } catch (err) {
      setTestResult({ ok: false, error: String(err) })
    } finally {
      setTesting(false)
    }
  }

  async function handleSaveWriteBind() {
    setWbSaving(true)
    setWbResult(null)
    try {
      const res = await apiFetch<{ write_capable: boolean; write_status: { capable: boolean; detail: string } }>(
        "/api/v1/ldap/write-bind",
        { method: "PUT", body: JSON.stringify({ write_bind_dn: wbDN, write_bind_password: wbPass }) }
      )
      setWbResult(res.write_capable ? "Write probe OK" : `Write probe failed: ${res.write_status?.detail ?? "unknown"}`)
      qc.invalidateQueries({ queryKey: ["ldap-config"] })
      setWbPass("") // clear password from form after save
    } catch (err) {
      setWbResult(`Error: ${String(err)}`)
    } finally {
      setWbSaving(false)
    }
  }

  return (
    <Section title="LDAP config" icon={Settings} id="ldap-config">
      <div className="rounded-md border border-border bg-card">
        <div className="flex items-center justify-between px-4 py-3 border-b border-border">
          <span className="text-xs font-medium text-muted-foreground">Built-in OpenLDAP module</span>
          <div className="flex gap-2">
            <Button size="sm" variant="outline" className="h-7 text-xs" onClick={handleTest} disabled={testing}>
              {testing ? "Testing…" : "Test connection"}
            </Button>
          </div>
        </div>

        {testResult && (
          <div className={cn(
            "px-4 py-2 text-xs border-b border-border",
            testResult.ok ? "bg-green-500/5 text-green-400" : "bg-destructive/5 text-destructive"
          )}>
            {testResult.ok
              ? `Connection OK — ${testResult.user_count ?? 0} users in ${testResult.base_dn}`
              : `Connection failed: ${testResult.error}`
            }
          </div>
        )}

        {isLoading ? (
          <div className="p-4 space-y-2"><Skeleton className="h-5 w-full" /></div>
        ) : (
          <div className="px-4 py-3 space-y-2">
            <LDAPConfigRow label="Status">
              <span className={cn(
                "rounded px-1.5 py-0.5 text-[10px] font-medium",
                data?.status === "ready" ? "bg-green-500/10 text-green-400" :
                  data?.status === "provisioning" ? "bg-amber-500/10 text-amber-400" :
                    "bg-secondary text-muted-foreground"
              )}>
                {data?.status ?? "disabled"}
              </span>
              {data?.status_detail && (
                <span className="text-[11px] text-muted-foreground ml-2">{data.status_detail}</span>
              )}
            </LDAPConfigRow>
            <LDAPConfigRow label="Base DN">
              <code className="font-mono text-[11px]">{data?.base_dn || "—"}</code>
              {data?.base_dn_locked && (
                <span className="ml-2 text-[10px] text-amber-400 rounded px-1 bg-amber-500/10">locked</span>
              )}
            </LDAPConfigRow>
            <LDAPConfigRow label="Service bind DN">
              <code className="font-mono text-[11px]">{data?.service_bind_dn || "—"}</code>
            </LDAPConfigRow>
            <LDAPConfigRow label="CA fingerprint">
              <code className="font-mono text-[11px] break-all">{data?.ca_fingerprint || "—"}</code>
            </LDAPConfigRow>
            <LDAPConfigRow label="Read bind">
              <span className="text-[11px] text-muted-foreground">{data?.bind_password_set ? "set" : "not set"}</span>
            </LDAPConfigRow>

            {/* Sprint 8 write-bind config (WRITE-CFG-3) */}
            <LDAPConfigRow label="Write bind">
              <div className="flex items-center gap-2">
                <span className={cn(
                  "rounded px-1.5 py-0.5 text-[10px] font-medium",
                  data?.write_capable === true ? "bg-green-500/10 text-green-400" :
                    data?.write_bind_dn_set ? "bg-amber-500/10 text-amber-400" :
                      "bg-secondary text-muted-foreground"
                )}>
                  {data?.write_capable === true ? "write-capable"
                    : data?.write_bind_dn_set ? "unverified"
                    : "not set (DM fallback)"}
                </span>
                <Button size="sm" variant="ghost" className="h-5 px-1.5 text-[10px]" onClick={() => setWriteBindOpen((v) => !v)}>
                  {writeBindOpen ? "Cancel" : "Configure"}
                </Button>
              </div>
            </LDAPConfigRow>
            <LDAPConfigRow label="Backend">
              <span className="text-[11px] text-muted-foreground">{data?.backend_dialect ?? "openldap"}</span>
              <span className="ml-1 text-[10px] text-green-400 rounded px-1 bg-green-500/10">implemented</span>
            </LDAPConfigRow>

            {/* Write-bind form */}
            {writeBindOpen && (
              <div className="mt-2 rounded border border-border bg-secondary/5 px-3 py-2 space-y-2">
                <p className="text-[11px] font-medium">Write bind credentials</p>
                <p className="text-[11px] text-muted-foreground">
                  Optional. Required only if you want to create/edit/delete users and groups in LDAP from clustr.
                  Leave password blank to keep the existing one.
                </p>
                <Input className="text-xs h-7 font-mono" placeholder="Write bind DN (e.g. cn=admin,dc=cluster,dc=local)" value={wbDN} onChange={(e) => setWbDN(e.target.value)} />
                <Input className="text-xs h-7 font-mono" type="password" placeholder="Write bind password (leave blank to keep)" value={wbPass} onChange={(e) => setWbPass(e.target.value)} />
                {wbResult && (
                  <p className={cn("text-[11px]", wbResult.startsWith("Write probe OK") ? "text-green-400" : "text-destructive")}>{wbResult}</p>
                )}
                <div className="flex gap-2">
                  <Button size="sm" className="flex-1 text-xs" disabled={wbSaving} onClick={handleSaveWriteBind}>
                    {wbSaving ? "Saving & probing…" : "Save + probe write"}
                  </Button>
                  <Button size="sm" variant="ghost" className="text-xs" onClick={() => { setWriteBindOpen(false); setWbResult(null) }}>Cancel</Button>
                </div>
              </div>
            )}

            {!data?.enabled && (
              <div className="rounded border border-amber-500/30 bg-amber-500/5 px-3 py-2 mt-3">
                <p className="text-[11px] text-amber-400 font-medium">LDAP module not enabled</p>
                <p className="text-[11px] text-muted-foreground mt-0.5">
                  Use the LDAP enable API or CLI to provision the built-in OpenLDAP server:
                </p>
                <code className="block font-mono text-[11px] mt-1 text-muted-foreground">
                  POST /api/v1/ldap/enable {"{"} "base_dn": "dc=cluster,dc=local", "admin_password": "…" {"}"}
                </code>
              </div>
            )}
          </div>
        )}
      </div>
    </Section>
  )
}

function LDAPConfigRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start gap-4">
      <span className="text-[11px] text-muted-foreground w-32 shrink-0 pt-0.5">{label}</span>
      <div className="flex items-center gap-1 flex-wrap">{children}</div>
    </div>
  )
}
