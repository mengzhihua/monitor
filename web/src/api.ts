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
  host: { id: string; hostname: string; os: string; arch: string; labels: Record<string, string>; update_every: number }
  collectors: { name: string; enabled: boolean; error?: string; runs: number; failures: number; last_run_ms: number }[]
  plugins?: PluginStatus[]
  db?: { persistence?: { last_checkpoint: number; finished: number; interval_seconds: number; error?: string } }
  alarms: AlarmSummary | null
  user?: { name: string; role: 'admin' | 'troubleshooter' | 'viewer' }
  nodes_count?: number
  streaming_enabled?: boolean
  stream?: { enabled: boolean; connected: boolean; destination?: string; last_error?: string; sent: number; replicated: number; dropped: number; protocol?: string; claimed?: boolean }
  aclk?: { available: boolean; online: boolean; protocol?: string; claimed?: boolean; destination?: string; storage?: string; nodes?: number; live?: number; capabilities?: string[] }
}
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
export interface AgentUser { name: string; token: string; role: string }
export interface AgentTarget { name: string; fields: string; url: string; address: string; listen: string; user: string }
export interface AgentRole { name: string; channels: string[] }
export interface AgentNotify {
  webhook_url: string
  slack_webhook_url: string
  slack_channel: string
  dingtalk_webhook_url: string
  wecom_webhook_url: string
  feishu_webhook_url: string
  feishu_webhook_url_env: string
  feishu_secret: string
  feishu_secret_env: string
  email_server: string
  email_from: string
  email_to: string[]
  telegram_token: string
  telegram_chat_id: string
  discord_webhook_url: string
  ntfy_url: string
  ntfy_topic: string
  ntfy_topic_env: string
  gotify_url: string
  gotify_token: string
  gotify_token_env: string
  bark_url: string
  bark_device_key: string
  bark_device_key_env: string
  roles: AgentRole[]
}
export interface AgentVisual {
  mode: 'agent' | 'hub'
  hostname: string
  update_every: number
  data_dir: string
  web_enabled: 'default' | 'on' | 'off'
  listen: string
  allow_from: string[]
  ticket_webhook: string
  users: AgentUser[]
  collectors_enabled: string[]
  collectors_disabled: string[]
  health_enabled: 'default' | 'on' | 'off'
  health_silent: boolean
  stream_enabled: boolean
  stream_destinations: string[]
  stream_api_key: string
  stream_protocol: string
  hub_api_keys: string[]
  hub_peers: string[]
  hub_storage: string
  hub_space: string
  hub_room: string
  notify: AgentNotify
  targets: AgentTarget[]
}
export interface AgentConfigFile {
  path: string
  yaml: string
  writable: boolean
  restart_required: boolean
  backup: boolean
  form?: AgentVisual
  form_error?: string
}
export interface NodesResponse { now: number; nodes: NodeInfo[] }
export interface ResourceMetric { value: number | null; at: number; state: 'fresh' | 'stale' | 'unavailable' }
export interface OperationsNode extends NodeInfo {
  cpu: ResourceMetric; memory: ResourceMetric; alarm_coverage: 'local' | 'mirrored' | 'peer' | 'disabled' | 'unknown' | 'empty'
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
  since: number; updated: number; stale: boolean; handling: HandlingRecord
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
export type AlarmStatus = 'REMOVED' | 'UNINITIALIZED' | 'UNDEFINED' | 'CLEAR' | 'WARNING' | 'CRITICAL'
export interface Alarm {
  id: number; name: string; chart: string; context: string; family: string; class?: string; type?: string; component?: string
  units: string; info: string; lookup?: string; calc?: string; warn?: string; crit?: string; update_every: number
  recipient: string; source: string; status: AlarmStatus; value: number | null; last_updated: number
  last_status_change: number; active: boolean; delay_up_to_timestamp?: number; last_notified?: number
  silenced?: boolean
}
export interface SilenceState { all: boolean; until?: number; alarms: Record<string, number>; maintenance?: boolean; maint_until?: number }
export interface AlarmLogEntry {
  unique_id: number; alarm_id: number; when: number; hostname: string; name: string; chart: string; context: string
  family: string; status: AlarmStatus; old_status: AlarmStatus; value: number | null; old_value: number | null
  units: string; info: string; recipient: string; delay: number; repeat?: boolean; notified: boolean; notified_at?: number
}
export interface AlarmSummary { normal: number; warning: number; critical: number; silent: number }
export interface AlarmsResponse { hostname: string; now: number; summary: AlarmSummary; alarms: Record<string, Alarm> }
export type LiveAlarmMsg = { node?: string; alarm: AlarmLogEntry }
export interface FunctionInfo { name: string; help: string; timeout: number }
/** Tabular function result (e.g. `processes`). */
export interface FunctionTable { columns: string[]; rows: Record<string, unknown>[]; total: number }
export interface FunctionResponse { function: string; time: number; result: FunctionTable | unknown }
export interface Weight {
  chart: string; context: string; title: string; dimension?: string; score: number; anomaly_rate: number
}
export interface WeightsResponse { method: string; group?: string; weights: Weight[]; count: number }
export interface LogRow { time: number; priority: string; unit: string; pid: string; message: string }
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

async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  const headers: Record<string, string> = {}
  if (auth.token) headers.Authorization = `Bearer ${auth.token}`
  const r = await fetch(base + path, { headers, signal })
  if (!r.ok) throw new ApiError(r.status, `${path}: ${r.status} ${await r.text()}`)
  return r.json() as Promise<T>
}

async function send<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  if (auth.token) headers.Authorization = `Bearer ${auth.token}`
  let init: RequestInit = { method, headers }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    init = { ...init, body: JSON.stringify(body) }
  }
  const r = await fetch(base + path, init)
  if (r.status === 204) return undefined as T
  if (!r.ok) throw new ApiError(r.status, `${path}: ${r.status} ${await r.text()}`)
  const text = await r.text()
  return (text ? JSON.parse(text) : undefined) as T
}

async function post<T>(path: string, body: unknown): Promise<T> {
  return send<T>('POST', path, body)
}

/**
 * Selected node on a hub: '' is the hub itself, otherwise a remote node id.
 * Every node-scoped request carries it as `node=`.
 */
export const selection = {
  node: '',
}

function q(params: Record<string, string | number>): string {
  const p = new URLSearchParams()
  if (selection.node) p.set('node', selection.node)
  for (const [k, v] of Object.entries(params)) p.set(k, String(v))
  const s = p.toString()
  return s ? `?${s}` : ''
}

export const api = {
  operationsViews: (signal?: AbortSignal) => get<OperationsViews>('/api/v1/operations/views', signal),
  saveOperationsViews: (revision: number, views: OperationsView[]) => post<OperationsViews>('/api/v1/operations/views', { revision, views }),
  operations: (signal?: AbortSignal, query: Record<string, string> = {}) => {
    const q = new URLSearchParams(query)
    const s = q.toString()
    return get<OperationsSnapshot>('/api/v1/operations' + (s ? '?' + s : ''), signal)
  },
  testNotification: (channel: string) => post<{ queued: boolean }>('/api/v1/operations/notifications/test', { channel }),
  notifications: (signal?: AbortSignal) => get<NotificationSnapshot>('/api/v1/operations/notifications', signal),
  maintenance: (signal?: AbortSignal) => get<MaintenanceSnapshot>('/api/v1/operations/maintenance', signal),
  changeMaintenance: (body: MaintenanceChange) => post<MaintenanceSnapshot>('/api/v1/operations/maintenance', body),
  handlingHistory: (params: Record<string, string>, signal?: AbortSignal) =>
    get<HandlingHistoryPage>('/api/v1/operations/history?' + new URLSearchParams(params), signal),
  exportHandlingHistory: async (params: Record<string, string>, signal?: AbortSignal) => {
    const headers: Record<string, string> = {}
    if (auth.token) headers.Authorization = `Bearer ${auth.token}`
    const r = await fetch(base + '/api/v1/operations/history/export?' + new URLSearchParams(params), { headers, signal })
    if (!r.ok) throw new ApiError(r.status, `${r.status} ${await r.text()}`)
    return r.blob()
  },
  acknowledge: (body: { id: string; action: string; note: string; revision: number }) =>
    post<HandlingRecord>('/api/v1/operations/acknowledgements', body),
  handleProblems: (body: HandlingChange & { note: string; items: { id: string; revision: number }[] }) => post<{ count: number; records: HandlingRecord[] }>('/api/v1/operations/handling/batch', body),
  handleProblem: (body: HandlingChange & { id: string; note: string; revision: number }) =>
    post<HandlingRecord>('/api/v1/operations/handling', body),
  info: (signal?: AbortSignal) => get<Info>('/api/v1/info', signal),
  nodes: (signal?: AbortSignal) => get<NodesResponse>('/api/v1/nodes', signal),
  charts: (signal?: AbortSignal) => get<ChartsResponse>(`/api/v1/charts${q({})}`, signal),
  alarms: (signal?: AbortSignal) => get<AlarmsResponse>(`/api/v1/alarms${q({ all: 'true' })}`, signal),
  alarmLog: (after = 0, signal?: AbortSignal) => get<AlarmLogEntry[]>(`/api/v1/alarm_log${q({ after })}`, signal),
  silence: (body: { all?: boolean; alarm?: string; chart?: string; until?: number; clear?: boolean } = {}) =>
    post<SilenceState>('/api/v1/alarms/silence', body),
  silenceState: () => get<SilenceState>('/api/v1/alarms/silence'),
  functions: (signal?: AbortSignal) => get<FunctionInfo[]>(`/api/v1/functions${q({})}`, signal),
  function: (name: string, args: Record<string, string> = {}, signal?: AbortSignal) =>
    get<FunctionResponse>(`/api/v1/function${q({ function: name, ...args })}`, signal),
  weights: (method = 'anomaly-rate', extra: Record<string, string | number> = {}) =>
    get<WeightsResponse>(`/api/v1/weights${q({ method, ...extra })}`),
  logs: (args: Record<string, string> = {}, signal?: AbortSignal) =>
    get<FunctionResponse>(`/api/v1/logs${q(args)}`, signal),
  data: (chart: string, after: number, before = 0, points = 0, signal?: AbortSignal) =>
    get<DataResponse>(`/api/v1/data${q({ chart, after, before, points })}`, signal),
  contexts: () => get<{ contexts: Record<string, { family: string; title: string; units: string; charts: string[]; dimensions: string[]; priority: number }> }>(`/api/v1/contexts${q({})}`),
  alarmSummary: () => get<{ status: Record<string, number>; classes?: Record<string, number> }>(`/api/v1/alarm_summary${q({})}`),
  manageHealth: () => get<{ enabled: boolean; silent: boolean; maintenance: boolean; maint_until?: number }>('/api/v1/manage/health'),
  setHealth: (body: Record<string, unknown>) => send<unknown>('PUT', '/api/v1/manage/health', body),
  agentConfig: () => get<AgentConfigFile>('/api/v1/manage/config'),
  saveAgentConfig: (yaml: string) => send<AgentConfigFile>('PUT', '/api/v1/manage/config', { yaml }),
  saveAgentForm: (form: AgentVisual) => send<AgentConfigFile>('PUT', '/api/v1/manage/config', { form }),
  rollbackAgentConfig: () => post<AgentConfigFile>('/api/v1/manage/config/rollback', {}),
  restartAgent: () => post<{ status: string }>('/api/v1/manage/restart', {}),
  share: (ttl = '24h') => post<{ token: string; url: string; until: number }>('/api/v1/share', { ttl }),
  ldapLogin: (user: string, password: string) => post<{ token: string; role: string }>('/api/v1/auth/ldap', { user, password }),
  spaces: () => get<{ spaces: Space[] }>('/api/v1/hub/spaces'),
  createSpace: (name: string) => post<Space>('/api/v1/hub/spaces', { name }),
  deleteSpace: (id: string) => send<void>('DELETE', `/api/v1/hub/spaces?id=${encodeURIComponent(id)}`),
  rooms: (spaceID = '') => get<{ rooms: Room[] }>(`/api/v1/hub/rooms${spaceID ? '?space_id=' + encodeURIComponent(spaceID) : ''}`),
  createRoom: (spaceID: string, name: string) => post<Room>('/api/v1/hub/rooms', { space_id: spaceID, name }),
  deleteRoom: (id: string) => send<void>('DELETE', `/api/v1/hub/rooms?id=${encodeURIComponent(id)}`),
  claims: () => get<{ claims: Claim[] }>('/api/v1/hub/claim-tokens'),
  issueClaim: (spaceID: string, roomID: string, ttl = '24h') =>
    post<Claim>('/api/v1/hub/claim-tokens', { space_id: spaceID, room_id: roomID, ttl }),
  putNodeConfig: (cfg: NodeConfig) => send<NodeConfig>('PUT', `/api/v1/hub/config?node=${encodeURIComponent(cfg.node_id)}`, cfg),
  console: () => get<ConsoleResponse>('/api/v1/hub/console'),
  oidcLoginURL: () => '/api/v1/auth/oidc/login',
  liveURL(charts: string[] = []) {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const params: Record<string, string> = {}
    if (charts.length) params.charts = charts.join(',')
    return `${proto}//${location.host}/api/v1/live${q(params)}`
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
