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

async function get<T>(path: string): Promise<T> {
  const r = await fetch(base + path)
  if (!r.ok) throw new Error(`${path}: ${r.status} ${await r.text()}`)
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
}
