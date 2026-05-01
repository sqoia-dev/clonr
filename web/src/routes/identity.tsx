import * as React from "react"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { Search, Plus, Pencil, Trash2, X, Eye, EyeOff, ShieldCheck, Users, Settings, Database, AlertTriangle, CheckCircle2, ToggleLeft, ToggleRight, KeyRound, Loader2, Server, ExternalLink } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetFooter } from "@/components/ui/sheet"
import { apiFetch } from "@/lib/api"
import { toast } from "@/hooks/use-toast"
import { UserPicker } from "@/components/UserPicker"
import { cn } from "@/lib/utils"
import type {
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
  LDAPSourceModeResponse,
  LDAPInternalStatusResponse,
  LDAPInternalEnableError,
  LDAPAdminPasswordResponse,
  LDAPPatchUserRequest,
} from "@/lib/types"
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

// ─── Users section (USERS-1..4) — LDAP directory users only ──────────────────

function UsersSection() {
  return (
    <Section title="Users" icon={Users} id="users">
      <LDAPUsersCard />
    </Section>
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

// ─── LDAP Users Card (WRITE-USER-5, Sprint 13 #93+#94+#95) ──────────────────

function LDAPUsersCard() {
  const qc = useQueryClient()
  const [q, setQ] = React.useState("")
  const [addOpen, setAddOpen] = React.useState(false)
  const [editSheetUser, setEditSheetUser] = React.useState<LDAPUser | null>(null) // #95 Sheet
  const [deleteConfirm, setDeleteConfirm] = React.useState<LDAPUser | null>(null)
  const [deleteInput, setDeleteInput] = React.useState("")
  const [resetResult, setResetResult] = React.useState<{ uid: string; pwd: string } | null>(null)
  const [showTempPwd, setShowTempPwd] = React.useState(false)
  const [copyDone, setCopyDone] = React.useState(false)

  // Add form state (#94: email + ssh_keys added)
  const [fUID, setFUID] = React.useState("")
  const [fCN, setFCN] = React.useState("")
  const [fSN, setFSN] = React.useState("")
  const [fEmail, setFEmail] = React.useState("")           // #94
  const [fSSHKeys, setFSSHKeys] = React.useState("")       // #94 multi-line textarea
  const [fUIDOverride, setFUIDOverride] = React.useState(false)  // #93 toggle
  const [fGIDOverride, setFGIDOverride] = React.useState(false)  // #93 toggle
  const [fUID_num, setFUID_num] = React.useState("")
  const [fGID_num, setFGID_num] = React.useState("")
  const [fHome, setFHome] = React.useState("")
  const [fShell, setFShell] = React.useState("/bin/bash")
  const [fPassword, setFPassword] = React.useState("")
  const [addError, setAddError] = React.useState("")

  // Get write-capable status for banner
  const { data: configData } = useQuery<LDAPConfigResponse>({
    queryKey: ["ldap-config"],
    queryFn: () => apiFetch<LDAPConfigResponse>("/api/v1/ldap/config"),
    staleTime: 15000,
    retry: false,
  })

  const { data, isFetching, isLoading, refetch } = useQuery<{ users: LDAPUser[]; total: number }>({
    queryKey: ["ldap-users-search", q],
    queryFn: () => apiFetch<{ users: LDAPUser[]; total: number }>(`/api/v1/ldap/users/search?q=${encodeURIComponent(q)}`),
    staleTime: 30000,
    retry: false,
  })

  const { data: groupsData } = useQuery<{ groups: LDAPGroup[]; total: number }>({
    queryKey: ["ldap-groups"],
    queryFn: () => apiFetch<{ groups: LDAPGroup[]; total: number }>("/api/v1/ldap/groups"),
    staleTime: 30000,
    retry: false,
  })
  const allGroups = groupsData?.groups ?? []

  const createMutation = useMutation({
    mutationFn: () => {
      const sshKeys = fSSHKeys.split("\n").map(k => k.trim()).filter(Boolean)
      return apiFetch("/api/v1/ldap/users", {
        method: "POST",
        body: JSON.stringify({
          uid: fUID,
          cn: fCN || fUID,
          sn: fSN || fUID,
          mail: fEmail || undefined,
          uid_number: fUIDOverride ? (Number(fUID_num) || 0) : 0,
          gid_number: fGIDOverride ? (Number(fGID_num) || 0) : 0,
          home_directory: fHome || `/home/${fUID}`,
          login_shell: fShell,
          password: fPassword || undefined,
          ssh_public_keys: sshKeys.length > 0 ? sshKeys : undefined,
        }),
      })
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ldap-users-search"] })
      setAddOpen(false)
      setFUID(""); setFCN(""); setFSN(""); setFEmail(""); setFSSHKeys("")
      setFUID_num(""); setFGID_num(""); setFUIDOverride(false); setFGIDOverride(false)
      setFHome(""); setFShell("/bin/bash"); setFPassword(""); setAddError("")
      toast({ title: "LDAP user created" })
      refetch()
    },
    onError: (err) => setAddError(String(err)),
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

  function handleSearch() { refetch() }

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

      {/* Add form (#94: email + ssh_keys; #93: auto-allocate toggle) */}
      {addOpen && (
        <div className="px-4 py-3 border-b border-border bg-secondary/10 space-y-2">
          <p className="text-xs font-medium">New LDAP user</p>
          <div className="grid grid-cols-3 gap-2">
            <Input className="text-xs h-7 font-mono" placeholder="UID (username) *" value={fUID} onChange={(e) => setFUID(e.target.value)} autoFocus />
            <Input className="text-xs h-7" placeholder="Display name (CN)" value={fCN} onChange={(e) => setFCN(e.target.value)} />
            <Input className="text-xs h-7" placeholder="Surname (SN)" value={fSN} onChange={(e) => setFSN(e.target.value)} />
          </div>
          {/* #94: email field */}
          <Input className="text-xs h-7" placeholder="Email (mail attribute)" type="email" value={fEmail} onChange={(e) => setFEmail(e.target.value)} />
          {/* #93: UID override toggle */}
          <div className="space-y-1">
            <div className="flex items-center gap-2">
              <button
                type="button"
                className={cn("text-[11px] px-2 py-0.5 rounded border transition-colors",
                  fUIDOverride ? "border-primary bg-primary/10 text-primary" : "border-border text-muted-foreground hover:text-foreground")}
                onClick={() => setFUIDOverride(v => !v)}
              >
                {fUIDOverride ? "UID: override" : "UID: auto-allocate"}
              </button>
              {fUIDOverride && (
                <Input className="text-xs h-7 font-mono w-36" placeholder="UID number" type="number" value={fUID_num} onChange={(e) => setFUID_num(e.target.value)} />
              )}
              <button
                type="button"
                className={cn("text-[11px] px-2 py-0.5 rounded border transition-colors",
                  fGIDOverride ? "border-primary bg-primary/10 text-primary" : "border-border text-muted-foreground hover:text-foreground")}
                onClick={() => setFGIDOverride(v => !v)}
              >
                {fGIDOverride ? "GID: override" : "GID: auto-allocate"}
              </button>
              {fGIDOverride && (
                <Input className="text-xs h-7 font-mono w-36" placeholder="GID number" type="number" value={fGID_num} onChange={(e) => setFGID_num(e.target.value)} />
              )}
            </div>
            {!fUIDOverride && <p className="text-[11px] text-muted-foreground">Server assigns the next free UID ≥ 10000.</p>}
          </div>
          <div className="grid grid-cols-2 gap-2">
            <Input className="text-xs h-7 font-mono" placeholder={`Home dir (/home/${fUID || "…"})`} value={fHome} onChange={(e) => setFHome(e.target.value)} />
            <Input className="text-xs h-7 font-mono" placeholder="Shell (/bin/bash)" value={fShell} onChange={(e) => setFShell(e.target.value)} />
          </div>
          <Input className="text-xs h-7 font-mono" type="password" placeholder="Initial password (optional)" value={fPassword} onChange={(e) => setFPassword(e.target.value)} />
          {/* #94: SSH keys textarea */}
          <div>
            <p className="text-[11px] text-muted-foreground mb-1">SSH public keys (one per line, optional)</p>
            <textarea
              className="w-full text-xs font-mono bg-background border border-border rounded px-2 py-1.5 resize-none focus:outline-none focus:ring-1 focus:ring-ring"
              rows={2}
              placeholder="ssh-ed25519 AAAA…"
              value={fSSHKeys}
              onChange={(e) => setFSSHKeys(e.target.value)}
            />
          </div>
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

        {isLoading && (
          <div className="space-y-2">
            <Skeleton className="h-5 w-full" />
            <Skeleton className="h-5 w-3/4" />
          </div>
        )}
        {!isLoading && !isFetching && data === undefined && (
          <p className="text-xs text-muted-foreground">Configure LDAP to see directory users.</p>
        )}
        {!isLoading && !isFetching && data !== undefined && users.length === 0 && (
          <p className="text-xs text-muted-foreground">No users found in directory. Try a different search.</p>
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
                    <td className="py-1.5 pr-4">{[u.given_name, u.sn].filter(Boolean).join(" ") || "—"}</td>
                    <td className="py-1.5 pr-4 text-muted-foreground">{u.mail || "—"}</td>
                    <td className="py-1.5 font-mono text-muted-foreground">{u.gid_number ?? "—"}</td>
                    {writeCapable && (
                      <td className="py-1.5 text-right">
                        <div className="flex items-center justify-end gap-1">
                          <Button size="sm" variant="ghost" className="h-6 w-6 p-0" onClick={() => setEditSheetUser(u)} title="Edit user">
                            <Pencil className="h-3 w-3" />
                          </Button>
                          <Button size="sm" variant="ghost" className="h-6 w-6 p-0" onClick={() => resetPwdMutation.mutate(u.uid)} disabled={resetPwdMutation.isPending} title="Reset password">
                            <KeyRound className="h-3 w-3" />
                          </Button>
                          <Button size="sm" variant="ghost" className="h-6 w-6 p-0 text-destructive hover:text-destructive" onClick={() => setDeleteConfirm(u)} title="Delete user">
                            <Trash2 className="h-3 w-3" />
                          </Button>
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

      {/* #95: Edit user Sheet */}
      {editSheetUser && (
        <LDAPUserEditSheet
          user={editSheetUser}
          allGroups={allGroups}
          onClose={() => { setEditSheetUser(null); refetch() }}
        />
      )}
    </div>
  )
}

// ─── #95: LDAP User Edit Sheet ────────────────────────────────────────────────

function LDAPUserEditSheet({
  user,
  allGroups,
  onClose,
}: {
  user: LDAPUser
  allGroups: LDAPGroup[]
  onClose: () => void
}) {
  const qc = useQueryClient()

  // Derive current group membership for this user
  const currentGroups = allGroups.filter(g => g.member_uids?.includes(user.uid)).map(g => g.cn)

  const [eName, setEName] = React.useState([user.given_name ?? "", user.sn ?? ""].filter(Boolean).join(" "))
  const [eMail, setEMail] = React.useState(user.mail ?? "")
  const [eGID, setEGID] = React.useState(user.gid_number != null ? String(user.gid_number) : "")
  const [eSSHKeys, setESSHKeys] = React.useState((user.ssh_public_keys ?? []).join("\n"))
  const [eGroups, setEGroups] = React.useState<string[]>(currentGroups)
  const [saveError, setSaveError] = React.useState("")

  // Parse "First Last" → {given_name, sn}
  function parseName(full: string): { given_name: string; sn: string; cn: string } {
    const parts = full.trim().split(/\s+/)
    if (parts.length === 1) return { given_name: parts[0], sn: parts[0], cn: parts[0] }
    const sn = parts[parts.length - 1]
    const given_name = parts.slice(0, -1).join(" ")
    return { given_name, sn, cn: full.trim() }
  }

  const patchMutation = useMutation({
    mutationFn: () => {
      const { given_name, sn, cn } = parseName(eName)
      const sshKeys = eSSHKeys.split("\n").map(k => k.trim()).filter(Boolean)

      // Compute group diffs
      const addGroups = eGroups.filter(g => !currentGroups.includes(g))
      const removeGroups = currentGroups.filter(g => !eGroups.includes(g))

      const body: LDAPPatchUserRequest = {
        cn: cn || undefined,
        sn: sn || undefined,
        given_name: given_name || undefined,
        mail: eMail || undefined,
        gid_number: eGID !== "" ? Number(eGID) : undefined,
        ssh_public_keys: sshKeys,
        add_groups: addGroups.length > 0 ? addGroups : undefined,
        remove_groups: removeGroups.length > 0 ? removeGroups : undefined,
      }

      return apiFetch(`/api/v1/ldap/users/${encodeURIComponent(user.uid)}`, {
        method: "PATCH",
        body: JSON.stringify(body),
      })
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ldap-users-search"] })
      qc.invalidateQueries({ queryKey: ["ldap-groups"] })
      toast({ title: "User updated" })
      onClose()
    },
    onError: (err) => setSaveError(String(err)),
  })

  function toggleGroup(cn: string) {
    setEGroups(prev => prev.includes(cn) ? prev.filter(g => g !== cn) : [...prev, cn])
  }

  return (
    <Sheet open onOpenChange={(open) => { if (!open) onClose() }}>
      <SheetContent className="w-[420px] sm:w-[480px] overflow-y-auto">
        <SheetHeader>
          <SheetTitle className="text-sm">Edit LDAP user — <span className="font-mono">{user.uid}</span></SheetTitle>
        </SheetHeader>

        <div className="space-y-4 py-4">
          {/* UID — immutable */}
          <div className="space-y-1">
            <label className="text-[11px] font-medium text-muted-foreground">UID (username)</label>
            <div className="h-8 px-2 flex items-center rounded border border-border bg-secondary/30 font-mono text-xs text-muted-foreground">
              {user.uid}
              <span className="ml-2 text-[10px] text-muted-foreground/60">(immutable)</span>
            </div>
          </div>

          {/* Full name */}
          <div className="space-y-1">
            <label className="text-[11px] font-medium text-muted-foreground">Full name</label>
            <Input
              className="text-xs h-8"
              placeholder="First Last"
              value={eName}
              onChange={(e) => setEName(e.target.value)}
            />
            <p className="text-[10px] text-muted-foreground">Split on last space: first part → givenName, last → sn</p>
          </div>

          {/* Email (#94) */}
          <div className="space-y-1">
            <label className="text-[11px] font-medium text-muted-foreground">Email (mail)</label>
            <Input
              className="text-xs h-8"
              type="email"
              placeholder="user@example.com"
              value={eMail}
              onChange={(e) => setEMail(e.target.value)}
            />
          </div>

          {/* GID (#95 with allocator validation) */}
          <div className="space-y-1">
            <label className="text-[11px] font-medium text-muted-foreground">Primary GID (gidNumber)</label>
            <Input
              className="text-xs h-8 font-mono"
              type="number"
              placeholder="Leave empty to keep current"
              value={eGID}
              onChange={(e) => setEGID(e.target.value)}
            />
            {eGID !== "" && (Number(eGID) < 10000 || Number(eGID) > 60000) && (
              <p className="text-[11px] text-amber-500">GID must be in range 10000–60000 (server validates before saving)</p>
            )}
          </div>

          {/* Supplementary groups */}
          <div className="space-y-1">
            <label className="text-[11px] font-medium text-muted-foreground">Supplementary groups</label>
            {allGroups.length === 0 ? (
              <p className="text-[11px] text-muted-foreground">No LDAP groups available.</p>
            ) : (
              <div className="flex flex-wrap gap-1.5 border border-border rounded p-2 min-h-[40px]">
                {allGroups.map((g) => (
                  <button
                    key={g.cn}
                    type="button"
                    onClick={() => toggleGroup(g.cn)}
                    className={cn(
                      "text-[11px] px-2 py-0.5 rounded border transition-colors",
                      eGroups.includes(g.cn)
                        ? "border-primary bg-primary/15 text-primary"
                        : "border-border text-muted-foreground hover:text-foreground hover:border-foreground/40"
                    )}
                  >
                    {g.cn}
                  </button>
                ))}
              </div>
            )}
            <p className="text-[10px] text-muted-foreground">Changes add/remove memberUid on the group entries.</p>
          </div>

          {/* SSH public keys (#94/#95) */}
          <div className="space-y-1">
            <label className="text-[11px] font-medium text-muted-foreground">SSH public keys (sshPublicKey)</label>
            <textarea
              className="w-full text-xs font-mono bg-background border border-border rounded px-2 py-1.5 resize-none focus:outline-none focus:ring-1 focus:ring-ring"
              rows={3}
              placeholder={"ssh-ed25519 AAAA…\nssh-rsa AAAA…"}
              value={eSSHKeys}
              onChange={(e) => setESSHKeys(e.target.value)}
            />
            <p className="text-[10px] text-muted-foreground">One key per line. Replaces all existing keys.</p>
          </div>

          {saveError && <p className="text-xs text-destructive">{saveError}</p>}
        </div>

        <SheetFooter>
          <Button variant="ghost" size="sm" className="text-xs" onClick={onClose}>Cancel</Button>
          <Button size="sm" className="text-xs" disabled={patchMutation.isPending} onClick={() => patchMutation.mutate()}>
            {patchMutation.isPending ? "Saving…" : "Save changes"}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
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

// ─── LDAP config section (Sprint 9 — mode toggle + internal enable) ──────────

function LDAPConfigSection() {
  const qc = useQueryClient()

  // Source mode (internal vs external)
  const { data: modeData, isLoading: modeLoading } = useQuery<LDAPSourceModeResponse>({
    queryKey: ["ldap-source-mode"],
    queryFn: () => apiFetch<LDAPSourceModeResponse>("/api/v1/ldap/source-mode"),
    staleTime: 10000,
    retry: false,
  })

  const sourceMode = modeData?.source_mode ?? "internal"

  // Mode switch confirm state (MODE-4)
  const [switchTarget, setSwitchTarget] = React.useState<"internal" | "external" | null>(null)
  const [switchConfirm, setSwitchConfirm] = React.useState("")

  const switchModeMutation = useMutation({
    mutationFn: (mode: "internal" | "external") =>
      apiFetch<{ source_mode: string; changed: boolean; slapd_was_running?: boolean }>(
        "/api/v1/ldap/source-mode",
        { method: "PUT", body: JSON.stringify({ mode, confirm: mode }) }
      ),
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ["ldap-source-mode"] })
      qc.invalidateQueries({ queryKey: ["ldap-internal-status"] })
      qc.invalidateQueries({ queryKey: ["ldap-config"] })
      setSwitchTarget(null)
      setSwitchConfirm("")
      if (res.slapd_was_running) {
        toast({
          title: "Mode switched — slapd is still running",
          description: "The internal LDAP server is still active. Use Disable to stop it if needed.",
        })
      } else {
        toast({ title: `Switched to ${res.source_mode} mode` })
      }
    },
    onError: (err) => toast({ variant: "destructive", title: "Mode switch failed", description: String(err) }),
  })

  function handleModeSwitch(target: "internal" | "external") {
    if (target === sourceMode) return
    setSwitchTarget(target)
    setSwitchConfirm("")
  }

  return (
    <Section title="LDAP config" icon={Settings} id="ldap-config">
      <div className="rounded-md border border-border bg-card">
        {/* Mode toggle header (MODE-1) */}
        <div className="px-4 py-3 border-b border-border">
          <p className="text-[11px] text-muted-foreground mb-2">Directory source</p>
          {modeLoading ? (
            <Skeleton className="h-7 w-64" />
          ) : (
            <div className="flex gap-1 rounded-md border border-border p-0.5 w-fit bg-secondary/30">
              <button
                className={cn(
                  "px-3 py-1 text-xs rounded transition-colors",
                  sourceMode === "internal"
                    ? "bg-primary text-primary-foreground"
                    : "text-muted-foreground hover:text-foreground"
                )}
                onClick={() => handleModeSwitch("internal")}
                disabled={switchModeMutation.isPending}
              >
                <Server className="h-3 w-3 inline mr-1 mb-0.5" />
                Internal — clustr-managed
              </button>
              <button
                className={cn(
                  "px-3 py-1 text-xs rounded transition-colors",
                  sourceMode === "external"
                    ? "bg-primary text-primary-foreground"
                    : "text-muted-foreground hover:text-foreground"
                )}
                onClick={() => handleModeSwitch("external")}
                disabled={switchModeMutation.isPending}
              >
                <ExternalLink className="h-3 w-3 inline mr-1 mb-0.5" />
                External — existing directory
              </button>
            </div>
          )}
        </div>

        {/* Typed-confirm for mode switch (MODE-4) */}
        {switchTarget !== null && (
          <div className="px-4 py-3 border-b border-border bg-amber-500/5 space-y-2">
            <p className="text-xs font-medium text-amber-400">
              Switch to {switchTarget === "internal" ? "Internal — clustr-managed" : "External — existing directory"}?
            </p>
            <p className="text-[11px] text-muted-foreground">
              Type <code className="font-mono">{switchTarget}</code> to confirm.
            </p>
            <div className="flex gap-2">
              <Input
                className="text-xs h-7 font-mono w-40"
                placeholder={switchTarget}
                value={switchConfirm}
                onChange={(e) => setSwitchConfirm(e.target.value)}
                autoFocus
              />
              <Button
                size="sm"
                className="h-7 text-xs"
                disabled={switchConfirm !== switchTarget || switchModeMutation.isPending}
                onClick={() => switchModeMutation.mutate(switchTarget)}
              >
                {switchModeMutation.isPending ? <Loader2 className="h-3 w-3 animate-spin" /> : "Confirm"}
              </Button>
              <Button size="sm" variant="ghost" className="h-7 text-xs" onClick={() => { setSwitchTarget(null); setSwitchConfirm("") }}>
                Cancel
              </Button>
            </div>
          </div>
        )}

        {/* Mode-specific content */}
        {sourceMode === "internal" ? (
          <LDAPInternalPanel qc={qc} />
        ) : (
          <LDAPExternalPanel qc={qc} />
        )}
      </div>
    </Section>
  )
}

// ─── Internal LDAP panel (ENABLE-1..6, DISABLE-1..3) ─────────────────────────

function LDAPInternalPanel({ qc }: { qc: ReturnType<typeof useQueryClient> }) {
  // Poll status when provisioning is in progress.
  const [polling, setPolling] = React.useState(false)

  const { data: status, refetch: refetchStatus } = useQuery<LDAPInternalStatusResponse>({
    queryKey: ["ldap-internal-status"],
    queryFn: () => apiFetch<LDAPInternalStatusResponse>("/api/v1/ldap/internal/status"),
    staleTime: polling ? 0 : 10000,
    refetchInterval: polling ? 2000 : false,
    retry: false,
  })

  // Start/stop polling based on status.
  React.useEffect(() => {
    if (status?.status === "provisioning") {
      setPolling(true)
    } else {
      setPolling(false)
    }
  }, [status?.status])

  // Enable form state
  const [baseDN, setBaseDN] = React.useState("dc=cluster,dc=local")
  const [adminPwd, setAdminPwd] = React.useState("")
  const [enableError, setEnableError] = React.useState<LDAPInternalEnableError | null>(null)

  const enableMutation = useMutation({
    mutationFn: () =>
      apiFetch<{ status: string; polling_url: string }>(
        "/api/v1/ldap/internal/enable",
        { method: "POST", body: JSON.stringify({ base_dn: baseDN, admin_password: adminPwd || undefined }) }
      ),
    onSuccess: () => {
      setEnableError(null)
      setPolling(true)
      refetchStatus()
      toast({ title: "Provisioning slapd…" })
    },
    onError: async (err) => {
      // Try to parse structured error from response body.
      try {
        const body = JSON.parse(String(err).replace(/^Error: /, ""))
        if (body?.code) {
          setEnableError(body as LDAPInternalEnableError)
          return
        }
      } catch { /* fallthrough */ }
      setEnableError({
        code: "enable_failed",
        message: String(err),
        remediation: "Check the server logs for details.",
        diag_cmd: "journalctl -u clustr-serverd --since '5 minutes ago'",
      })
    },
  })

  // Disable state
  const [disableOpen, setDisableOpen] = React.useState(false)
  const [destroyConfirm, setDestroyConfirm] = React.useState("")

  // Default disable: wipe data (no body needed — server default).
  const disableMutation = useMutation({
    mutationFn: () => apiFetch("/api/v1/ldap/internal/disable", { method: "POST" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ldap-internal-status"] })
      qc.invalidateQueries({ queryKey: ["ldap-config"] })
      setDisableOpen(false)
      toast({ title: "LDAP stopped and data wiped" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Disable failed", description: String(err) }),
  })

  // Preserve-data path: opt-in, requires typed confirm.
  const preserveMutation = useMutation({
    mutationFn: () => apiFetch("/api/v1/ldap/internal/disable", { method: "POST", body: JSON.stringify({ preserve_data: true }) }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ldap-internal-status"] })
      qc.invalidateQueries({ queryKey: ["ldap-config"] })
      setDisableOpen(false)
      setDestroyConfirm("")
      toast({ title: "LDAP stopped (data preserved)" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Stop failed", description: String(err) }),
  })

  // Admin password recovery (ENABLE-6 — show once)
  const [showPwd, setShowPwd] = React.useState(false)
  const [adminPwdValue, setAdminPwdValue] = React.useState<string | null>(null)
  const [pwdCopied, setPwdCopied] = React.useState(false)

  async function handleShowAdminPwd() {
    try {
      const res = await apiFetch<LDAPAdminPasswordResponse>("/api/v1/ldap/internal/admin-password")
      setAdminPwdValue(res.admin_password)
      setShowPwd(true)
    } catch (err) {
      toast({ variant: "destructive", title: "Cannot retrieve admin password", description: String(err) })
    }
  }

  const isReady = status?.status === "ready"
  const isProvisioning = status?.status === "provisioning"
  const isError = status?.status === "error"
  const isDisabled = !status?.enabled || status?.status === "disabled"

  return (
    <div className="divide-y divide-border">
      {/* Status panel (ENABLE-6) — shown when enabled */}
      {!isDisabled && (
        <div className="px-4 py-3 space-y-2">
          <div className="flex items-center justify-between">
            <p className="text-[11px] font-medium text-muted-foreground">Internal slapd status</p>
            {isReady && !disableOpen && (
              <button
                className="text-[11px] text-muted-foreground hover:text-destructive underline underline-offset-2"
                onClick={() => setDisableOpen(true)}
              >
                Disable
              </button>
            )}
          </div>

          {/* Provisioning spinner (ENABLE-4) */}
          {isProvisioning && (
            <div className="flex items-center gap-2 text-amber-400 text-xs">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              <span>Provisioning slapd… {status?.status_detail && `— ${status.status_detail}`}</span>
            </div>
          )}

          {/* Ready panel (green) */}
          {isReady && (
            <div className="rounded border border-green-500/30 bg-green-500/5 px-3 py-2 space-y-1.5">
              <div className="flex items-center gap-2">
                <CheckCircle2 className="h-3.5 w-3.5 text-green-400" />
                <span className="text-xs font-medium text-green-400">slapd running</span>
              </div>
              <div className="grid grid-cols-2 gap-x-4 gap-y-0.5 text-[11px]">
                <span className="text-muted-foreground">Base DN</span>
                <code className="font-mono">{status?.base_dn || "—"}</code>
                <span className="text-muted-foreground">Port</span>
                <span>{status?.port ?? 636} (LDAPS)</span>
                <span className="text-muted-foreground">Uptime</span>
                <span>{formatUptime(status?.uptime_sec ?? 0)}</span>
                <span className="text-muted-foreground">Running</span>
                <span className={status?.running ? "text-green-400" : "text-amber-400"}>
                  {status?.running ? "yes" : "no (may be restarting)"}
                </span>
              </div>

              {/* Admin password recovery (ENABLE-6) */}
              <div className="pt-1">
                {!showPwd ? (
                  <button
                    className="text-[11px] text-muted-foreground hover:text-foreground underline underline-offset-2"
                    onClick={handleShowAdminPwd}
                  >
                    Show admin password (one-time recovery)
                  </button>
                ) : adminPwdValue ? (
                  <div className="flex items-center gap-2 mt-1">
                    <code className="font-mono text-[11px] flex-1 break-all">{adminPwdValue}</code>
                    <Button size="sm" variant="ghost" className="h-6 px-2 text-[11px]"
                      onClick={() => { navigator.clipboard.writeText(adminPwdValue); setPwdCopied(true); setTimeout(() => setPwdCopied(false), 2000) }}>
                      {pwdCopied ? "Copied!" : "Copy"}
                    </Button>
                    <Button size="sm" variant="ghost" className="h-6 w-6 p-0"
                      onClick={() => { setShowPwd(false); setAdminPwdValue(null) }}>
                      <X className="h-3 w-3" />
                    </Button>
                  </div>
                ) : null}
              </div>
            </div>
          )}

          {/* Error panel */}
          {isError && (
            <div className="rounded border border-destructive/30 bg-destructive/5 px-3 py-2 space-y-1">
              <div className="flex items-center gap-2">
                <AlertTriangle className="h-3.5 w-3.5 text-destructive" />
                <span className="text-xs font-medium text-destructive">slapd error</span>
              </div>
              <p className="text-[11px] text-muted-foreground">{status?.status_detail}</p>
              <button
                className="text-[11px] text-primary underline underline-offset-2"
                onClick={() => enableMutation.mutate()}
                disabled={enableMutation.isPending}
              >
                Re-enable to retry provisioning
              </button>
            </div>
          )}

          {/* Disable panel (DISABLE-2) — wipe is the default, preserve is opt-in */}
          {disableOpen && (
            <div className="rounded border border-border bg-secondary/5 px-3 py-2 space-y-2 mt-1">
              <p className="text-xs font-medium">Disable internal LDAP</p>
              {/* Primary: Stop + wipe (default behavior) */}
              <div className="flex gap-2">
                <Button
                  size="sm"
                  variant="destructive"
                  className="text-xs h-7"
                  disabled={disableMutation.isPending}
                  onClick={() => disableMutation.mutate()}
                >
                  {disableMutation.isPending ? <Loader2 className="h-3 w-3 animate-spin" /> : "Stop + wipe data"}
                </Button>
                <Button size="sm" variant="ghost" className="text-xs h-7" onClick={() => { setDisableOpen(false); setDestroyConfirm("") }}>
                  Cancel
                </Button>
              </div>
              {/* Advanced: Stop + preserve data — requires typed confirm */}
              <div className="pt-1 border-t border-border/50">
                <p className="text-[11px] text-muted-foreground mb-1.5">
                  Stop + preserve data (advanced) — type <code className="font-mono">preserve</code> to confirm:
                </p>
                <div className="flex gap-2">
                  <Input
                    className="text-xs h-7 font-mono w-28"
                    placeholder="preserve"
                    value={destroyConfirm}
                    onChange={(e) => setDestroyConfirm(e.target.value)}
                  />
                  <Button
                    size="sm"
                    variant="outline"
                    className="text-xs h-7"
                    disabled={destroyConfirm !== "preserve" || preserveMutation.isPending}
                    onClick={() => preserveMutation.mutate()}
                  >
                    {preserveMutation.isPending ? <Loader2 className="h-3 w-3 animate-spin" /> : "Stop (keep data)"}
                  </Button>
                </div>
              </div>
            </div>
          )}
        </div>
      )}

      {/* Enable form (MODE-2) — shown when not enabled */}
      {isDisabled && (
        <div className="px-4 py-3 space-y-3">
          <p className="text-xs font-medium text-muted-foreground">Enable built-in slapd</p>
          <p className="text-[11px] text-muted-foreground">
            clustr will install openldap-servers, generate TLS certificates, and start slapd on port 636.
          </p>

          {/* Structured error (ENABLE-4) */}
          {enableError && (
            <div className="rounded border border-destructive/30 bg-destructive/5 px-3 py-2 space-y-1.5">
              <div className="flex items-start gap-2">
                <AlertTriangle className="h-3.5 w-3.5 text-destructive mt-0.5 shrink-0" />
                <div className="space-y-1 min-w-0">
                  <p className="text-xs font-medium text-destructive">{enableError.message}</p>
                  <p className="text-[11px] text-muted-foreground">{enableError.remediation}</p>
                  {enableError.diag_cmd && (
                    <div className="flex items-center gap-2">
                      <code className="font-mono text-[11px] text-muted-foreground flex-1 truncate">{enableError.diag_cmd}</code>
                      <Button size="sm" variant="ghost" className="h-5 px-1.5 text-[10px] shrink-0"
                        onClick={() => navigator.clipboard.writeText(enableError!.diag_cmd!)}>
                        Copy
                      </Button>
                    </div>
                  )}
                </div>
              </div>
            </div>
          )}

          <div className="space-y-2">
            <div>
              <label className="text-[11px] text-muted-foreground">Base DN</label>
              <Input
                className="text-xs h-7 font-mono mt-0.5"
                value={baseDN}
                onChange={(e) => setBaseDN(e.target.value)}
                placeholder="dc=cluster,dc=local"
              />
            </div>
            <div>
              <label className="text-[11px] text-muted-foreground">Admin password (leave blank to auto-generate)</label>
              <Input
                className="text-xs h-7 font-mono mt-0.5"
                type="password"
                value={adminPwd}
                onChange={(e) => setAdminPwd(e.target.value)}
                placeholder="auto-generate"
              />
            </div>
          </div>

          <Button
            size="sm"
            className="w-full text-xs"
            disabled={!baseDN || enableMutation.isPending || isProvisioning}
            onClick={() => enableMutation.mutate()}
          >
            {enableMutation.isPending || isProvisioning ? (
              <>
                <Loader2 className="h-3 w-3 mr-1.5 animate-spin" />
                Provisioning slapd…
              </>
            ) : (
              "Enable built-in LDAP"
            )}
          </Button>
        </div>
      )}
    </div>
  )
}

// ─── External LDAP panel (Sprint 7+8 form — MODE-3) ──────────────────────────

function LDAPExternalPanel({ qc }: { qc: ReturnType<typeof useQueryClient> }) {
  const { data, isLoading } = useQuery<LDAPConfigResponse>({
    queryKey: ["ldap-config"],
    queryFn: () => apiFetch<LDAPConfigResponse>("/api/v1/ldap/config"),
    staleTime: 15000,
    retry: false,
  })

  const [testResult, setTestResult] = React.useState<LDAPTestResponse | null>(null)
  const [testing, setTesting] = React.useState(false)

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
      setWbPass("")
    } catch (err) {
      setWbResult(`Error: ${String(err)}`)
    } finally {
      setWbSaving(false)
    }
  }

  return (
    <div>
      {/* Test connection */}
      <div className="px-4 py-2 border-b border-border flex items-center justify-between">
        <span className="text-[11px] text-muted-foreground">External LDAP / Active Directory</span>
        <Button size="sm" variant="outline" className="h-6 text-[11px]" onClick={handleTest} disabled={testing}>
          {testing ? "Testing…" : "Test connection"}
        </Button>
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

          {/* Write-bind config (WRITE-CFG-3) */}
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

          {writeBindOpen && (
            <div className="mt-2 rounded border border-border bg-secondary/5 px-3 py-2 space-y-2">
              <p className="text-[11px] font-medium">Write bind credentials</p>
              <p className="text-[11px] text-muted-foreground">
                Required to create/edit/delete users and groups in the external directory from clustr.
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
              <p className="text-[11px] text-amber-400 font-medium">External LDAP not configured</p>
              <p className="text-[11px] text-muted-foreground mt-0.5">
                Configure the connection to your existing directory server using the fields above.
              </p>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function formatUptime(sec: number): string {
  if (sec <= 0) return "—"
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}

function LDAPConfigRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start gap-4">
      <span className="text-[11px] text-muted-foreground w-32 shrink-0 pt-0.5">{label}</span>
      <div className="flex items-center gap-1 flex-wrap">{children}</div>
    </div>
  )
}
