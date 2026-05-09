/**
 * InterfaceList.tsx — Multi-NIC editor for node Add/Edit forms (Sprint 44 MULTI-NIC-EDITOR)
 *
 * Renders a stacked list of typed per-interface cards:
 *   ethernet: name + MAC + IP + VLAN + default-gateway flag
 *   fabric:   name + IB GUID + IP + port
 *   ipmi:     name + IP + channel + user + password
 *
 * Validates per-kind fields on submit via validate(). Consumers call validate() before
 * reading the value to get errors keyed by "index.field".
 *
 * Wire shape mirrors pkg/api/types.go InterfaceConfig / IBInterfaceConfig.
 */

import * as React from "react"
import { Plus, Trash2, ChevronDown, ChevronUp } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"

// ─── Types ────────────────────────────────────────────────────────────────────

export type EthernetRow = {
  kind: "ethernet"
  name: string
  mac: string
  ip?: string
  vlan?: string
  is_default_gateway?: boolean
}

export type FabricRow = {
  kind: "fabric"
  name: string
  guid: string
  ip?: string
  port?: string
}

export type IpmiRow = {
  kind: "ipmi"
  name: string
  ip: string
  channel: string
  user: string
  pass: string
}

export type InterfaceRow = EthernetRow | FabricRow | IpmiRow

export type InterfaceErrors = Record<string, string>

export interface InterfaceListProps {
  value: InterfaceRow[]
  onChange: (next: InterfaceRow[]) => void
  errors?: InterfaceErrors
}

// ─── Validation ───────────────────────────────────────────────────────────────

const macRe = /^([0-9a-f]{2}:){5}[0-9a-f]{2}$/i
const guidRe = /^([0-9a-f]{4}:){3}[0-9a-f]{4}$/i
const ipv4Re = /^(\d{1,3}\.){3}\d{1,3}(\/\d+)?$/

export function validateInterfaces(rows: InterfaceRow[]): InterfaceErrors {
  const errs: InterfaceErrors = {}

  rows.forEach((row, i) => {
    if (!row.name.trim()) {
      errs[`${i}.name`] = "Interface name required"
    }

    if (row.kind === "ethernet") {
      const mac = row.mac.trim()
      if (!mac) {
        errs[`${i}.mac`] = "MAC address required"
      } else if (!macRe.test(mac)) {
        errs[`${i}.mac`] = "Invalid MAC (e.g. bc:24:11:36:e9:2f)"
      }
      if (row.ip && !ipv4Re.test(row.ip.trim())) {
        errs[`${i}.ip`] = "Invalid IPv4 address or CIDR"
      }
      if (row.vlan !== undefined && row.vlan !== "") {
        const vlanNum = parseInt(row.vlan, 10)
        if (isNaN(vlanNum) || vlanNum < 1 || vlanNum > 4094) {
          errs[`${i}.vlan`] = "VLAN must be 1–4094"
        }
      }
    } else if (row.kind === "fabric") {
      const guid = row.guid.trim()
      if (!guid) {
        errs[`${i}.guid`] = "IB GUID required"
      } else if (!guidRe.test(guid)) {
        errs[`${i}.guid`] = "Invalid GUID (e.g. 0001:0002:0003:0004)"
      }
      if (row.ip && !ipv4Re.test(row.ip.trim())) {
        errs[`${i}.ip`] = "Invalid IPv4 address or CIDR"
      }
      if (row.port !== undefined && row.port !== "") {
        const portNum = parseInt(row.port, 10)
        if (isNaN(portNum) || portNum < 0 || portNum > 255) {
          errs[`${i}.port`] = "Port must be 0–255"
        }
      }
    } else if (row.kind === "ipmi") {
      const ipmiIp = row.ip.trim()
      if (!ipmiIp) {
        errs[`${i}.ip`] = "IPMI IP required"
      } else if (!ipv4Re.test(ipmiIp)) {
        errs[`${i}.ip`] = "Invalid IPv4 address or CIDR"
      }
      const channel = parseInt(row.channel, 10)
      if (isNaN(channel) || channel < 0 || channel > 15) {
        errs[`${i}.channel`] = "Channel must be 0–15"
      }
      if (!row.user.trim()) {
        errs[`${i}.user`] = "IPMI user required"
      }
    }
  })

  return errs
}

// ─── Kind defaults ────────────────────────────────────────────────────────────

function defaultRow(kind: InterfaceRow["kind"]): InterfaceRow {
  if (kind === "ethernet") {
    return { kind: "ethernet", name: "eth0", mac: "", ip: "", vlan: "", is_default_gateway: false }
  }
  if (kind === "fabric") {
    return { kind: "fabric", name: "ib0", guid: "", ip: "", port: "" }
  }
  return { kind: "ipmi", name: "ipmi0", ip: "", channel: "1", user: "admin", pass: "" }
}

const KIND_LABELS: Record<InterfaceRow["kind"], string> = {
  ethernet: "Ethernet",
  fabric: "Fabric (IB)",
  ipmi: "IPMI",
}

const KIND_COLORS: Record<InterfaceRow["kind"], string> = {
  ethernet: "bg-blue-50 border-blue-200 dark:bg-blue-950/20 dark:border-blue-800",
  fabric: "bg-purple-50 border-purple-200 dark:bg-purple-950/20 dark:border-purple-800",
  ipmi: "bg-orange-50 border-orange-200 dark:bg-orange-950/20 dark:border-orange-800",
}

const KIND_BADGE: Record<InterfaceRow["kind"], string> = {
  ethernet: "bg-blue-100 text-blue-700 dark:bg-blue-900 dark:text-blue-300",
  fabric: "bg-purple-100 text-purple-700 dark:bg-purple-900 dark:text-purple-300",
  ipmi: "bg-orange-100 text-orange-700 dark:bg-orange-900 dark:text-orange-300",
}

// ─── Field helper ─────────────────────────────────────────────────────────────

function F({
  label,
  error,
  children,
  half,
}: {
  label: string
  error?: string
  children: React.ReactNode
  half?: boolean
}) {
  return (
    <div className={cn("space-y-0.5", half ? "col-span-1" : "col-span-2")}>
      <label className="text-xs text-muted-foreground">{label}</label>
      {children}
      {error && <p className="text-xs text-destructive">{error}</p>}
    </div>
  )
}

// ─── Per-kind card bodies ─────────────────────────────────────────────────────

function EthernetCard({
  row,
  idx,
  errors,
  onChange,
}: {
  row: EthernetRow
  idx: number
  errors: InterfaceErrors
  onChange: (patch: Partial<EthernetRow>) => void
}) {
  return (
    <div className="grid grid-cols-2 gap-2 mt-2">
      <F label="Name" error={errors[`${idx}.name`]}>
        <Input
          value={row.name}
          onChange={(e) => onChange({ name: e.target.value })}
          className={cn("text-xs h-7 font-mono", errors[`${idx}.name`] && "border-destructive")}
          data-testid={`iface-${idx}-name`}
        />
      </F>
      <F label="MAC *" error={errors[`${idx}.mac`]}>
        <Input
          value={row.mac}
          onChange={(e) => onChange({ mac: e.target.value })}
          placeholder="bc:24:11:36:e9:2f"
          className={cn("text-xs h-7 font-mono", errors[`${idx}.mac`] && "border-destructive")}
          data-testid={`iface-${idx}-mac`}
        />
      </F>
      <F label="IP / CIDR (optional)" error={errors[`${idx}.ip`]}>
        <Input
          value={row.ip ?? ""}
          onChange={(e) => onChange({ ip: e.target.value })}
          placeholder="10.0.0.1/24"
          className={cn("text-xs h-7 font-mono", errors[`${idx}.ip`] && "border-destructive")}
          data-testid={`iface-${idx}-ip`}
        />
      </F>
      <F label="VLAN (optional)" error={errors[`${idx}.vlan`]} half>
        <Input
          value={row.vlan ?? ""}
          onChange={(e) => onChange({ vlan: e.target.value })}
          placeholder="100"
          className={cn("text-xs h-7", errors[`${idx}.vlan`] && "border-destructive")}
          data-testid={`iface-${idx}-vlan`}
        />
      </F>
      <div className="col-span-2">
        <label className="flex items-center gap-1.5 text-xs cursor-pointer select-none">
          <input
            type="checkbox"
            checked={row.is_default_gateway ?? false}
            onChange={(e) => onChange({ is_default_gateway: e.target.checked })}
            className="rounded"
            data-testid={`iface-${idx}-default-gw`}
          />
          Default gateway
        </label>
      </div>
    </div>
  )
}

function FabricCard({
  row,
  idx,
  errors,
  onChange,
}: {
  row: FabricRow
  idx: number
  errors: InterfaceErrors
  onChange: (patch: Partial<FabricRow>) => void
}) {
  return (
    <div className="grid grid-cols-2 gap-2 mt-2">
      <F label="Name" error={errors[`${idx}.name`]}>
        <Input
          value={row.name}
          onChange={(e) => onChange({ name: e.target.value })}
          className={cn("text-xs h-7 font-mono", errors[`${idx}.name`] && "border-destructive")}
          data-testid={`iface-${idx}-name`}
        />
      </F>
      <F label="IB GUID *" error={errors[`${idx}.guid`]}>
        <Input
          value={row.guid}
          onChange={(e) => onChange({ guid: e.target.value })}
          placeholder="0001:0002:0003:0004"
          className={cn("text-xs h-7 font-mono", errors[`${idx}.guid`] && "border-destructive")}
          data-testid={`iface-${idx}-guid`}
        />
      </F>
      <F label="IP / CIDR (optional)" error={errors[`${idx}.ip`]}>
        <Input
          value={row.ip ?? ""}
          onChange={(e) => onChange({ ip: e.target.value })}
          placeholder="192.168.40.1/24"
          className={cn("text-xs h-7 font-mono", errors[`${idx}.ip`] && "border-destructive")}
          data-testid={`iface-${idx}-ip`}
        />
      </F>
      <F label="Port (optional)" error={errors[`${idx}.port`]} half>
        <Input
          value={row.port ?? ""}
          onChange={(e) => onChange({ port: e.target.value })}
          placeholder="1"
          className={cn("text-xs h-7", errors[`${idx}.port`] && "border-destructive")}
          data-testid={`iface-${idx}-port`}
        />
      </F>
    </div>
  )
}

function IpmiCard({
  row,
  idx,
  errors,
  onChange,
}: {
  row: IpmiRow
  idx: number
  errors: InterfaceErrors
  onChange: (patch: Partial<IpmiRow>) => void
}) {
  return (
    <div className="grid grid-cols-2 gap-2 mt-2">
      <F label="Name" error={errors[`${idx}.name`]}>
        <Input
          value={row.name}
          onChange={(e) => onChange({ name: e.target.value })}
          className={cn("text-xs h-7 font-mono", errors[`${idx}.name`] && "border-destructive")}
          data-testid={`iface-${idx}-name`}
        />
      </F>
      <F label="IP *" error={errors[`${idx}.ip`]}>
        <Input
          value={row.ip}
          onChange={(e) => onChange({ ip: e.target.value })}
          placeholder="10.0.1.50"
          className={cn("text-xs h-7 font-mono", errors[`${idx}.ip`] && "border-destructive")}
          data-testid={`iface-${idx}-ip`}
        />
      </F>
      <F label="Channel (0–15)" error={errors[`${idx}.channel`]} half>
        <Input
          value={row.channel}
          onChange={(e) => onChange({ channel: e.target.value })}
          placeholder="1"
          className={cn("text-xs h-7", errors[`${idx}.channel`] && "border-destructive")}
          data-testid={`iface-${idx}-channel`}
        />
      </F>
      <F label="User" error={errors[`${idx}.user`]} half>
        <Input
          value={row.user}
          onChange={(e) => onChange({ user: e.target.value })}
          placeholder="admin"
          className={cn("text-xs h-7", errors[`${idx}.user`] && "border-destructive")}
          data-testid={`iface-${idx}-user`}
        />
      </F>
      <F label="Password" error={errors[`${idx}.pass`]}>
        <Input
          type="password"
          value={row.pass}
          onChange={(e) => onChange({ pass: e.target.value })}
          placeholder="••••••••"
          className={cn("text-xs h-7", errors[`${idx}.pass`] && "border-destructive")}
          data-testid={`iface-${idx}-pass`}
        />
      </F>
    </div>
  )
}

// ─── InterfaceCard ────────────────────────────────────────────────────────────

function InterfaceCard({
  row,
  idx,
  errors,
  onChange,
  onRemove,
}: {
  row: InterfaceRow
  idx: number
  errors: InterfaceErrors
  onChange: (next: InterfaceRow) => void
  onRemove: () => void
}) {
  const [open, setOpen] = React.useState(true)

  function patchEth(patch: Partial<EthernetRow>) {
    onChange({ ...(row as EthernetRow), ...patch })
  }
  function patchFab(patch: Partial<FabricRow>) {
    onChange({ ...(row as FabricRow), ...patch })
  }
  function patchIpmi(patch: Partial<IpmiRow>) {
    onChange({ ...(row as IpmiRow), ...patch })
  }

  const hasError = Object.keys(errors).some((k) => k.startsWith(`${idx}.`))

  return (
    <div className={cn("rounded-md border px-3 py-2 space-y-1", KIND_COLORS[row.kind], hasError && "border-destructive")}>
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <span className={cn("text-xs font-medium rounded px-1.5 py-0.5", KIND_BADGE[row.kind])}>
            {KIND_LABELS[row.kind]}
          </span>
          <span className="text-xs font-mono text-muted-foreground">{row.name || `iface-${idx}`}</span>
        </div>
        <div className="flex items-center gap-1">
          <button
            type="button"
            onClick={() => setOpen((o) => !o)}
            className="text-muted-foreground hover:text-foreground p-0.5"
            aria-label={open ? "Collapse" : "Expand"}
          >
            {open ? <ChevronUp className="h-3.5 w-3.5" /> : <ChevronDown className="h-3.5 w-3.5" />}
          </button>
          <button
            type="button"
            onClick={onRemove}
            className="text-muted-foreground hover:text-destructive p-0.5"
            aria-label="Remove interface"
            data-testid={`iface-${idx}-remove`}
          >
            <Trash2 className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>

      {open && (
        <>
          {row.kind === "ethernet" && (
            <EthernetCard row={row} idx={idx} errors={errors} onChange={patchEth} />
          )}
          {row.kind === "fabric" && (
            <FabricCard row={row} idx={idx} errors={errors} onChange={patchFab} />
          )}
          {row.kind === "ipmi" && (
            <IpmiCard row={row} idx={idx} errors={errors} onChange={patchIpmi} />
          )}
        </>
      )}
    </div>
  )
}

// ─── InterfaceList ────────────────────────────────────────────────────────────

export function InterfaceList({ value, onChange, errors = {} }: InterfaceListProps) {
  function addInterface(kind: InterfaceRow["kind"]) {
    onChange([...value, defaultRow(kind)])
  }

  function updateInterface(idx: number, next: InterfaceRow) {
    const copy = [...value]
    copy[idx] = next
    onChange(copy)
  }

  function removeInterface(idx: number) {
    onChange(value.filter((_, i) => i !== idx))
  }

  return (
    <div className="space-y-2" data-testid="interface-list">
      {value.length === 0 && (
        <p className="text-xs text-muted-foreground italic">No interfaces defined. Add one below.</p>
      )}

      {value.map((row, idx) => (
        <InterfaceCard
          key={idx}
          row={row}
          idx={idx}
          errors={errors}
          onChange={(next) => updateInterface(idx, next)}
          onRemove={() => removeInterface(idx)}
        />
      ))}

      <div className="flex gap-2 flex-wrap">
        {(["ethernet", "fabric", "ipmi"] as const).map((kind) => (
          <Button
            key={kind}
            type="button"
            variant="outline"
            size="sm"
            className="h-7 text-xs gap-1"
            onClick={() => addInterface(kind)}
            data-testid={`add-iface-${kind}`}
          >
            <Plus className="h-3 w-3" />
            {KIND_LABELS[kind]}
          </Button>
        ))}
      </div>
    </div>
  )
}
