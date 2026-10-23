import type { Chart, DataResponse } from '../../../api/monitoring/types'

/** One accepted history response and its bounded live tail, always in raw units. */
export interface ChartSeries {
  node: string
  times: number[]
  dimensions: string[]
  values: (number | null)[][]
  anomaly: Record<string, number>
  anomalyBits: number[]
  sampleStep: number | null
}

export function emptySeries(): ChartSeries {
  return { node: '', times: [], dimensions: [], values: [], anomaly: {}, anomalyBits: [], sampleStep: null }
}

export const collectionStep = (chart: Chart) => Math.max(1, chart.update_every || 1)

export function historySeries(chart: Chart, response: DataResponse, node: string): ChartSeries {
  if (response.units !== chart.units) throw new Error('采样单位与图表定义不一致，请稍后重试或刷新图表。')
  const dimensions = chart.dimensions.filter(dimension => !dimension.hidden).map(dimension => dimension.id)
  const columns = new Map(response.dimension_ids.map((id, index) => [id, index + 1]))
  const anomaly: Record<string, number> = {}
  ;(response.dimension_anomaly ?? []).forEach((value, index) => {
    const id = response.dimension_ids[index]
    if (id) anomaly[id] = value
  })
  return {
    node, dimensions, anomaly,
    times: response.result.data.map(row => row[0] as number),
    values: dimensions.map(id => {
      const column = columns.get(id)
      return response.result.data.map(row => column == null ? null : row[column] as number | null)
    }),
    anomalyBits: dimensions.map(id => {
      const column = response.dimension_ids.indexOf(id)
      if (column >= 0 && ((response.dimension_anomaly && response.dimension_anomaly[column] >= 50)
        || (response.anomaly && response.anomaly[column]))) return 1
      return chart.dimensions.find(dimension => dimension.id === id)?.anomaly ? 1 : 0
    }),
    sampleStep: Number.isFinite(response.view_update_every) && response.view_update_every > 0 ? response.view_update_every : null,
  }
}

/** Mutate only the live tail; callers publish once after all dimensions align. */
export function appendLiveSample(series: ChartSeries, timestamp: number, values: Record<string, number>, duration: number, step: number): boolean {
  if (duration > 1200) return false // Long ranges retain downsampled history.
  const previous = series.times.at(-1)
  if (previous !== undefined && timestamp <= previous) return false
  // A missed collection period becomes a single null, never a fabricated zero.
  if (previous !== undefined && timestamp - previous > 2 * step) {
    series.times.push(timestamp - step)
    series.values.forEach(row => row.push(null))
  }
  series.times.push(timestamp)
  series.dimensions.forEach((id, index) => series.values[index]!.push(id in values ? values[id]! : null))
  const cutoff = timestamp - duration
  while (series.times.length && (series.times[0]! < cutoff || series.times.length > 1200)) {
    series.times.shift()
    series.values.forEach(row => row.shift())
  }
  return true
}

/** Stacking is a rendering projection; summaries and exports keep raw samples. */
export function plotSeries(series: ChartSeries, stacked: boolean): (number | null)[][] {
  if (!stacked) return [series.times, ...series.values]
  const totals = new Array<number>(series.times.length).fill(0)
  const values = series.values.map(row => row.map((value, index) => value == null ? null : totals[index] += value))
  return [series.times, ...values.reverse()]
}

export function chartDefinition(chart: Chart, historical: boolean): string {
  return [chart.id, chart.context, chart.title, chart.units, chart.chart_type, chart.update_every,
    !historical && chart.anomaly ? 1 : 0,
    ...chart.dimensions.map(dimension => `${dimension.id}\u0000${dimension.name}\u0000${dimension.hidden ? 1 : 0}\u0000${!historical && dimension.anomaly ? 1 : 0}`),
  ].join('\u0001')
}

export function formatSample(value: number | null | undefined): string {
  if (value == null || Number.isNaN(value)) return '-'
  const absolute = Math.abs(value)
  if (absolute >= 1e6) return (value / 1e6).toFixed(2) + 'M'
  if (absolute >= 1e4) return (value / 1e3).toFixed(1) + 'k'
  if (absolute >= 100) return value.toFixed(0)
  if (absolute >= 10) return value.toFixed(1)
  return value.toFixed(2)
}
