import type { ServiceReport, AvailabilityRow, HandlingHistoryRecord } from '../../../api/operations/types'
export interface ServiceHandover {
  schema: string; service: string; hostname: string; captured_at: number; completed_at: number; config_revision: string
  report: ServiceReport; availability: AvailabilityRow[]
  alarms: { name: string; chart: string; status: string; updated: number; value: number | null; units: string }[]
  handling_history: HandlingHistoryRecord[]; history_matched: number; history_limit: number; history_snapshot: string
  history_capacity: number; actions_per_record: number; history_persistent: boolean; notes: string[]
}
const escape = (value: unknown) => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]!))
const time = (value: number) => new Date(value * 1000).toISOString()
const percentage = (value: number | null) => value == null ? '数据不足' : `${value.toFixed(2)}%`
function table(headers: string[], rows: unknown[][]) {
  return `<div class="table"><table><thead><tr>${headers.map(h => `<th>${escape(h)}</th>`).join('')}</tr></thead><tbody>${rows.map(row => `<tr>${row.map((v, i) => `<td data-label="${escape(headers[i])}"><span>${escape(v)}</span></td>`).join('')}</tr>`).join('')}</tbody></table></div>`
}
/** A script-free, self-contained artifact. Every data field is escaped as text. */
export function handoverHTML(r: ServiceHandover): string {
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"><title>${escape(r.service)} · 服务交接报告</title><style>
body{font:15px/1.65 system-ui,sans-serif;color:#172334;background:#f1f5f9;margin:0}main{max-width:1100px;margin:auto;padding:32px;background:white}h1{font-size:28px}h2{margin-top:32px;font-size:20px}p,li,td{overflow-wrap:anywhere;white-space:pre-wrap}.meta{color:#526173}.table{overflow:auto}table{border-collapse:collapse;width:100%;font-size:13px}th,td{text-align:left;vertical-align:top;padding:10px;border-bottom:1px solid #cbd5e1}th{background:#eef2f6}.record{padding:16px;border:1px solid #cbd5e1;border-radius:8px;margin:12px 0}.notice{border-left:4px solid #d97706;padding:12px;background:#fffbeb}@media print{body{background:white}main{padding:0}.table{overflow:visible}thead{display:table-header-group}tr{break-inside:avoid}h2{break-after:avoid}}@media screen and (max-width:600px){main{padding:16px}h1{font-size:23px}table,tbody{display:block}thead{position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%)}tr{display:block;border:1px solid #cbd5e1;border-radius:8px;padding:10px;margin-bottom:12px}td{display:grid;grid-template-columns:100px minmax(0,1fr);gap:10px;border:0;padding:6px}td::before{content:attr(data-label);color:#526173;font-size:12px}td span{min-width:0;overflow-wrap:anywhere}}
</style></head><body><main><h1>${escape(r.service)} · 服务交接报告</h1>
<p class="meta">本机：${escape(r.hostname)}<br>统计窗口：${time(r.report.from)} — ${time(r.report.to)}<br>读取区间：${time(r.captured_at)} — ${time(r.completed_at)}<br>服务配置版本：${escape(r.config_revision)}<br>报告结构：${escape(r.schema)}</p>
<div class="notice"><strong>统计口径与分享范围</strong><ul>${r.notes.map(n => `<li>${escape(n)}</li>`).join('')}</ul></div>
<h2>服务与依赖状态</h2>${table(['服务','当前状态','未告警占比','关联告警','依赖'],r.report.services.map(s=>[s.name,s.status,percentage(s.sla),(s.alarms||[]).join('、'),(s.children||[]).join('、')]))}
<h2>当前告警输入</h2>${r.alarms.length ? table(['告警','图表','状态','值','最后求值 (UTC)'],r.alarms.map(a=>[a.name,a.chart,a.status,a.value == null ? '无有效值' : `${a.value.toLocaleString('zh-CN', { maximumFractionDigits: 3 })} ${a.units}`,a.updated ? time(a.updated) : '未求值'])) : '<p>没有匹配的告警输入，不能据此认为服务正常。</p>'}
<h2>告警历史覆盖</h2>${r.availability.length ? table(['告警','图表','未告警占比','已知告警秒数','已知 / 窗口秒数','未知秒数'],r.availability.map(a=>[a.name,a.chart,percentage(a.uptime),a.raised_seconds,`${a.known_seconds ?? '未知'} / ${a.window_seconds}`,a.unknown_seconds ?? '未知'])) : '<p>没有可统计的告警实例。</p>'}
<h2>处理与交接记录</h2><p>匹配 ${r.history_matched} 条，展示 ${r.handling_history.length} 条，上限 ${r.history_limit} 条。存储容量 ${r.history_capacity} 条，每条保留最多 ${r.actions_per_record} 次操作。${r.history_persistent ? '服务端持久化已启用。' : '仅内存保存，重启可能丢失。'}</p><p class="meta">处理记录快照：${escape(r.history_snapshot)}</p>
${r.handling_history.length ? r.handling_history.map(h=>`<article class="record"><h3>${escape(h.problem.name)} · ${escape(h.problem.chart)}</h3><p>责任人：${escape(h.assignee || '未分配')} · 进度：${escape(h.status || '待处理')} · ${h.acknowledged ? '已确认' : '未确认'} · 版本 ${h.revision}</p><p>${h.history_truncated ? '此条操作历史已裁剪，以下不是完整记录。' : '以下为保留的操作记录。'}</p>${table(['操作时间 (UTC)','操作者','操作','备注'],h.history.map(a=>[time(a.at),a.actor,a.action,a.note]))}</article>`).join('') : '<p>窗口内没有匹配的已保存处理记录；不等于没有问题或已处理完成。</p>'}
</main></body></html>`
}
