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
  host_id?: string
  problem?: AIAlarm
  charts: Array<{
    id: string; title: string; units: string; update_every: number
    dimensions: AIDimension[]; points: number; step: number; last_collected_at?: number; state?: string
  }>
  alarms: AIAlarm[]
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
  resolution?: AIResolution
}

export interface AIAlarm { name: string; chart: string; status: string; value: number | null; units: string; problem_id?: string; updated_at?: number; state?: string }
export interface AIProblemSelection { id: string; name: string; chart: string }
export interface AIPlanStep { id: string; title: string; kind: 'diagnostic' | 'remediation' | 'verification'; instruction: string; expected: string; risk: 'low' | 'medium' | 'high' | 'unknown'; rollback: string; chart: string; dimension: string }
export interface AIPlan { summary: string; hypotheses: Array<{ explanation: string; evidence: string; confidence: 'low' | 'medium' | 'high' }>; steps: AIPlanStep[] }
export interface AIResolution {
  id: string; node: string; hostname: string; created_at: number; updated_at: number; revision: number
  model: string; question: string; plan: AIPlan; baseline: AISnapshot
  progress: Array<{ id: string; status: 'todo' | 'done' | 'skipped'; note: string }>
  review?: { at: number; outcome: string; snapshot: AISnapshot; notes: string[]; comparisons: Array<{ chart: string; dimension: string; units: string; before: number | null; after: number | null; delta: number | null }> }
}
export interface AIResolutionSummary { id: string; node: string; hostname: string; summary: string; created_at: number; updated_at: number; done: number; total: number }

export const ai = {
  settings: (signal?: AbortSignal) => get<AISettings>('/api/v1/ai/settings', signal),
  save: (body: AISettingsUpdate, signal?: AbortSignal) => send<AISettings>('PUT', '/api/v1/ai/settings', body, signal),
  remove: (revision: number, signal?: AbortSignal) => send<AISettings>('DELETE', '/api/v1/ai/settings', { revision }, signal),
  preview: (node: string, charts: string[], window: number, signal?: AbortSignal, problem_id?: string) =>
    send<AIPreview>('POST', '/api/v1/ai/preview', { node, charts, window, ...(problem_id ? { problem_id } : {}) }, signal),
  analyze: (preview_id: string, question: string, revision: number, signal?: AbortSignal, mode?: 'resolution') =>
    send<AIResult>('POST', '/api/v1/ai/analyze', { preview_id, question, revision, ...(mode ? { mode } : {}) }, signal),
  resolutions: (node: string | undefined, signal?: AbortSignal) => get<{ records: AIResolutionSummary[]; limit: number; persistent: boolean }>(`/api/v1/ai/resolutions${node === undefined ? '' : '?node=' + encodeURIComponent(node)}`, signal),
  resolution: (id: string, signal?: AbortSignal) => get<AIResolution>(`/api/v1/ai/resolutions?id=${encodeURIComponent(id)}`, signal),
  progress: (id: string, revision: number, step_id: string, status: string, note: string, signal?: AbortSignal) => send<AIResolution>('POST', '/api/v1/ai/resolutions', { id, revision, step_id, status, note }, signal),
  review: (id: string, revision: number, signal?: AbortSignal) => send<AIResolution>('POST', '/api/v1/ai/resolutions/review', { id, revision }, signal),
  deleteResolution: (id: string, revision: number, signal?: AbortSignal) => send<{ deleted: boolean }>('DELETE', '/api/v1/ai/resolutions', { id, revision }, signal),
}
