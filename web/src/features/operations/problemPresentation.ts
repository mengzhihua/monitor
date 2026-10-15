import type { HandlingAction, HandlingStatus, Problem } from '../../api/operations/types'

export const actionName: Record<string, string> = { acknowledge: '确认问题', unacknowledge: '撤销确认', comment: '补充备注', assign: '指派责任人', unassign: '取消指派', progress: '更新进度', auto_command: '自动动作', inhibited: '动作已抑制' }
export const progressName: Record<HandlingStatus, string> = { open: '待处理', investigating: '排查中', watching: '观察中' }
export function describeAction(h: HandlingAction) {
  const label = actionName[h.action] || h.action
  if (h.action === 'assign' || h.action === 'unassign') return `${label}：${h.previous_assignee || '未分配'} → ${h.assignee || '未分配'}`
  if (h.action === 'progress') return `${label}：${progressName[h.previous_status || 'open']} → ${progressName[h.status || 'open']}`
  return label
}
export const formatTime = (t: number) => t > 0 ? new Date(t * 1000).toLocaleString() : '暂无样本'
export const deliveryText = (d: NonNullable<Problem['delivery']>) => {
  const outcome = d.outcome === 'accepted' ? '通道已接受' : d.outcome === 'failed' ? '通道失败' : d.outcome === 'suppressed' ? '已抑制' : d.outcome
  return outcome + (d.reason ? `（${d.reason}）` : '')
}
export function problemAge(at: number, now: number) {
  if (!at) return '时间未知'
  const seconds = Math.max(0, now - at)
  return seconds < 60 ? `${Math.floor(seconds)} 秒` : seconds < 3600 ? `${Math.floor(seconds / 60)} 分钟`
    : seconds < 86400 ? `${Math.floor(seconds / 3600)} 小时` : `${Math.floor(seconds / 86400)} 天`
}
