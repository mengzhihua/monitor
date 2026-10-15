import type { AlertRuleSpec } from '../alarms/types'

export type NodeStatus = 'live' | 'stale' | 'offline'
export interface NodeInfo {
  id: string; hostname: string; os: string; arch: string; labels: Record<string, string> | null; update_every: number
  version: string; status: NodeStatus; local: boolean; first_seen: number; last_seen: number; last_data?: number
  charts_count: number; alarms: { warning: number; critical: number }; functions?: string[]; peer?: string
  space_id?: string; room_id?: string; replica?: boolean
  aclk?: boolean; protocol?: string
}
export interface Space { id: string; name: string; created: number }
export interface Room { id: string; name: string; space_id: string; nodes?: string[] }
export interface Claim { token: string; space_id: string; room_id: string; node_id?: string; expires: number; used_at?: number }
export interface NodeConfig { node_id: string; disabled?: string[]; yaml?: string; updated?: number }
export interface NodeApplyState { rev: number; state: 'applied' | 'rejected' | 'deferred'; error?: string; at: number }
export interface Template {
  id?: string
  name: string
  description?: string
  macros?: Record<string, string>
  rules?: AlertRuleSpec[]
  removed?: string[]
  disabled?: string[]
  tags?: Record<string, string>
  assign?: TemplateAssign
  updated?: number
}
export interface TemplateAssign { rooms?: string[]; nodes?: string[]; labels?: Record<string, string> }
export interface HealthOverlay {
  rev: number
  templates?: string[]
  macros?: Record<string, string>
  rules?: AlertRuleSpec[]
  removed?: string[]
  disabled?: string[]
  tags?: Record<string, string>
  rule_source?: Record<string, string>
}
export interface NodeInventoryView {
  node_id: string
  auto: Record<string, string>
  tags: Record<string, string>
  manual: Record<string, string>
  effective: Record<string, string>
  updated: number
  sort_order: string[]
}
export interface NodeConfigFull extends NodeConfig {
  reported?: string; report_at?: number; apply?: NodeApplyState; online?: boolean; pending?: boolean; pushed?: boolean
}
export interface NodesResponse { now: number; nodes: NodeInfo[] }
export interface ConsoleNode {
  id: string; hostname: string; status: string; aclk?: boolean; protocol?: string
  charts_count?: number; last_seen?: number; alarms?: { warning: number; critical: number }
}
export interface ConsoleRoom {
  id: string; name: string; nodes: ConsoleNode[]
  alarms: { warning: number; critical: number }
  alarm_list?: { name: string; chart: string; status: string }[]
}
export interface ConsoleSpace { id: string; name: string; created?: number; rooms: ConsoleRoom[] }
export interface ConsoleResponse {
  spaces: ConsoleSpace[]
  unassigned?: ConsoleNode[]
  routing?: { roles?: Record<string, string[]>; channels?: { name: string; configured?: boolean }[] }
  aclk: { available?: boolean; online?: boolean; protocol?: string; storage?: string; nodes?: number; live?: number }
}
