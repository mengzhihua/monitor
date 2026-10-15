import { download, get, post } from '../transport'
import type { HandlingChange, HandlingRecord, OperationsSnapshot, NotificationSnapshot, MaintenanceSnapshot, MaintenanceChange, OperationsView, OperationsViews, HandlingHistoryPage, ServiceReport, TopologyEdge, AvailabilityRow } from './types'

/** Global operations use explicit query filters, never the active metric node. */
export const operationsApi = {
  operationsViews: (signal?: AbortSignal) => get<OperationsViews>('/api/v1/operations/views', signal),
  saveOperationsViews: (revision: number, views: OperationsView[]) => post<OperationsViews>('/api/v1/operations/views', { revision, views }),
  operations: (signal?: AbortSignal, query: Record<string, string> = {}) => {
    const q = new URLSearchParams(query)
    const s = q.toString()
    return get<OperationsSnapshot>('/api/v1/operations' + (s ? '?' + s : ''), signal)
  },
  services: (signal?: AbortSignal, window = '24h') => get<ServiceReport>('/api/v1/services?window=' + encodeURIComponent(window), signal),
  topology: (signal?: AbortSignal) => get<{ edges: TopologyEdge[] }>('/api/v1/topology', signal),
  availability: (signal?: AbortSignal, window = '24h') => get<{ from: number; to: number; alarms: AvailabilityRow[] }>('/api/v1/reports/availability?window=' + encodeURIComponent(window), signal),
  testNotification: (channel: string) => post<{ queued: boolean }>('/api/v1/operations/notifications/test', { channel }),
  notifications: (signal?: AbortSignal) => get<NotificationSnapshot>('/api/v1/operations/notifications', signal),
  maintenance: (signal?: AbortSignal) => get<MaintenanceSnapshot>('/api/v1/operations/maintenance', signal),
  changeMaintenance: (body: MaintenanceChange) => post<MaintenanceSnapshot>('/api/v1/operations/maintenance', body),
  handlingHistory: (params: Record<string, string>, signal?: AbortSignal) =>
    get<HandlingHistoryPage>('/api/v1/operations/history?' + new URLSearchParams(params), signal),
  exportHandlingHistory: async (params: Record<string, string>, signal?: AbortSignal) => {
    const r = await download('/api/v1/operations/history/export?' + new URLSearchParams(params), signal)
    return r.blob()
  },
  exportDiagnostic: async (params: Record<string, string> = {}, signal?: AbortSignal) => {
    const q = new URLSearchParams(params)
    const r = await download('/api/v1/operations/diagnostic/export' + (q.toString() ? '?' + q : ''), signal)
    const disposition = r.headers.get('Content-Disposition') || ''
    const match = /filename="([^"]+)"/.exec(disposition)
    // Read the full body before building the Blob so Playwright/object-URL downloads
    // are not handed an empty stream under chunked Transfer-Encoding.
    const body = await r.arrayBuffer()
    return {
      blob: new Blob([body], { type: 'application/json;charset=utf-8' }),
      filename: match?.[1] || `monitor-diagnostic-${Date.now()}.json`,
    }
  },
  acknowledge: (body: { id: string; action: string; note: string; revision: number }) =>
    post<HandlingRecord>('/api/v1/operations/acknowledgements', body),
  handleProblems: (body: HandlingChange & { note: string; items: { id: string; revision: number }[] }) => post<{ count: number; records: HandlingRecord[] }>('/api/v1/operations/handling/batch', body),
  handleProblem: (body: HandlingChange & { id: string; note: string; revision: number }) =>
    post<HandlingRecord>('/api/v1/operations/handling', body),
}
