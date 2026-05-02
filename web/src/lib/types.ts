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

export type InstallInstructionOpcode = "modify" | "overwrite" | "script"

export interface InstallInstruction {
  opcode: InstallInstructionOpcode
  target: string
  payload: string
}

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
  install_instructions?: InstallInstruction[]
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

// ── clustr-internal-repo packages ────────────────────────────────────────────

export interface RepoPackage {
  filename: string  // e.g. "slurm-25.11.5-clustr1.el9.x86_64.rpm"
  name: string      // e.g. "slurm"
  version: string   // e.g. "25.11.5"
  arch: string      // e.g. "x86_64"
  el_major: string  // e.g. "el9"
  path: string      // absolute path on server
}

export interface ListRepoPackagesResponse {
  packages: RepoPackage[]
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

// ── Identity (Sprint 7) ───────────────────────────────────────────────────────

export interface LocalUser {
  id: string
  username: string
  role: string
  must_change_password: boolean
  disabled: boolean
  created_at: string
  last_login_at?: string
}

export interface ListLocalUsersResponse {
  users: LocalUser[]
}

export interface LDAPUser {
  uid: string
  uid_number?: number
  gid_number?: number
  cn?: string
  sn?: string
  given_name?: string
  mail?: string
  home_directory?: string
  login_shell?: string
  ssh_public_keys?: string[]  // #94: sshPublicKey attributes
  locked?: boolean
}

// #93: structured error response for posixid validation failures
export interface PosixIDErrorResponse {
  error: string
  code: "range_exhausted" | "reserved_id" | "out_of_range" | "id_collision" | "posixid_error"
  field: "uid_number" | "gid_number"
}

// #95: PATCH /api/v1/ldap/users/{uid} request body
export interface LDAPPatchUserRequest {
  cn?: string
  sn?: string
  given_name?: string
  mail?: string
  gid_number?: number
  home_directory?: string
  login_shell?: string
  ssh_public_keys?: string[]
  add_groups?: string[]
  remove_groups?: string[]
}

export interface ListLDAPUsersResponse {
  users: LDAPUser[]
  total: number
}

export interface UserSearchResult {
  identifier: string
  display_name: string
  email?: string
  source: "ldap" | "local"
}

export interface UserSearchResponse {
  users: UserSearchResult[]
  total: number
}

export interface NodeSudoer {
  node_id: string
  user_identifier: string
  source: "ldap" | "local"
  commands: string
  assigned_at: string
  assigned_by: string
}

export interface ListNodeSudoersResponse {
  sudoers: NodeSudoer[]
  total: number
}

export interface LDAPConfigResponse {
  enabled: boolean
  status: string
  status_detail: string
  base_dn: string
  base_dn_locked: boolean
  service_bind_dn: string
  ca_fingerprint: string
  bind_password_set: boolean
  // Sprint 8 — write-bind fields (WRITE-CFG-3)
  write_bind_dn_set?: boolean
  write_capable?: boolean
  write_status?: string
  write_capable_detail?: string
  backend_dialect?: string
}

// Sprint 8 — write-bind config payload
export interface LDAPWriteBindRequest {
  write_bind_dn: string
  write_bind_password: string
}

export interface LDAPWriteBindResponse {
  write_bind_dn_set: boolean
  write_capable: boolean
  write_status: { capable: boolean; detail: string }
}

// Sprint 8 — group mode
export interface LDAPGroupModeResponse {
  cn: string
  mode: "overlay" | "direct"
}

// Sprint 8 — reset password response
export interface LDAPResetPasswordResponse {
  uid: string
  temp_password: string
  force_change: boolean
  note: string
}

// Sprint 15 #100 — create user response (may include temp_password if auto-generated)
export interface LDAPCreateUserResponse {
  uid: string
  uid_number?: number
  gid_number?: number
  cn?: string
  sn?: string
  given_name?: string
  mail?: string
  home_directory?: string
  login_shell?: string
  locked?: boolean
  temp_password?: string  // present only when server auto-generated the password
  force_change?: boolean
  note?: string
}

export interface LDAPTestResponse {
  ok: boolean
  error?: string
  user_count?: number
  base_dn?: string
}

// Sprint 9 — internal LDAP auto-deploy
export interface LDAPSourceModeResponse {
  source_mode: "internal" | "external"
}

export interface LDAPInternalStatusResponse {
  enabled: boolean
  status: string         // "disabled" | "provisioning" | "ready" | "error"
  status_detail?: string
  base_dn?: string
  running: boolean
  port: number
  uptime_sec: number
  admin_password_set: boolean
  source_mode: "internal" | "external"
  // Sprint 12 — live systemd state (sysd.Status fields)
  systemd_active?: string    // "active" | "inactive" | "failed" | "unknown" | ...
  systemd_enabled?: string   // "enabled" | "disabled" | "masked" | ...
  // ui_buttons is the recommended action set derived from live systemd state.
  // Values: "enable" | "disable" | "takeover" | "stop" | "start"
  ui_buttons?: string[]
}

export interface LDAPInternalEnableError {
  code: string           // "port_in_use" | "slapd_not_installed" | "selinux_denied" | "unit_failed_to_start" | "enable_failed"
  message: string
  remediation: string
  diag_cmd?: string
}

export interface LDAPAdminPasswordResponse {
  admin_password: string
}

export interface SpecialtyGroup {
  id: string
  name: string
  gid_number: number
  description: string
  members: string[]
  created_at: string
  updated_at: string
}

export interface ListSpecialtyGroupsResponse {
  groups: SpecialtyGroup[]
  total: number
}

export interface GroupOverlay {
  group_dn: string
  user_identifier: string
  source: string
  added_at: string
  added_by: string
}

export interface LDAPGroup {
  cn: string
  gid_number: number
  member_uids: string[]
  description?: string
}

export interface ListLDAPGroupsResponse {
  groups: LDAPGroup[]
  total: number
}

// ── Slurm (Sprint 10) ─────────────────────────────────────────────────────────

export interface SlurmStatus {
  status: string       // "not_configured" | "ready" | "disabled" | "error"
  enabled: boolean
  cluster_name?: string
  slurm_repo_url?: string
  managed_files?: string[]
}

export interface SlurmConfigFile {
  filename: string
  path: string
  content: string
  checksum: string
  file_mode: string
  owner: string
  version: number
}

export interface ListSlurmConfigsResponse {
  configs: SlurmConfigFile[]
  total: number
}

export interface SlurmValidateResponse {
  filename: string
  valid: boolean
  issues: SlurmValidationIssue[]
}

export interface SlurmValidationIssue {
  severity: string  // "error" | "warning"
  line?: number
  message: string
}

export interface SlurmRoleSummary {
  role: string
  count: number
}

export interface ListSlurmRoleSummaryResponse {
  summary: SlurmRoleSummary[]
}

export interface SlurmNodeEntry {
  node_id: string
  roles: string[]
  connected: boolean
}

export interface ListSlurmNodesResponse {
  nodes: SlurmNodeEntry[]
  total: number
}

export interface SlurmScriptSummary {
  script_type: string
  version: number
  checksum?: string
  dest_path?: string
  enabled: boolean
  has_content: boolean
}

export interface ListSlurmScriptsResponse {
  scripts: SlurmScriptSummary[]
  total: number
}

export interface SlurmScriptFile {
  script_type: string
  dest_path: string
  content: string
  checksum: string
  version: number
}

export interface SlurmBuild {
  id: string
  version: string
  arch: string
  status: string   // "building" | "completed" | "failed"
  configure_flags?: string[]
  artifact_path?: string
  artifact_checksum?: string
  artifact_size?: number
  started_at: number
  completed_at?: number
  error_message?: string
  is_active: boolean
  initiated_by?: string
}

export interface ListSlurmBuildsResponse {
  builds: SlurmBuild[]
  total: number
  active_build_id: string
}

export interface SlurmBuildLogEvent {
  line?: string
  build_id: string
}

export interface SlurmUpgradeValidation {
  valid: boolean
  warnings?: string[]
  errors?: string[]
  upgrade_plan?: SlurmUpgradePlan
  from_version?: string
  to_version?: string
  job_count: number
}

export interface SlurmUpgradePlan {
  dbd_nodes: string[]
  controller_nodes: string[]
  compute_batches: string[][]
  login_nodes: string[]
}

export interface SlurmUpgradeOperation {
  id: string
  from_build_id: string
  to_build_id: string
  status: string   // "queued" | "in_progress" | "paused" | "completed" | "failed" | "rollback_initiated"
  phase?: string   // "dbd" | "controller" | "compute" | "login"
  current_batch: number
  total_batches: number
  batch_size: number
  drain_timeout_min: number
  confirmed_db_backup: boolean
  initiated_by: string
  started_at: number
  completed_at?: number
  node_results?: Record<string, { ok: boolean; error?: string; installed_version?: string; phase: string }>
}

export interface ListSlurmUpgradesResponse {
  operations: SlurmUpgradeOperation[]
  total: number
}

export interface SlurmNodeSyncStatus {
  node_id: string
  state: SlurmNodeConfigState[]
}

export interface SlurmNodeConfigState {
  filename: string
  deployed_version: number
  content_hash: string
  deployed_at: number
  push_op_id?: string
}

export interface SlurmNodeRole {
  node_id: string
  roles: string[]
}

export interface SlurmNodeOverride {
  node_id: string
  params: Record<string, string>
}

// ── Slurm Sprint 12 — TAIL-1..4 types ────────────────────────────────────────

export interface SlurmRenderPreviewResponse {
  filename: string
  node_id: string
  rendered_content: string
  checksum: string
}

export interface SlurmDepMatrixRow {
  id: string
  slurm_version_min: string
  slurm_version_max: string
  dep_name: string
  dep_version_min: string
  dep_version_max: string
  source: string
}

export interface SlurmDepMatrixResponse {
  matrix: SlurmDepMatrixRow[]
  total: number
}

export interface SlurmPushOperation {
  id: string
  filenames: string[]
  file_versions: Record<string, number>
  apply_action: string
  status: string   // "pending" | "running" | "completed" | "failed"
  node_count: number
  success_count: number
  failure_count: number
  started_at: number
  completed_at?: number
  node_results?: Record<string, SlurmPushNodeResult>
}

export interface SlurmPushNodeResult {
  ok: boolean
  error?: string
  file_results: SlurmPushFileResult[]
  apply_result: { ok: boolean; error?: string; output?: string }
}

export interface SlurmPushFileResult {
  filename: string
  ok: boolean
  error?: string
}

export interface SlurmMungeKeyResponse {
  status: string
  message: string
}

// ── Sprint 24 #153: Jobs + Partitions ────────────────────────────────────────

export interface SlurmJob {
  job_id: string
  name: string
  state: string       // RUNNING | PENDING | COMPLETED | FAILED | CANCELLED | ...
  user: string
  partition: string
  num_nodes: string
  time_used: string   // elapsed walltime D-HH:MM:SS
  time_limit: string  // time limit D-HH:MM:SS or "UNLIMITED"
  command: string
  req_cpus: string
  req_memory: string
  node_list: string
  reason: string      // PendingReason when PENDING
}

export interface ListSlurmJobsResponse {
  jobs: SlurmJob[]
  total: number
}

export interface SlurmPartitionInfo {
  name: string
  state: string           // up | down | drain | inact
  total_nodes: number
  allocated_nodes: number
  idle_nodes: number
  is_default: boolean
  max_time: string        // e.g. "7-00:00:00" or "UNLIMITED"
}

export interface ListSlurmPartitionsResponse {
  partitions: SlurmPartitionInfo[]
  total: number
}
