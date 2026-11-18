import type { Info, Chart } from '../../../api/monitoring/types'
import type { NotificationSnapshot, ServiceReport } from '../../../api/operations/types'
import type { AlarmsResponse } from '../../../api/alarms/types'
export type CheckState = 'ready' | 'attention' | 'unknown'
export interface Check { id: string; title: string; state: CheckState; detail: string; action: 'charts' | 'config' | 'rules' | 'notifications' | 'services' }
export interface Evidence { info?: Info; charts?: Chart[]; alarms?: AlarmsResponse; notifications?: NotificationSnapshot; services?: ServiceReport }
export function readiness(e: Evidence, now: number): Check[] {
  const checks: Check[] = []
  const add = (id: string, title: string, action: Check['action'], state: CheckState, detail: string) => checks.push({ id, title, action, state, detail })
  const charts = e.charts
  if (!charts || !e.info) add('collection', '1. 确认指标采集', 'charts', 'unknown', '未能读取完整的本机采集状态，请重试。')
  else {
    const fresh = charts.filter(c => c.last_entry > 0 && c.last_entry <= now + 5 && now - c.last_entry <= Math.max(10, 3 * c.update_every)).length
    const failed = e.info.collectors.filter(c => c.enabled && !!c.error).length
    add('collection', '1. 确认指标采集', 'charts', fresh > 0 && fresh === charts.length && !failed ? 'ready' : 'attention', `${charts.length} 个图表，${fresh} 个近期有样本，${failed} 个采集器报告错误。打开图表核对所需指标；有样本不代表全部业务已覆盖。`)
  }
  const p = e.info?.db?.persistence
  if (!p) add('storage', '2. 检查历史保存', 'config', 'unknown', '服务端未提供历史保存状态。')
  else if (p.error) add('storage', '2. 检查历史保存', 'config', 'attention', '最近一次保存失败，请检查数据目录权限、容量和服务日志。')
  else {
    const fresh = p.finished > 0 && p.finished <= now + 5 && now - p.finished <= Math.max(60, 3 * p.interval_seconds)
    add('storage', '2. 检查历史保存', 'config', fresh ? 'ready' : 'attention', fresh ? `近期完成历史保存：${new Date(p.finished * 1000).toLocaleString()}。重启恢复和备份仍需单独验证。` : '尚无近期保存成功记录。等待检查点完成，或检查保存配置；这不等于历史已安全落盘。')
  }
  const n = e.notifications, alarms = e.alarms
  if (!n || !alarms) add('alarms', '3. 核对告警覆盖', 'rules', 'unknown', '未能读取告警引擎或当前告警，请重试。')
  else {
    const active = Object.values(alarms.alarms).filter(a => a.active)
    const valid = active.filter(a => ['CLEAR', 'WARNING', 'CRITICAL'].includes(a.status) && a.last_updated > 0 && a.last_updated <= now + 5 && now - a.last_updated <= Math.max(10, 3 * a.update_every)).length
    add('alarms', '3. 核对告警覆盖', 'rules', n.available && n.enabled && !n.closed && valid > 0 && valid === active.length ? 'ready' : 'attention', !n.available || !n.enabled || n.closed ? '本机告警引擎未运行或已暂停，请联系管理员检查配置。' : `${active.length} 个活动告警，${valid} 个近期得到有效求值。请核对规则匹配范围；零告警不表示已经受到保护。`)
  }
  if (!n) add('notifications', '4. 验证通知通道', 'notifications', 'unknown', '通知状态读取失败，不能确认配置或发送结果。')
  else {
    const channels = n.channels.filter(c => c.configured_count > 0)
    const accepted = channels.filter(c => c.last_accepted > 0 && c.last_accepted > c.last_failed).length
    add('notifications', '4. 验证通知通道', 'notifications', channels.length > 0 && channels.length === accepted && !n.closed ? 'ready' : 'attention', channels.length ? `${channels.length} 个已配置通道，${accepted} 个最近结果为通道接受。请手动发送测试并核对收件；通道接受不代表用户已收件，也不验证真实告警路由。记录仅限本次启动。` : '尚未配置通知通道。请先配置收件通道，再手动发送测试并核对收件。')
  }
  const services = e.services?.services
  add('services', '5. 建立业务服务视图（可选）', 'services', !services ? 'unknown' : services.length && services.every(s => s.status !== 'UNKNOWN') ? 'ready' : 'attention', !services ? '服务目录读取失败。' : services.length ? `${services.length} 个服务；请核对告警关联与依赖，未知状态表示缺少有效输入。` : '将基础指标关联到业务服务，方便按业务影响定位问题。未配置不影响基础采集。')
  return checks
}
