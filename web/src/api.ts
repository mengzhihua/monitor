export interface Dimension { id: string; name: string; algorithm: string; hidden?: boolean }
export interface Chart {
  id: string; context: string; family: string; title: string; units: string
  chart_type: 'line' | 'area' | 'stacked'; priority: number; update_every: number
  plugin: string; module: string; labels: Record<string, string> | null
  dimensions: Dimension[]; first_entry: number; last_entry: number
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
  host: { id: string; hostname: string; os: string; arch: string; labels: Record<string, string>; update_every: number }
  collectors: { name: string; enabled: boolean; error?: string; runs: number; failures: number; last_run_ms: number }[]
  plugins?: PluginStatus[]
  alarms: AlarmSummary | null
}
export interface DataResponse {
  id: string; units: string; after: number; before: number; view_update_every: number
  dimension_ids: string[]; dimension_names: string[]
  result: { labels: string[]; data: (number | null)[][] }
}
export interface LiveMsg { chart: string; t: number; v: Record<string, number> }
export type AlarmStatus = 'REMOVED' | 'UNINITIALIZED' | 'UNDEFINED' | 'CLEAR' | 'WARNING' | 'CRITICAL'
export interface Alarm {
  id: number; name: string; chart: string; context: string; family: string; class?: string; type?: string; component?: string
  units: string; info: string; lookup?: string; calc?: string; warn?: string; crit?: string; update_every: number
  recipient: string; source: string; status: AlarmStatus; value: number | null; last_updated: number
  last_status_change: number; active: boolean; delay_up_to_timestamp?: number; last_notified?: number
}
export interface AlarmLogEntry {
  unique_id: number; alarm_id: number; when: number; hostname: string; name: string; chart: string; context: string
  family: string; status: AlarmStatus; old_status: AlarmStatus; value: number | null; old_value: number | null
  units: string; info: string; recipient: string; delay: number; repeat?: boolean; notified: boolean; notified_at?: number
}
export interface AlarmSummary { normal: number; warning: number; critical: number; silent: number }
export interface AlarmsResponse { hostname: string; now: number; summary: AlarmSummary; alarms: Record<string, Alarm> }
export type LiveAlarmMsg = { alarm: AlarmLogEntry }

const base = ''
const TOKEN_KEY = 'monitor.token'

/**
 * Access token for agents started with `web.token`. Accepted once via
 * `/?token=...` (then removed from the URL so it is not kept in history) or
 * entered in the UI; kept in sessionStorage and sent as a Bearer header.
 */
export const auth = {
  get token(): string {
    return sessionStorage.getItem(TOKEN_KEY) ?? ''
  },
  set token(t: string) {
    if (t) sessionStorage.setItem(TOKEN_KEY, t)
    else sessionStorage.removeItem(TOKEN_KEY)
  },
  /** Import `?token=` from the page URL and scrub it from the address bar. */
  fromURL() {
    const u = new URL(location.href)
    const t = u.searchParams.get('token')
    if (t === null) return
    this.token = t
    u.searchParams.delete('token')
    history.replaceState(null, '', u.pathname + u.search + u.hash)
  },
}

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function get<T>(path: string): Promise<T> {
  const headers: Record<string, string> = {}
  if (auth.token) headers.Authorization = `Bearer ${auth.token}`
  const r = await fetch(base + path, { headers })
  if (!r.ok) throw new ApiError(r.status, `${path}: ${r.status} ${await r.text()}`)
  return r.json() as Promise<T>
}

export const api = {
  info: () => get<Info>('/api/v1/info'),
  charts: () => get<ChartsResponse>('/api/v1/charts'),
  alarms: () => get<AlarmsResponse>('/api/v1/alarms?all=true'),
  alarmLog: (after = 0) => get<AlarmLogEntry[]>(`/api/v1/alarm_log?after=${after}`),
  data: (chart: string, after: number, before = 0, points = 0) =>
    get<DataResponse>(`/api/v1/data?chart=${encodeURIComponent(chart)}&after=${after}&before=${before}&points=${points}`),
  liveURL(charts: string[] = []) {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const q = charts.length ? `?charts=${encodeURIComponent(charts.join(','))}` : ''
    return `${proto}//${location.host}/api/v1/live${q}`
  },
  /**
   * WebSocket subprotocols: browsers cannot set headers, so the token rides
   * here, base64url-encoded so any token satisfies the subprotocol grammar.
   */
  liveProtocols(): string[] {
    return auth.token ? ['monitor', `bearer.${base64url(auth.token)}`] : ['monitor']
  },
}

function base64url(s: string): string {
  const bytes = new TextEncoder().encode(s)
  let bin = ''
  bytes.forEach((b) => (bin += String.fromCharCode(b)))
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}
