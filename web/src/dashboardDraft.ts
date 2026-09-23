import { validateBoards, type PersonalBoard } from './dashboardConfig'
export const draftKey = 'monitor.dashboard-draft.v1'
export interface DashboardDraft { version: 1; board: PersonalBoard; expected: PersonalBoard | null }
function validateDraft(value: unknown): DashboardDraft {
  if (!value || typeof value !== 'object') throw new Error('草稿格式无效。')
  const data = value as DashboardDraft
  if (data.version !== 1 || !data.board || typeof data.board.title !== 'string') throw new Error('草稿版本或内容无效。')
  const b = data.board
  // Incomplete titles and empty selections are valid drafts, but not saved boards.
  const empty = Array.isArray(b.groupIds) && !b.groupIds.length && Array.isArray(b.chartIds) && !b.chartIds.length
  const [clean] = validateBoards([{ ...b, title: b.title.trim() || '未命名草稿', ...(empty ? { chartIds: ['draft-placeholder'] } : {}) }])
  if (b.title.length > 100) throw new Error('草稿名称过长。')
  const expected = data.expected === null ? null : validateBoards([data.expected])[0]!
  if (expected && expected.id !== clean!.id) throw new Error('草稿与原看板不匹配。')
  return { version: 1, board: { ...clean!, title: b.title, ...(empty ? { chartIds: [] } : {}) }, expected }
}
export function encodeDraft(value: DashboardDraft): string {
  const raw = JSON.stringify(validateDraft(value))
  if (raw.length > 500_000) throw new Error('草稿过大，无法暂存。')
  return raw
}
export function decodeDraft(raw: string): DashboardDraft {
  if (raw.length > 500_000) throw new Error('草稿过大，无法恢复。')
  return validateDraft(JSON.parse(raw))
}
