import { dashboards, type Dashboard } from './dashboards'

export const storageKey = 'monitor.personal-dashboards.v1'
export const groupOptions = [...new Map(dashboards.flatMap(b => b.groups).map(g => [g.id, g])).values()]
export interface PersonalBoard {
  id: string; title: string; description: string; groupIds: string[]; chartIds: string[]
  windowSec: number; columns: number; limit: number; hideEmpty: boolean
}
export const ranges = [0, 60, 300, 900, 3600, 21600, 86400, 604800]
export function newBoard(): PersonalBoard {
  return { id: `custom-${Array.from(crypto.getRandomValues(new Uint8Array(16)), n => n.toString(16).padStart(2, '0')).join('')}`, title: '', description: '', groupIds: [], chartIds: [], windowSec: 0, columns: 0, limit: 4, hideEmpty: false }
}
export function validateBoards(value: unknown): PersonalBoard[] {
  if (!Array.isArray(value) || value.length > 20) throw new Error('最多支持 20 个个人看板。')
  const ids = new Set<string>()
  return value.map(raw => {
    if (!raw || typeof raw !== 'object') throw new Error('看板配置格式错误。')
    const b = raw as PersonalBoard
    const list = (v: unknown, max: number): v is string[] => Array.isArray(v) && v.length <= max && v.every(x => typeof x === 'string' && x.length > 0 && x.length <= 500) && new Set(v).size === v.length
    if (typeof b.id !== 'string' || !/^custom-[\w-]{1,80}$/.test(b.id) || ids.has(b.id)
      || typeof b.title !== 'string' || !b.title.trim() || b.title.length > 100
      || typeof b.description !== 'string' || b.description.length > 500
      || !list(b.groupIds, 30) || !b.groupIds.every(id => groupOptions.some(g => g.id === id))
      || !list(b.chartIds, 100) || !b.groupIds.length && !b.chartIds.length
      || !ranges.includes(b.windowSec) || ![0, 1, 2, 3].includes(b.columns)
      || ![2, 4, 8, 12].includes(b.limit) || typeof b.hideEmpty !== 'boolean') {
      throw new Error('配置无效：请填写名称，选择至少一个分组或图表，并检查布局、时间范围及数量限制。')
    }
    ids.add(b.id)
    return { id: b.id, title: b.title.trim(), description: b.description, groupIds: [...b.groupIds], chartIds: [...b.chartIds], windowSec: b.windowSec, columns: b.columns, limit: b.limit, hideEmpty: b.hideEmpty }
  })
}
export function decodeBoards(text: string): PersonalBoard[] {
  if (new TextEncoder().encode(text).length > 500_000) throw new Error('配置文件不能超过 500 KB。')
  const data = JSON.parse(text)
  if (data?.version !== 1) throw new Error('不支持此配置版本。')
  return validateBoards(data.boards)
}
export function encodeBoards(boards: PersonalBoard[]): string {
  const text = JSON.stringify({ version: 1, boards: validateBoards(boards) }, null, 2)
  if (new TextEncoder().encode(text).length > 500_000) throw new Error('个人看板配置总大小不能超过 500 KB，请减少指定图表。')
  return text
}
export function asDashboard(b: PersonalBoard): Dashboard {
  return { id: b.id, title: b.title, description: b.description, category: '我的看板', groups: [
    ...b.groupIds.map(id => groupOptions.find(g => g.id === id)!),
    ...(b.chartIds.length ? [{ id: `${b.id}-charts`, title: '指定图表', hint: '按保存顺序显示当前节点存在的图表；未采集的图表 ID 仍保留在配置中。', patterns: [], chartIds: b.chartIds }] : []),
  ] }
}
