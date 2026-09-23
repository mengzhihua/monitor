<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import type { Chart } from '../api'
import { api } from '../api'
import { live } from '../live'
import { pageVisible } from '../visibility'
import { metricHelp, dimensionHelp, unitHelp } from '../metricHelp'
import { activateChart, retainChart } from '../chart_cache'

const props = defineProps<{ chart: Chart; window: number }>()

const help = computed(() => metricHelp(props.chart))
const card = ref<HTMLDivElement>()
const el = ref<HTMLDivElement>()
const error = ref('')
const clock = ref(Date.now()/1000)
const lastSample = ref(props.chart.last_entry || 0)
const stale = computed(() => !lastSample.value || clock.value-lastSample.value > Math.max(15, 3*step()))
let loadGeneration = 0
let disposed = false
let refreshTimer: number | undefined
let visible = false
let inViewport = false
let request: AbortController | undefined
let io: IntersectionObserver | null = null
const anomaly = ref<Record<string, number>>({})
const anomalous = computed(() => Object.values(anomaly.value).some((v) => v >= 50))

let plot: uPlot | null = null
let plotDefinition = ''
let unsub: (() => void) | null = null
let times: number[] = []
let raw: (number | null)[][] = []
let dims: string[] = []
let anomBits: number[] = []
let pending = false
let renderFrame: number | undefined

const palette = ['#3b82f6', '#22c55e', '#f59e0b', '#ef4444', '#a855f7', '#06b6d4', '#ec4899', '#84cc16', '#f97316', '#64748b']

function visibleDims() {
  return props.chart.dimensions.filter((d) => !d.hidden).map((d) => d.id)
}

function fmt(v: number | null | undefined) {
  if (v == null || Number.isNaN(v)) return '-'
  const a = Math.abs(v)
  if (a >= 1e6) return (v / 1e6).toFixed(2) + 'M'
  if (a >= 1e4) return (v / 1e3).toFixed(1) + 'k'
  if (a >= 100) return v.toFixed(0)
  if (a >= 10) return v.toFixed(1)
  return v.toFixed(2)
}

const stacked = () => props.chart.chart_type === 'stacked'
/** Collection period in seconds; charts like system.load sample every 5s. */
const step = () => Math.max(1, props.chart.update_every || 1)

/** Series index in uPlot → dimension index. Stacked charts are drawn as
 *  cumulative sums in reverse order so the largest area is painted first. */
function dimIndex(sidx: number) {
  return stacked() ? dims.length - sidx : sidx - 1
}

function buildData(): uPlot.AlignedData {
  if (!stacked()) return [times, ...raw] as uPlot.AlignedData
  const acc = new Array<number>(times.length).fill(0)
  const out: (number | null)[][] = []
  for (const s of raw) {
    out.push(s.map((v, i) => (v == null ? null : (acc[i] += v))))
  }
  out.reverse()
  return [times, ...out] as uPlot.AlignedData
}

function makeOpts(width: number): uPlot.Options {
  const st = stacked()
  const series: uPlot.Series[] = [
    { value: (_u, v) => (v == null ? '-' : new Date(v * 1000).toLocaleTimeString()) },
  ]
  for (let sidx = 1; sidx <= dims.length; sidx++) {
    const di = dimIndex(sidx)
    const id = dims[di]!
    const dim = props.chart.dimensions.find((d) => d.id === id)
    const anomalous = (anomaly.value[id] ?? 0) >= 50 || !!(dim?.anomaly || anomBits[di])
    const color = anomalous ? '#ef4444' : palette[di % palette.length]!
    const strokeWidth = anomalous ? 2 : 1
    series.push({
      label: (dim?.name ?? id) + (anomalous ? ' ⚠' : ''),
      stroke: color,
      width: strokeWidth,
      fill: st || props.chart.chart_type === 'area' ? color + (st ? 'cc' : '33') : undefined,
      spanGaps: false,
      value: (_u, _v, s, idx) => (idx == null ? '-' : fmt(raw[dimIndex(s)]?.[idx])),
      points: {
        show: true,
        size: 5,
        filter: (_u, si) => {
          const values = raw[dimIndex(si)] ?? []
          const isolated: number[] = []
          values.forEach((value, index) => {
            if (value != null && values[index-1] == null && values[index+1] == null) isolated.push(index)
          })
          return isolated
        },
      },
    })
  }
  return {
    width,
    height: 180,
    series,
    legend: { show: true, live: true },
    cursor: { drag: { x: false, y: false }, y: false },
    scales: { x: { time: true }, y: { range: (_u, min, max) => [Math.min(0, min), max <= 0 ? 1 : max * 1.05] } },
    axes: [
      { stroke: '#94a3b8', grid: { stroke: '#1e293b' }, ticks: { stroke: '#1e293b' } },
      {
        stroke: '#94a3b8',
        grid: { stroke: '#1e293b' },
        ticks: { stroke: '#1e293b' },
        size: 56,
        values: (_u, vals) => vals.map((v) => fmt(v)),
      },
    ],
  }
}

async function load(first = false) {
  if (!visible || disposed) return
  clearTimeout(refreshTimer)
  request?.abort()
  const current = request = new AbortController()
  const generation = ++loadGeneration
  error.value = ''
  const requestedDims = visibleDims()
  try {
    // One bucket per collection period, otherwise slow charts come back as
    // mostly-null 1s rows and uPlot draws nothing between isolated samples.
    const d = await api.data(props.chart.id, -props.window, 0, Math.min(1200, Math.ceil(props.window / step())), current.signal)
    if (disposed || current.signal.aborted || generation !== loadGeneration) return
    dims = requestedDims
    times = d.result.data.map((r) => r[0] as number)
    const idx = new Map(d.dimension_ids.map((id, i) => [id, i + 1]))
    const bits: Record<string, number> = {}
    ;(d.dimension_anomaly ?? []).forEach((v, i) => {
      const id = d.dimension_ids[i]
      if (id) bits[id] = v
    })
    anomaly.value = bits
    anomBits = dims.map((id) => {
      const col = d.dimension_ids.indexOf(id)
      if (col >= 0 && ((d.dimension_anomaly && d.dimension_anomaly[col] >= 50) || (d.anomaly && d.anomaly[col]))) return 1
      return props.chart.dimensions.find((x) => x.id === id)?.anomaly ? 1 : 0
    })
    raw = dims.map((id) => {
      const col = idx.get(id)
      return d.result.data.map((r) => (col == null ? null : (r[col] as number | null)))
    })
  } catch (e) {
    if (disposed || current.signal.aborted || generation !== loadGeneration) return
    error.value = String(e)
  }
  request = undefined
  render()
  scheduleRefresh(first)
}

function render() {
  if (!el.value) return
  const width = el.value.clientWidth || 600
  const definition = [defFingerprint(), ...dims, ...anomBits].join('\u0001')
  if (plot && plotDefinition === definition) {
    // A resize can happen while hidden, when ResizeObserver deliberately skips
    // canvas work. Catch up before reusing a cached plot in the foreground.
    if (plot.width !== width) plot.setSize({ width, height: 180 })
    plot.setData(buildData())
    return
  }
  plot?.destroy()
  plot = new uPlot(makeOpts(width), buildData(), el.value)
  plotDefinition = definition
  // uPlot owns legend DOM; decorate it after each recreation, in its actual
  // series order (the stacked renderer reverses dimensions).
  el.value.querySelectorAll<HTMLElement>('.u-legend .u-series').forEach((row, index) => {
    const dim = props.chart.dimensions.find(d => d.id === dims[dimIndex(index)])
    const text = index === 0 ? '采样时间：鼠标选中位置对应的本地时间。' : dim ? dimensionHelp(props.chart, dim) : help.value
    row.title = text
    row.querySelectorAll<HTMLElement>('th, td').forEach(cell => { cell.title = text })
  })
}

function onLive(t: number, v: Record<string, number>) {
  lastSample.value = Math.max(lastSample.value, t)
  if (props.window > 1200) return // long windows refresh downsampled history; never grow per-second arrays
  if (times.length && t <= times[times.length - 1]!) return
  // a missed collection period becomes a single null so uPlot breaks the line
  if (times.length && t - times[times.length - 1]! > 2 * step()) {
    times.push(t - step())
    raw.forEach((r) => r.push(null))
  }
  times.push(t)
  dims.forEach((id, i) => raw[i]!.push(id in v ? v[id]! : null))
  const cutoff = t - props.window
  while (times.length && (times[0]! < cutoff || times.length > 1200)) {
    times.shift()
    raw.forEach((r) => r.shift())
  }
  if (!pending) {
    pending = true
    renderFrame = requestAnimationFrame(() => {
      pending = false
      renderFrame = undefined
      if (!disposed) plot?.setData(buildData())
    })
  }
}

let ro: ResizeObserver | null = null

/** Spread periodic history queries across 30 seconds instead of bursting all charts at once. */
function refreshOffset(id: string) {
  let hash = 2166136261
  for (let i = 0; i < id.length; i++) hash = Math.imul(hash ^ id.charCodeAt(i), 16777619)
  return 1000 + (hash >>> 0) % 29000
}

function scheduleRefresh(first: boolean) {
  clearTimeout(refreshTimer)
  if (!visible || disposed) return
  refreshTimer = window.setTimeout(() => {
    clock.value = Date.now() / 1000
    void load()
  }, first ? refreshOffset(props.chart.id) : 30000)
}

function releasePlot() {
  // Preserve the placeholder height, including the legend, when evicting.
  if (plot && el.value) el.value.style.minHeight = `${el.value.clientHeight}px`
  plot?.destroy()
  plot = null
  plotDefinition = ''
  times = []; raw = []; dims = []; anomBits = []
  anomaly.value = {}
}

function updateSubscription() {
  unsub?.()
  unsub = visible && props.window <= 1200
    ? live.subscribe(props.chart.id, (m) => onLive(m.t, m.v)) : null
}

function updateVisibility() {
  const active = inViewport && pageVisible.value
  if (active === visible) return
  visible = active
  updateSubscription()
  if (active) {
    activateChart(releasePlot)
    clock.value = Date.now() / 1000
    void load(true)
  } else {
    ++loadGeneration
    request?.abort()
    request = undefined
    clearTimeout(refreshTimer)
    if (renderFrame !== undefined) cancelAnimationFrame(renderFrame)
    renderFrame = undefined
    pending = false
    if (plot) retainChart(releasePlot)
  }
}

onMounted(() => {
  io = new IntersectionObserver((entries) => {
    inViewport = entries.some((entry) => entry.isIntersecting)
    updateVisibility()
  }, { rootMargin: '300px 0px' })
  io.observe(card.value!)
  ro = new ResizeObserver(() => { if (visible && plot && el.value) plot.setSize({ width: el.value.clientWidth, height: 180 }) })
  ro.observe(el.value!)
})
onBeforeUnmount(() => {
  disposed = true
  ++loadGeneration
  request?.abort()
  clearTimeout(refreshTimer)
  if (renderFrame !== undefined) cancelAnimationFrame(renderFrame)
  io?.disconnect()
  unsub?.()
  ro?.disconnect()
  activateChart(releasePlot)
  releasePlot()
})
watch(pageVisible, updateVisibility)
watch(() => props.chart.last_entry, (t) => { lastSample.value = Math.max(lastSample.value, t || 0) })
/** Anything that feeds makeOpts/buildData/load: a changed definition needs a full reload. */
const defFingerprint = () =>
  [props.chart.id, props.chart.context, props.chart.title, props.chart.units, props.chart.chart_type, props.chart.update_every, props.chart.anomaly ? 1 : 0, ...props.chart.dimensions.map((d) => `${d.id}\u0000${d.name}\u0000${d.hidden ? 1 : 0}\u0000${d.anomaly ? 1 : 0}`)].join('\u0001')
watch([() => props.window, defFingerprint], () => {
  updateSubscription()
  if (visible) void load(); else ++loadGeneration
})
</script>

<template>
  <div ref="card" class="card" :class="{ anom: chart.anomaly }">
    <div class="head">
      <div>
        <span class="title" :title="help">{{ chart.title }}</span>
        <span class="id" :title="help">{{ chart.id }}</span>
        <span v-if="anomalous || chart.anomaly" class="anom">ANOM</span>
      </div>
      <span v-if="stale" class="err" :title="lastSample ? new Date(lastSample * 1000).toLocaleString() : '尚无样本'">{{ lastSample ? '数据过期' : '暂无数据' }}</span>
      <span class="units" :title="unitHelp(chart.units)">{{ chart.units }}</span>
    </div>
    <details class="metric-help">
      <summary :title="help">中文指标说明</summary>
      <p>{{ help }}</p>
      <ul><li v-for="dim in chart.dimensions.filter(d => !d.hidden)" :key="dim.id">{{ dimensionHelp(chart, dim) }}</li></ul>
    </details>
    <div ref="el" class="plot"></div>
    <div v-if="error" class="err">{{ error }}</div>
  </div>
</template>

<style scoped>
.card { background: #0f172a; border: 1px solid #1e293b; border-radius: 8px; padding: 10px 12px; min-width: 0; }
.card.anom { border-color: #7f1d1d; box-shadow: inset 0 0 0 1px #7f1d1d; }
.badge { margin-left: 8px; font-size: 10px; color: #fecaca; background: #7f1d1d; border-radius: 8px; padding: 0 6px; }
.head { display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 4px; }
.metric-help { color:#94a3b8; font-size:12px; margin:6px 0; overflow-wrap:anywhere; }
.metric-help summary { cursor:help; width:fit-content; color:#5eead4; }
.metric-help p, .metric-help li { white-space:pre-line; line-height:1.7; }
.metric-help ul { padding-left:18px; }
.title, .units, .id, :deep(.u-series) { cursor:help; }
.head { flex-wrap:wrap; gap:6px; overflow-wrap:anywhere; }
.title { font-weight: 600; font-size: 14px; }
.id { color: #64748b; font-size: 11px; margin-left: 8px; font-family: ui-monospace, monospace; }
.anom { margin-left: 8px; font-size: 10px; font-weight: 700; color: #fecaca; background: #7f1d1d; border-radius: 4px; padding: 1px 6px; letter-spacing: 0.04em; }
.units { color: #94a3b8; font-size: 12px; }
.plot { width: 100%; min-height: 180px; }
.err { color: #f87171; font-size: 12px; margin-top: 4px; }
:deep(.u-legend) { font-size: 11px; color: #cbd5e1; }
:deep(.u-legend .u-value) { font-family: ui-monospace, monospace; }
</style>
