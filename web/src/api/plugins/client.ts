import { get, send } from '../transport'
import type { PlatformInfo, PluginActionRequest, PluginCatalog } from './types'

/** Plugin processes belong to this server; the selected monitoring node never applies. */
export const pluginsApi = {
  platform: (signal?: AbortSignal) => get<PlatformInfo>('/api/v1/platform', signal),
  catalog: (signal?: AbortSignal) => get<PluginCatalog>('/api/v1/plugins', signal),
  act: (body: PluginActionRequest, signal?: AbortSignal) => send<PluginCatalog>('POST', '/api/v1/plugins/actions', body, signal),
}
