
export type AlarmStatus = 'REMOVED' | 'UNINITIALIZED' | 'UNDEFINED' | 'CLEAR' | 'WARNING' | 'CRITICAL'
export interface Alarm {
  id: number; name: string; chart: string; context: string; family: string; class?: string; type?: string; component?: string
  units: string; info: string; lookup?: string; calc?: string; warn?: string; crit?: string; update_every: number
  recipient: string; source: string; status: AlarmStatus; value: number | null; last_updated: number
  last_status_change: number; active: boolean; delay_up_to_timestamp?: number; last_notified?: number
  silenced?: boolean
  pending_status?: AlarmStatus; pending_since?: number; pending_until?: number; hold_until?: number
  recovery_hold?: boolean
}
export interface SilenceState { all: boolean; until?: number; alarms: Record<string, number>; maintenance?: boolean; maint_until?: number }
export interface AlertRuleSpec {
  name: string; on: string; class?: string; type?: string; component?: string
  lookup?: string; calc?: string; every?: string; units?: string; warn?: string; crit?: string
  for?: string; keep_firing_for?: string; no_data_state?: string; exec_err_state?: string; recovery?: string; delay?: string; repeat?: string; info?: string; to?: string; chart_labels?: Record<string, string>; disabled?: boolean
  macros?: Record<string, string>
}
export interface AlertRuleConfig {
  hash: string; name: string; on: string; source: string; every: number; config: Omit<AlertRuleSpec, 'on'> & { on?: string }
  origin: 'base' | 'override' | 'custom' | 'deleted' | 'invalid'; has_base: boolean; deleted: boolean
}
export interface AlertRulesSnapshot {
  api: number; revision: string; persistent: boolean; hostname: string; scope: 'local'; can_manage: boolean
  configs: AlertRuleConfig[]; count: number
}
export type AlertRuleChange = { revision: string; action: 'create' | 'update'; config: AlertRuleSpec }
  | { revision: string; action: 'delete' | 'reset'; name: string }
export interface AlertRulePreview {
  revision: string; action: AlertRuleChange['action']; name: string; valid: boolean; persistent: boolean
  matched: number; charts: { id: string; title: string; context: string; family: string }[]
  truncated: boolean; limit: number; disabled: boolean; notice: string
}
export interface AlarmLogEntry {
  unique_id: number; alarm_id: number; when: number; hostname: string; name: string; chart: string; context: string
  family: string; status: AlarmStatus; old_status: AlarmStatus; value: number | null; old_value: number | null
  units: string; info: string; recipient: string; delay: number; repeat?: boolean; notified: boolean; notified_at?: number
}
export interface AlarmSummary { normal: number; warning: number; critical: number; silent: number }
export interface AlarmsResponse { hostname: string; now: number; summary: AlarmSummary; alarms: Record<string, Alarm> }
export type LiveAlarmMsg = { node?: string; alarm: AlarmLogEntry }
