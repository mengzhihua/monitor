import type { AlarmSummary } from '../alarms/types'

export interface Dimension { id: string; name: string; algorithm: string; hidden?: boolean; anomaly?: boolean }
export interface Chart {
  id: string; context: string; family: string; title: string; units: string
  chart_type: 'line' | 'area' | 'stacked'; priority: number; update_every: number
  plugin: string; module: string; labels: Record<string, string> | null
  dimensions: Dimension[]; first_entry: number; last_entry: number
  anomaly?: boolean
}
export interface ChartsResponse { hostname: string; update_every: number; charts_count: number; charts: Record<string, Chart> }
export interface PluginStatus {
  name: string; command: string; state: 'starting' | 'running' | 'waiting' | 'stopped' | 'disabled' | 'failed'
  pid?: number; enabled: boolean; update_every: number; restarts: number; started?: number; error?: string
  stats: { lines: number; samples: number; charts: number; errors: number; last_error?: string; last_data?: number }
  variables?: Record<string, number>
}
export interface Info {
  version: string; mode: string; uptime: number; charts_count: number; metrics_count: number
  commit?: string; build_time?: string; dirty?: boolean
  host: { id: string; hostname: string; os: string; arch: string; labels: Record<string, string>; update_every: number }
  collectors: { name: string; enabled: boolean; error?: string; runs: number; failures: number; last_run_ms: number }[]
  plugins?: PluginStatus[]
  db?: { persistence?: { last_checkpoint: number; finished: number; interval_seconds: number; error?: string } }
  alarms: AlarmSummary | null
  user?: { name: string; role: 'admin' | 'troubleshooter' | 'viewer'; personal?: boolean }
  nodes_count?: number
  streaming_enabled?: boolean
  stream?: { enabled: boolean; connected: boolean; destination?: string; last_error?: string; sent: number; replicated: number; dropped: number; protocol?: string; claimed?: boolean }
  aclk?: { available: boolean; online: boolean; protocol?: string; claimed?: boolean; destination?: string; storage?: string; nodes?: number; live?: number; capabilities?: string[] }
}
export interface DataResponse {
  id: string; units: string; after: number; before: number; view_update_every: number
  dimension_ids: string[]; dimension_names: string[]
  dimension_anomaly?: number[]
  anomaly?: number[]
  group_by?: string
  api?: number
  result: { labels: string[]; data: (number | null)[][] }
}
export interface LiveMsg { node?: string; chart: string; t: number; v: Record<string, number> }
export interface FunctionInfo { name: string; help: string; timeout: number }
/** Tabular function result (e.g. `processes`). */
export interface FunctionTable { columns: string[]; rows: Record<string, unknown>[]; total: number }
export interface FunctionResponse { function: string; time: number; result: FunctionTable | unknown }
export interface Weight {
  chart: string; context: string; title: string; dimension?: string; score: number; anomaly_rate: number
}
export interface WeightsResponse { method: string; group?: string; weights: Weight[]; count: number }
export interface LogRow { time: number; priority: string; unit: string; pid: string; message: string }
