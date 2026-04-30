export type NodeState =
  | "registered"
  | "configured"
  | "deploying"
  | "deployed"
  | "reimage_pending"
  | "failed"
  | "deployed_preboot"
  | "deployed_verified"
  | "deploy_verify_timeout"

export interface NodeConfig {
  id: string
  hostname: string
  hostname_auto: boolean
  fqdn: string
  primary_mac: string
  interfaces: InterfaceConfig[]
  ssh_keys: string[]
  kernel_args: string
  tags: string[]
  groups: string[]
  custom_vars: Record<string, string>
  base_image_id: string
  group_id?: string
  reimage_pending: boolean
  last_deploy_failed_at?: string
  deploy_completed_preboot_at?: string
  deploy_verified_booted_at?: string
  deploy_verify_timeout_at?: string
  last_seen_at?: string
  detected_firmware?: string
  /** Provider identifies the node's hardware/power backend: "ipmi", "proxmox", or "" (unset). */
  provider?: string
  created_at: string
  updated_at: string
}

/** Valid values for the provider field. Mirrors api.NodeProviderValues on the server. */
export const NODE_PROVIDERS = [
  { value: "",        label: "Not set" },
  { value: "ipmi",    label: "IPMI" },
  { value: "proxmox", label: "Proxmox" },
] as const

export interface InterfaceConfig {
  name: string
  mac: string
  ip_address?: string
  gateway?: string
}

export interface ListNodesResponse {
  nodes: NodeConfig[]
  total: number
  page?: number
  per_page?: number
  next_cursor?: number
}

export function nodeState(n: NodeConfig): NodeState {
  if (n.reimage_pending) return "reimage_pending"
  if (n.last_deploy_failed_at && !n.deploy_completed_preboot_at) return "failed"
  if (n.deploy_verified_booted_at) return "deployed_verified"
  if (n.deploy_verify_timeout_at) return "deploy_verify_timeout"
  if (n.deploy_completed_preboot_at) return "deployed_preboot"
  if (n.base_image_id) return "configured"
  return "registered"
}

// ── Images ───────────────────────────────────────────────────────────────────

export type ImageStatus = "building" | "ready" | "error" | "archived" | "interrupted"

export interface BaseImage {
  id: string
  name: string
  version: string
  os: string
  arch: string
  status: ImageStatus
  format: string
  firmware: string
  size_bytes: number
  checksum: string
  tags: string[]
  source_url?: string
  notes?: string
  error_message?: string
  build_method?: string
  built_for_roles?: string[]
  created_at: string
  finalized_at?: string
}

export interface ListImagesResponse {
  images: BaseImage[]
  total: number
  page?: number
  per_page?: number
  next_cursor?: number
}

export type ImageEventKind = "image.created" | "image.updated" | "image.deleted" | "image.finalized"

export interface ImageEvent {
  kind: ImageEventKind
  image?: BaseImage
  id: string
}

// ── Bundles ───────────────────────────────────────────────────────────────────

export interface Bundle {
  name: string           // e.g. "slurm-v24.11.4-clustr5"
  slurm_version: string  // e.g. "24.11.4"
  bundle_version: string // e.g. "v24.11.4-clustr5"
  sha256: string         // tarball SHA256 hex
  kind: string           // "builtin"
  source: string         // "embedded"
}

export interface ListBundlesResponse {
  bundles: Bundle[]
  total: number
}

// ── GPG Keys ─────────────────────────────────────────────────────────────────

export interface GPGKey {
  fingerprint: string
  owner: string
  armored_key?: string
  source: "embedded" | "user"
  created_at: string
}

export interface ListGPGKeysResponse {
  keys: GPGKey[]
}

// ── Audit / Activity ─────────────────────────────────────────────────────────

export interface AuditRecord {
  id: string
  actor_id: string
  actor_label: string
  action: string
  resource_type: string
  resource_id: string
  old_value?: unknown
  new_value?: unknown
  ip_addr?: string
  created_at: string
}

export interface AuditQueryResponse {
  records: AuditRecord[]
  total: number
  limit: number
  offset: number
}

// ── API Keys ─────────────────────────────────────────────────────────────────

export interface APIKey {
  id: string
  scope: string
  node_id?: string
  label?: string
  created_by?: string
  hash_prefix: string
  created_at: string
  expires_at?: string
  last_used_at?: string
}

export interface ListAPIKeysResponse {
  api_keys: APIKey[]
}

export interface CreateAPIKeyResponse {
  key: string
  api_key: APIKey
}

// ── Reimage ──────────────────────────────────────────────────────────────────

export type ReimageStatus =
  | "pending"
  | "triggered"
  | "in_progress"
  | "complete"
  | "failed"
  | "canceled"

export interface ReimageRequest {
  id: string
  node_id: string
  image_id: string
  status: ReimageStatus
  scheduled_at?: string
  error_message?: string
  requested_by?: string
  dry_run?: boolean
  created_at: string
  triggered_at?: string
  completed_at?: string
}

// ── Server health / config ────────────────────────────────────────────────────

export interface HealthResponse {
  status: string
  version: string
  commit_sha: string
  build_time: string
  flip_back_failures?: number
}

// ── Node Groups ───────────────────────────────────────────────────────────────

export interface NodeGroup {
  id: string
  name: string
  description?: string
  role?: string
  expires_at?: string
  created_at: string
  updated_at: string
}

export interface NodeGroupWithCount extends NodeGroup {
  member_count: number
}

export interface ListNodeGroupsResponse {
  groups: NodeGroupWithCount[]
  total: number
}

export interface GroupMembersResponse {
  group: NodeGroup
  members: NodeConfig[]
}

export interface GroupReimageRequest {
  image_id: string
  concurrency?: number
  pause_on_failure_pct?: number
}

export interface GroupReimageJobStatus {
  job_id: string
  group_id: string
  image_id: string
  status: "queued" | "running" | "paused" | "completed" | "failed"
  total_nodes: number
  triggered_nodes: number
  succeeded_nodes: number
  failed_nodes: number
  concurrency: number
  pause_on_failure_pct: number
  error_message?: string
  created_at: string
  updated_at: string
}

export type GroupReimageEventKind =
  | "reimage.queued"
  | "reimage.started"
  | "reimage.imaging"
  | "reimage.verifying"
  | "reimage.done"
  | "reimage.failed"
  | "reimage.completed"

export interface GroupReimageEvent {
  kind: GroupReimageEventKind
  job_id: string
  node_id?: string
  position?: number
  progress?: number
  duration_ms?: number
  error?: string
  succeeded?: number
  failed?: number
  total?: number
}

// ── Power ─────────────────────────────────────────────────────────────────────

export interface PowerStatusResponse {
  status: "on" | "off" | "unknown"
  last_checked: string
  error?: string
}

export interface SensorReading {
  name: string
  value: string
  unit: string
  state: string
}

export interface SensorsResponse {
  node_id: string
  sensors: SensorReading[]
  last_checked: string
}

// ── Local Files ───────────────────────────────────────────────────────────────

export interface LocalFileInfo {
  path: string
  name: string
  size: number
  mtime: string
}

export interface ListLocalFilesResponse {
  files: LocalFileInfo[]
  import_dir: string
}
