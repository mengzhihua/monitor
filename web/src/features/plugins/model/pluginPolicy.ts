import type { PlatformInfo, PluginAction, PluginCatalog, PluginStatus } from '../../../api/plugins/types'

export function pluginAccess(platform: PlatformInfo | undefined, role: string, authenticated: boolean) {
  const supported = authenticated && role === 'admin' && platform?.api_version === 1 && platform.scope === 'local'
  const read = !!supported && platform!.capabilities.includes('plugins.catalog.v1')
  return { read, control: read && platform!.capabilities.includes('plugins.control.v1') }
}

/** The catalog is an observed runtime snapshot; a disabled config lock cannot be lifted here. */
export function allowsPluginAction(catalog: PluginCatalog | null, action: PluginAction, name?: string): boolean {
  if (!catalog?.running) return false
  if (action === 'rescan') return true
  const plugin = catalog.plugins.find(plugin => plugin.name === name)
  if (!plugin) return false
  if (action === 'disable') return plugin.enabled
  if (plugin.locked) return false
  return action === 'enable' ? !plugin.enabled : plugin.enabled
}

const states: Record<PluginStatus['state'], string> = {
  starting: '正在启动', running: '运行中', waiting: '等待重启', stopped: '已停止', disabled: '已停用', failed: '启动失败',
}
const sources: Record<PluginStatus['source'], string> = { configured: '配置声明', legacy: '目录脚本', manifest: '清单插件包' }
export const pluginState = (state: PluginStatus['state']) => Object.hasOwn(states, state) ? states[state] : '未知状态'
export const pluginSource = (source: PluginStatus['source']) => Object.hasOwn(sources, source) ? sources[source] : '未知来源'
