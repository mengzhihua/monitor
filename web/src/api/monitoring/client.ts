import { get } from '../transport'
import { nodeQuery } from '../selection'
import { auth } from '../auth'
import type { ChartsResponse, Info, DataResponse, FunctionInfo, FunctionResponse, WeightsResponse } from './types'

/** Live metric reads are node-scoped; agent identity remains local to the hub. */
export const monitoringApi = {
  info: (signal?: AbortSignal) => get<Info>('/api/v1/info', signal),
  charts: (signal?: AbortSignal) => get<ChartsResponse>(`/api/v1/charts${nodeQuery({})}`, signal),
  functions: (signal?: AbortSignal) => get<FunctionInfo[]>(`/api/v1/functions${nodeQuery({})}`, signal),
  function: (name: string, args: Record<string, string> = {}, signal?: AbortSignal) =>
    get<FunctionResponse>(`/api/v1/function${nodeQuery({ function: name, ...args })}`, signal),
  weights: (method = 'anomaly-rate', extra: Record<string, string | number> = {}, signal?: AbortSignal) =>
    get<WeightsResponse>(`/api/v1/weights${nodeQuery({ method, ...extra })}`, signal),
  logs: (args: Record<string, string> = {}, signal?: AbortSignal) =>
    get<FunctionResponse>(`/api/v1/logs${nodeQuery(args)}`, signal),
  data: (chart: string, after: number, before = 0, points = 0, signal?: AbortSignal) =>
    get<DataResponse>(`/api/v1/data${nodeQuery({ chart, after, before, points })}`, signal),
  contexts: (signal?: AbortSignal) => get<{ contexts: Record<string, { family: string; title: string; units: string; charts: string[]; dimensions: string[]; priority: number }> }>(`/api/v1/contexts${nodeQuery({})}`, signal),
  liveURL(charts: string[] = []) {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const params: Record<string, string> = {}
    if (charts.length) params.charts = charts.join(',')
    return `${proto}//${location.host}/api/v1/live${nodeQuery(params)}`
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
