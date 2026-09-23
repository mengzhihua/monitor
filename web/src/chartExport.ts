import type { Chart } from './api'
function textCell(value: string): string {
  // Spreadsheet programs must treat metric names/IDs as text, never formulas.
  const safe = /^[\s]*[=+@-]/.test(value) ? `'${value}` : value
  return `"${safe.replaceAll('"', '""')}"`
}
/** Export the loaded raw series, never uPlot's cumulative stacked arrays. */
export function chartCSV(chart: Chart, node: string, times: number[], ids: string[], values: (number | null)[][]): string {
  const header = ['节点 ID', '图表 ID', '时间 (UTC)', 'Unix 时间 (秒)', ...ids.map(id => `${chart.dimensions.find(d => d.id === id)?.name || id} [${id}] (${chart.units})`)]
  const rows = times.flatMap((t, index) => {
    const date = new Date(t * 1000)
    if (!Number.isFinite(t) || !Number.isFinite(date.getTime())) return []
    return [[textCell(node), textCell(chart.id), textCell(date.toISOString()), String(t), ...ids.map((_, di) => {
      const value = values[di]?.[index]
      return value != null && Number.isFinite(value) ? String(value) : ''
    })].join(',')]
  })
  return '\uFEFF' + [header.map(textCell).join(','), ...rows].join('\r\n') + '\r\n'
}
