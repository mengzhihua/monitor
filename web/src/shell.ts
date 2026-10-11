import { get, send } from './api'
export interface ShellTarget { id: string; name: string; node: string; kind: 'local' | 'ssh'; address?: string; user?: string; host_key?: string; host_key_algorithm?: string }
export interface ShellTargets { enabled: boolean; secure: boolean; audit_enabled: boolean; idle_seconds: number; max_seconds: number; targets: ShellTarget[] }
export const shell = {
 targets: (signal?: AbortSignal) => get<ShellTargets>('/api/v1/shell/targets', signal),
 ticket: (target: string, signal?: AbortSignal) => send<{ticket: string; expires_at: number}>('POST','/api/v1/shell/tickets',{target},signal),
 url: () => `${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/api/v1/shell/connect`,
}
