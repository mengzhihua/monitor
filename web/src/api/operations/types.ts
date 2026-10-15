import type { NodeInfo } from '../hub/types'

export interface ResourceMetric { value: number | null; at: number; state: 'fresh' | 'stale' | 'unavailable' }
export interface DiskMetric { mount: string; value: number | null; at: number; state: 'fresh' | 'stale' | 'unavailable' }
export interface OperationsNode extends NodeInfo {
  cpu: ResourceMetric; memory: ResourceMetric; disks: DiskMetric[]; alarm_coverage: 'local' | 'mirrored' | 'peer' | 'disabled' | 'unknown' | 'empty'
}
export type HandlingStatus = 'open' | 'investigating' | 'watching'
export interface HandlingAction {
  at: number; actor: string; action: string; note: string
  previous_assignee?: string; assignee?: string; previous_status?: HandlingStatus; status?: HandlingStatus
}
export interface HandlingChange {
  action: 'acknowledge' | 'unacknowledge' | 'comment' | 'assign' | 'unassign' | 'progress'
  assignee?: string; status?: HandlingStatus
}
export interface HandlingRecord {
  problem: { node: string; hostname: string; chart: string; name: string; severity: string; since: number }
  id: string; acknowledged: boolean; revision: number; assignee: string; status: HandlingStatus
  history: HandlingAction[]
}
export interface Problem {
  id: string; node: string; hostname: string; node_status: string; chart: string; name: string
  severity: 'WARNING' | 'CRITICAL'; family: string; info: string; value: number | null; units: string
  since: number; updated: number; stale: boolean; recovery_hold?: boolean; hold_until?: number
  handling: HandlingRecord
  delivery?: { at: number; channel: string; outcome: string; reason?: string }
}
export interface OperationsSnapshot {
  current_user: { name: string; role: string }
  assignees: { name: string; role: string }[]
  activity: HandlingRecord[]
  now: number; nodes: OperationsNode[]; problems: Problem[]; summary: Record<string, number>; persistent: boolean
  page?: { limit: number; nodes_matched: number; problems_matched: number; families: { name: string; count: number }[] }
}
export interface NotificationResult {
  id: number; event_id: number; at: number; name: string; chart: string; severity: string; repeat: boolean
  channel: string; outcome: string; reason: string; http_status?: number; duration_ms: number; test?: boolean
}
export interface NotificationSnapshot {
  available: boolean; can_test?: boolean; scope: 'local'; hostname: string; since: number; now: number; enabled: boolean; closed: boolean
  queue_size: number; queue_limit: number; retention: number; enqueued: number; suppressed: number; unrouted: number
  dropped: number; accepted: number; failed: number; total: number; in_flight: NotificationResult | null
  channels: { name: string; configured_count: number; attempts: number; accepted: number; failed: number; last_attempt: number; last_accepted: number; last_failed: number }[]
  recent: NotificationResult[]
}
export interface MaintenanceSpec {
  title: string; reason: string; scope: 'all' | 'alarm'; chart: string; alarm: string; starts_at: number; duration_seconds: number
}
export interface MaintenancePlan extends MaintenanceSpec {
  id: string; ends_at: number; created_at: number; created_by: string
  canceled_at?: number; canceled_by?: string; cancel_reason?: string; state: 'active' | 'scheduled' | 'ended' | 'canceled'
}
export interface MaintenanceSnapshot {
  revision: number; plans: MaintenancePlan[]; available: boolean; can_manage: boolean; persistent: boolean
  scope: 'local'; hostname: string; now: number; pending_limit: number; history_limit: number
  targets: { chart: string; alarm: string }[]
}
export type MaintenanceChange = { action: 'create'; revision: number; plan: MaintenanceSpec }
  | { action: 'cancel'; revision: number; id: string; reason: string }
export interface OperationsView {
  name: string; query: string; severity: string; nodeStatus: string; pendingOnly: boolean; ownerFilter: string; progressFilter: string
}
export interface OperationsViews {
  revision: number; views: OperationsView[]; enabled: boolean; persistent: boolean; limit: number
  user: { name: string; role: string }
}
export interface HandlingHistoryRecord extends HandlingRecord {
  updated_at: number; history_truncated: boolean
}
export interface HandlingHistoryPage {
  records: HandlingHistoryRecord[]; total: number; stored: number; capacity: number; actions_per_record: number
  snapshot: string; next_cursor: string; filter: Record<string, string | number>
}
export interface ServiceNode {
  name: string; status: string; sla: number; alarms?: string[]; children?: string[]
}
export interface ServiceReport { from: number; to: number; services: ServiceNode[] }
export interface TopologyEdge { source: string; target: string; kind: string }
export interface AvailabilityRow { name: string; chart: string; uptime: number; raised_seconds: number; window_seconds: number }
