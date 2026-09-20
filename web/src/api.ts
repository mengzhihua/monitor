export interface Dimension { id: string; name: string; algorithm: string; hidden?: boolean }
export interface Chart {
  id: string; context: string; family: string; title: string; units: string
  chart_type: 'line' | 'area' | 'stacked'; priority: number; update_every: number
  plugin: string; module: string; labels: Record<string, string> | null
  dimensions: Dimension[]; first_entry: number; last_entry: number
}
export interface ChartsResponse { hostname: string; update_every: number; charts_count: number; charts: Record<string, Chart> }
export interface Info {
  version: string; mode: string; uptime: number; charts_count: number; metrics_count: number
  host: { id: string; hostname: string; os: string; arch: string; labels: Record<string, string>; update_every: number }
  collectors: { name: string; enabled: boolean; error?: string; runs: number; failures: number; last_run_ms: number }[]
}
export interface DataResponse {
  id: string; units: string; after: number; before: number; view_update_every: number
  dimension_ids: string[]; dimension_names: string[]
  result: { labels: string[]; data: (number | null)[][] }
}
export interface LiveMsg { chart: string; t: number; v: Record<string, number> }

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
  data: (chart: string, after: number, before = 0, points = 0) =>
    get<DataResponse>(`/api/v1/data?chart=${encodeURIComponent(chart)}&after=${after}&before=${before}&points=${points}`),
  liveURL(charts: string[] = []) {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const q = charts.length ? `?charts=${encodeURIComponent(charts.join(','))}` : ''
    return `${proto}//${location.host}/api/v1/live${q}`
  },
  /** WebSocket subprotocols: browsers cannot set headers, so the token rides here. */
  liveProtocols(): string[] {
    return auth.token ? ['monitor', `bearer.${encodeURIComponent(auth.token)}`] : ['monitor']
  },
}
