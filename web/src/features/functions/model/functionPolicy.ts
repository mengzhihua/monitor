import type { FunctionInfo } from '../../../api/monitoring/types'

export const isPluginFunction = (name: string) => name.startsWith('plugin.')

/** A view refresh may repeat built-in diagnostics, but never execute a plugin. */
export function functionRequest(name: string, sort: string, explicit = false) {
  if (!name || isPluginFunction(name) && !explicit) return null
  const args: Record<string, string> = {}
  if (sort && ['processes', 'services', 'network-connections'].includes(name)) args.sort = sort
  return { name, args }
}

/** Plugins allow at most 60 seconds; leave transport time after that deadline. */
export function pluginRequestTimeout(fn: FunctionInfo | undefined): number {
  const seconds = fn?.timeout
  return (typeof seconds === 'number' && Number.isFinite(seconds) && seconds > 0 ? Math.min(seconds, 60) : 10) * 1000 + 5000
}
