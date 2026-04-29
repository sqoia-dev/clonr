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
  created_at: string
  updated_at: string
}

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
