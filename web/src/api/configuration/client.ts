import { get, send, post } from '../transport'
import type { AgentVisual, ManageConfig } from './types'

/** Agent configuration, lifecycle and access settings apply to the local server. */
export const configurationApi = {
  manageHealth: () => get<{ enabled: boolean; silent: boolean; maintenance: boolean; maint_until?: number }>('/api/v1/manage/health'),
  setHealth: (body: Record<string, unknown>) => send<unknown>('PUT', '/api/v1/manage/health', body),
  manageConfig: (signal?: AbortSignal) => get<ManageConfig>('/api/v1/manage/config', signal),
  putManageConfig: (yaml: string, ifUpdated?: number) => send<ManageConfig>('PUT', '/api/v1/manage/config', { yaml, if_updated: ifUpdated }),
  putManageForm: (form: AgentVisual, ifUpdated?: number) => send<ManageConfig>('PUT', '/api/v1/manage/config', { form, if_updated: ifUpdated }),
  rollbackManageConfig: () => post<ManageConfig>('/api/v1/manage/config/rollback', {}),
  restartService: () => post<{ ok: boolean }>('/api/v1/manage/restart', {}),
  share: (ttl = '24h') => post<{ token: string; url: string; until: number }>('/api/v1/share', { ttl }),
  ldapLogin: (user: string, password: string) => post<{ token: string; role: string }>('/api/v1/auth/ldap', { user, password }),
  oidcLoginURL: () => '/api/v1/auth/oidc/login',
}
