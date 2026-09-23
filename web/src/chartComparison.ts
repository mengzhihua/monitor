import type { Chart, DataResponse } from './api'
import { summarizeSeries } from './chartInspection'
import { textCell } from './chartExport'

export type ComparisonSeries = ReturnType<typeof summarizeSeries> & { id: string; name: string; help: string }
export function compareSeries(current: ComparisonSeries[], previous: DataResponse) {
  const columns = new Map(previous.dimension_ids.map((id, i) => [id, i+1]))
  return current.map(series => {
    const column = columns.get(series.id)
    const prior = summarizeSeries(previous.result.data.map(row => column === undefined ? null : row[column] ?? null))
    let delta: number | null = null, percent: number | null = null, reason = ''
    if (series.mean === null) reason = '本段无有效采样'
    else if (prior.mean === null) reason = '前段无有效采样'
    else {
      const difference = series.mean-prior.mean
      if (Number.isFinite(difference)) delta = difference
      else reason = '差值超出数值范围'
      if (delta !== null) {
        if (prior.mean <= 0) reason = '前段均值为 0 或负数，仅比较差值'
        else {
          const relative = delta/prior.mean*100
          if (Number.isFinite(relative)) percent = relative
          else reason = '变化率超出数值范围'
        }
      }
    }
    return { ...series, prior, delta, percent, reason }
  })
}
export function comparisonCSV(chart: Chart, node: string, end: number, duration: number, rows: ReturnType<typeof compareSeries>) {
  const header = ['节点 ID','图表 ID','单位','本段开始 UTC','本段结束 UTC','前段开始 UTC','前段结束 UTC','维度 ID','维度名称','本段均值','前段均值','均值差值','相对变化率 (%)','本段非空采样','本段总行数','前段非空采样','前段总行数','说明']
  const time = (t: number) => textCell(new Date(t*1000).toISOString())
  const number = (n: number | null) => n !== null && Number.isFinite(n) ? String(n) : ''
  return '\uFEFF' + [header.map(textCell).join(','), ...rows.map(row => [
    textCell(node), textCell(chart.id), textCell(chart.units), time(end-duration), time(end), time(end-duration*2), time(end-duration), textCell(row.id), textCell(row.name),
    number(row.mean), number(row.prior.mean), number(row.delta), number(row.percent), row.count, row.total, row.prior.count, row.prior.total, textCell(row.reason),
  ].join(','))].join('\r\n') + '\r\n'
}
