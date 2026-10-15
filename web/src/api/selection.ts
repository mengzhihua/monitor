/**
 * Selected node on a hub: '' is the hub itself, otherwise a remote node id.
 * Every node-scoped request carries it as `node=`.
 */
export const selection = {
  node: '',
}

export function nodeQuery(params: Record<string, string | number>): string {
  const p = new URLSearchParams()
  if (selection.node) p.set('node', selection.node)
  for (const [k, v] of Object.entries(params)) p.set(k, String(v))
  const s = p.toString()
  return s ? `?${s}` : ''
}
