import { get, send } from './api'

export interface AISettings {
  base_url: string
  model: string
  has_api_key: boolean
  allow_private: boolean
  revision: number
  enabled: boolean
  persistent: boolean
}
export interface AISettingsUpdate {
  base_url: string
  model: string
  api_key?: string
  clear_api_key: boolean
  allow_private: boolean
  revision: number
}
export interface AIDimension {
  id: string
  name: string
  count: number
  missing: number
  min: number | null
  max: number | null
  average: number | null
  first: number | null
  last: number | null
  last_at: number | null
}
export interface AISnapshot {
  node: string
  hostname: string
  generated_at: number
  after: number
  before: number
  charts: Array<{
    id: string; title: string; units: string; update_every: number
    dimensions: AIDimension[]; points: number; step: number
  }>
  alarms: Array<{ name: string; chart: string; status: string; value: number | null; units: string }>
  notes: string[]
}
export interface AIPreview { id: string; expires_at: number; snapshot: AISnapshot }
export interface AIResult {
  text: string
  model: string
  duration_ms: number
  truncated: boolean
  usage?: { prompt_tokens: number; completion_tokens: number; total_tokens: number }
  generated_at: number
}

export const ai = {
  settings: (signal?: AbortSignal) => get<AISettings>('/api/v1/ai/settings', signal),
  save: (body: AISettingsUpdate, signal?: AbortSignal) => send<AISettings>('PUT', '/api/v1/ai/settings', body, signal),
  remove: (revision: number, signal?: AbortSignal) => send<AISettings>('DELETE', '/api/v1/ai/settings', { revision }, signal),
  preview: (node: string, charts: string[], window: number, signal?: AbortSignal) =>
    send<AIPreview>('POST', '/api/v1/ai/preview', { node, charts, window }, signal),
  analyze: (preview_id: string, question: string, revision: number, signal?: AbortSignal) =>
    send<AIResult>('POST', '/api/v1/ai/analyze', { preview_id, question, revision }, signal),
}
