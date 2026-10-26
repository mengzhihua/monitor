export interface PlatformInfo {
  api_version: number
  scope: 'local'
  capabilities: string[]
  plugin_protocols: string[]
}

export interface PluginManifest {
  api_version: number; id: string; name: string; version: string; description: string
  protocol: string; capabilities: string[]
}

export interface PluginStatus {
  name: string; command: string; state: 'starting' | 'running' | 'waiting' | 'stopped' | 'disabled' | 'failed'
  source: 'configured' | 'legacy' | 'manifest'; locked: boolean; manifest?: PluginManifest
  pid?: number; enabled: boolean; update_every: number; restarts: number; started?: number; error?: string
  stats: { lines: number; samples: number; charts: number; errors: number; last_error?: string; last_data?: number }
  budgets?: PluginBudgets
  stderr?: { bytes: number; lines: number; logged: number; suppressed: number; truncated: number }
  variables?: Record<string, number>; functions?: string[]
}

export interface PluginBudgets {
  max_charts?: number
  max_dimensions?: number
  max_labels?: number
  max_variables?: number
  max_line?: number
}

export interface PluginCatalog {
  schema_version: 1; revision: number; running: boolean
  budgets?: PluginBudgets
  plugins: PluginStatus[]
  diagnostics: { path: string; error: string }[]
}

export type PluginAction = 'enable' | 'disable' | 'restart' | 'rescan'
export interface PluginActionRequest { name?: string; action: PluginAction; if_revision: number }
