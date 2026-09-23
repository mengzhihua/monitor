import { decodeBoards, encodeBoards, storageKey, type PersonalBoard } from './dashboardConfig'

type BoardStorage = Pick<Storage, 'getItem' | 'setItem'>
export function readPersonalBoards(storage: Pick<Storage, 'getItem'>): PersonalBoard[] {
  const raw = storage.getItem(storageKey)
  if (raw === null) return []
  try { return decodeBoards(raw) }
  catch { throw new Error('无法读取个人看板配置，原始数据已保留，请先备份或修复存储内容。') }
}
type Mutation = { type: 'save'; board: PersonalBoard; expected: PersonalBoard | null }
  | { type: 'delete'; expected: PersonalBoard }
  | { type: 'import'; boards: PersonalBoard[] }
/** Re-read immediately before writing; reject observed edits to the same board,
 * while preserving unrelated boards saved since the editor was opened. */
export function changePersonalBoards(storage: BoardStorage, change: Mutation): PersonalBoard[] {
  const latest = readPersonalBoards(storage)
  let next: PersonalBoard[]
  if (change.type === 'import') next = [...latest, ...change.boards]
  else {
    const id = change.type === 'save' ? change.board.id : change.expected.id
    const current = latest.find(b => b.id === id) ?? null
    if (JSON.stringify(current) !== JSON.stringify(change.expected)) {
      throw new Error('此看板已在其他标签页修改或删除。草稿已保留，请另存为新看板，或取消编辑后重新打开。')
    }
    next = change.type === 'delete' ? latest.filter(b => b.id !== id)
      : current ? latest.map(b => b.id === id ? change.board : b) : [...latest, change.board]
  }
  const encoded = encodeBoards(next)
  storage.setItem(storageKey, encoded)
  return decodeBoards(encoded)
}

export const preferencesKey = 'monitor.dashboard-preferences.v1'
export interface DashboardPreferences { selected: string; favorites: string[]; collapsed: boolean }
export function readPreferences(storage: Pick<Storage, 'getItem'>, ids: string[]): DashboardPreferences {
  const fallback = { selected: 'developer', favorites: [], collapsed: false }
  try {
    const raw = storage.getItem(preferencesKey)
    if (!raw || raw.length > 20_000) return fallback
    const p = JSON.parse(raw)
    return { selected: ids.includes(p.selected) ? p.selected : 'developer',
      favorites: Array.isArray(p.favorites) ? [...new Set<string>(p.favorites.filter((id: unknown) => typeof id === 'string' && ids.includes(id)))] : [],
      collapsed: p.collapsed === true }
  } catch { return fallback }
}
