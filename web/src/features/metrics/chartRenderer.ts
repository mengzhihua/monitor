import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import type { Chart } from '../../api/monitoring/types'
import { dimensionHelp, metricHelp } from '../../metricHelp'
import { chartDefinition, formatSample, pieSlices, plotSeries, type ChartSeries } from './model/chartSeries'

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
  let pie = false
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

  function drawPie() {
    const element = source.element()
    if (!element) return
    plot?.destroy()
    plot = null
    definition = ''
    element.replaceChildren()
    const chart = source.chart()
    const snapshot = source.series()
    const names: Record<string, string> = {}
    for (const dimension of chart.dimensions) names[dimension.id] = dimension.name || dimension.id
    const slices = pieSlices(snapshot.dimensions, snapshot.values, names)
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
    svg.setAttribute('viewBox', '0 0 200 160')
    svg.setAttribute('role', 'img')
    svg.style.width = '100%'
    svg.style.height = '160px'
    const cx = 100, cy = 78, radius = 58
    if (slices.length === 0) {
      svg.setAttribute('aria-label', '饼图没有可计入的末行正值')
      const text = document.createElementNS('http://www.w3.org/2000/svg', 'text')
      text.setAttribute('x', '100')
      text.setAttribute('y', '84')
      text.setAttribute('text-anchor', 'middle')
      text.setAttribute('fill', '#94a3b8')
      text.setAttribute('font-size', '12')
      text.textContent = '没有可计入的正值'
      svg.append(text)
    } else if (slices.length === 1) {
      const slice = slices[0]!
      svg.setAttribute('aria-label', `饼图：${slice.name} ${formatSample(slice.value)}，100%`)
      const circle = document.createElementNS('http://www.w3.org/2000/svg', 'circle')
      circle.setAttribute('cx', String(cx))
      circle.setAttribute('cy', String(cy))
      circle.setAttribute('r', String(radius))
      circle.setAttribute('fill', palette[0]!)
      const title = document.createElementNS('http://www.w3.org/2000/svg', 'title')
      title.textContent = `${slice.name} ${formatSample(slice.value)}`
      circle.append(title)
      svg.append(circle)
    } else {
      let angle = -Math.PI / 2
      svg.setAttribute('aria-label', `饼图：${slices.map(slice => `${slice.name} ${formatSample(slice.value)}，${Math.round(slice.fraction * 100)}%`).join('；')}`)
      slices.forEach((slice, index) => {
        const next = angle + slice.fraction * Math.PI * 2
        const path = document.createElementNS('http://www.w3.org/2000/svg', 'path')
        const large = next - angle > Math.PI ? 1 : 0
        const x1 = cx + radius * Math.cos(angle)
        const y1 = cy + radius * Math.sin(angle)
        const x2 = cx + radius * Math.cos(next)
        const y2 = cy + radius * Math.sin(next)
        path.setAttribute('d', `M ${cx} ${cy} L ${x1} ${y1} A ${radius} ${radius} 0 ${large} 1 ${x2} ${y2} Z`)
        path.setAttribute('fill', palette[index % palette.length]!)
        const title = document.createElementNS('http://www.w3.org/2000/svg', 'title')
        title.textContent = `${slice.name} ${formatSample(slice.value)}`
        path.append(title)
        svg.append(path)
        angle = next
      })
    }
    const legend = document.createElement('ul')
    legend.style.cssText = 'display:flex;flex-wrap:wrap;gap:8px 12px;margin:8px 0 0;padding:0;list-style:none;color:#cbd5e1;font-size:12px;'
    slices.forEach((slice, index) => {
      const item = document.createElement('li')
      item.style.cssText = 'display:inline-flex;align-items:center;gap:6px;min-height:24px;'
      const swatch = document.createElement('span')
      swatch.style.cssText = `width:10px;height:10px;background:${palette[index % palette.length]};display:inline-block;`
      item.append(swatch, document.createTextNode(`${slice.name} ${formatSample(slice.value)} ${Math.round(slice.fraction * 100)}%`))
      legend.append(item)
    })
    element.append(svg, legend)
    pie = true
  }

  function render() {
    const element = source.element()
    if (!element) return
    if (source.chart().chart_type === 'pie') {
      drawPie()
      return
    }
    if (pie) {
      element.replaceChildren()
      pie = false
    }
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
    if ((plot || pie) && element) element.style.minHeight = `${element.clientHeight}px`
    plot?.destroy()
    plot = null
    definition = ''
    pie = false
    element?.replaceChildren()
  }

  return {
    render, release,
    exists: () => plot !== null || pie,
    update: () => {
      if (source.chart().chart_type === 'pie') { drawPie(); return }
      plot?.setData(data())
    },
    resize: () => {
      const element = source.element()
      if (plot && element) plot.setSize({ width: element.clientWidth, height: source.height() })
    },
  }
}
