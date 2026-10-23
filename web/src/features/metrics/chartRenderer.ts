import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import type { Chart } from '../../api/monitoring/types'
import { dimensionHelp, metricHelp } from '../../metricHelp'
import { chartDefinition, formatSample, plotSeries, type ChartSeries } from './model/chartSeries'

interface PlotSource {
  element: () => HTMLDivElement | undefined
  chart: () => Chart
  series: () => ChartSeries
  height: () => number
  historical: () => boolean
}

const palette = ['#3b82f6', '#22c55e', '#f59e0b', '#ef4444', '#a855f7', '#06b6d4', '#ec4899', '#84cc16', '#f97316', '#64748b']

/** Owns only uPlot/DOM resources. Requests, subscriptions and snapshots live elsewhere. */
export function createChartRenderer(source: PlotSource) {
  let plot: uPlot | null = null
  let definition = ''
  const stacked = () => source.chart().chart_type === 'stacked'
  // Stacked sums are drawn in reverse order so the largest area is painted first.
  const dimensionIndex = (index: number) => stacked() ? source.series().dimensions.length - index : index - 1
  const data = () => plotSeries(source.series(), stacked()) as uPlot.AlignedData

  function options(width: number): uPlot.Options {
    const chart = source.chart()
    const snapshot = source.series()
    const st = stacked()
    const series: uPlot.Series[] = [
      { value: (_u, value) => value == null ? '-' : new Date(value * 1000).toLocaleTimeString() },
    ]
    for (let index = 1; index <= snapshot.dimensions.length; index++) {
      const di = dimensionIndex(index)
      const id = snapshot.dimensions[di]!
      const dimension = chart.dimensions.find(dimension => dimension.id === id)
      const anomalous = !source.historical() && ((snapshot.anomaly[id] ?? 0) >= 50 || !!(dimension?.anomaly || snapshot.anomalyBits[di]))
      const color = anomalous ? '#ef4444' : palette[di % palette.length]!
      series.push({
        label: (dimension?.name ?? id) + (anomalous ? ' ⚠' : ''),
        stroke: color,
        width: anomalous ? 2 : 1,
        fill: st || chart.chart_type === 'area' ? color + (st ? 'cc' : '33') : undefined,
        spanGaps: false,
        value: (_u, _value, seriesIndex, row) => row == null ? '-' : formatSample(source.series().values[dimensionIndex(seriesIndex)]?.[row]),
        points: {
          show: true, size: 5,
          filter: (_u, seriesIndex) => {
            const values = source.series().values[dimensionIndex(seriesIndex)] ?? []
            const isolated: number[] = []
            values.forEach((value, row) => {
              if (value != null && values[row - 1] == null && values[row + 1] == null) isolated.push(row)
            })
            return isolated
          },
        },
      })
    }
    return {
      width, height: source.height(), series,
      legend: { show: true, live: true },
      cursor: { drag: { x: false, y: false }, y: false },
      scales: { x: { time: true }, y: { range: (_u, min, max) => [Math.min(0, min), max <= 0 ? 1 : max * 1.05] } },
      axes: [
        { stroke: '#94a3b8', space: 110, grid: { stroke: '#1e293b' }, ticks: { stroke: '#1e293b' } },
        { stroke: '#94a3b8', grid: { stroke: '#1e293b' }, ticks: { stroke: '#1e293b' }, size: 56,
          values: (_u, values) => values.map(value => formatSample(value)) },
      ],
    }
  }

  function render() {
    const element = source.element()
    if (!element) return
    const width = element.clientWidth || 600
    const height = source.height()
    const snapshot = source.series()
    const nextDefinition = [chartDefinition(source.chart(), source.historical()), ...snapshot.dimensions, ...snapshot.anomalyBits].join('\u0001')
    if (plot && definition === nextDefinition) {
      // Resize can happen while hidden, when observer work is suspended.
      if (plot.width !== width || plot.height !== height) plot.setSize({ width, height })
      plot.setData(data())
      return
    }
    plot?.destroy()
    plot = new uPlot(options(width), data(), element)
    definition = nextDefinition
    // uPlot owns legend DOM; annotate its actual (possibly reversed) series order.
    element.querySelectorAll<HTMLElement>('.u-legend .u-series').forEach((row, index) => {
      const chart = source.chart()
      const dimension = chart.dimensions.find(dimension => dimension.id === snapshot.dimensions[dimensionIndex(index)])
      const help = index === 0 ? '采样时间：鼠标选中位置对应的本地时间。' : dimension ? dimensionHelp(chart, dimension) : metricHelp(chart)
      row.title = help
      row.querySelectorAll<HTMLElement>('th, td').forEach(cell => { cell.title = help })
    })
  }

  function release() {
    const element = source.element()
    // Preserve the placeholder height, including the legend, when evicting.
    if (plot && element) element.style.minHeight = `${element.clientHeight}px`
    plot?.destroy()
    plot = null
    definition = ''
  }

  return {
    render, release,
    exists: () => plot !== null,
    update: () => plot?.setData(data()),
    resize: () => {
      const element = source.element()
      if (plot && element) plot.setSize({ width: element.clientWidth, height: source.height() })
    },
  }
}
