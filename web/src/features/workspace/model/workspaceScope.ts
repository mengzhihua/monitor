export type WorkspaceScope = 'local' | 'node' | 'global'

export function workspaceKey(id: string, scope: WorkspaceScope | undefined, node: string): string {
  return `${id}:${scope === 'node' ? node : ''}`
}

export function workspaceScopeLabel(scope: WorkspaceScope | undefined, localHost: string, selectedHost?: string): string {
  if (scope === 'global') return '全局视图'
  if (scope === 'local') return `本机 · ${localHost}`
  return selectedHost || localHost
}
