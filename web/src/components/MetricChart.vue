<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import type { Chart } from '../api'
import { api, selection } from '../api'
import { live } from '../live'
import { pageVisible } from '../visibility'
import { metricHelp, dimensionHelp, unitHelp } from '../metricHelp'
import { chartCSV } from '../chartExport'
import { historyEnd, localDateTime, summarizeSeries } from '../chartInspection'
import { activateChart, retainChart } from '../chart_cache'
import ChartComparison from './ChartComparison.vue'

const props = defineProps<{ chart: Chart; window: number; end?: number | null; detail?: boolean }>()

const dialog = ref<HTMLDialogElement>()
const zoomButton = ref<HTMLButtonElement>()
const zoomed = ref(false)
const sampleCount = ref(0)
const snapshotReady = ref(false)
const loading = ref(false)
const comparing = ref(false)
const sampleStep = ref<number | null>(null)
const anchor = ref<number | null>(props.end ?? null)
watch(anchor, value => { if (value === null) comparing.value = false })
const endInput = ref(localDateTime(props.end ?? Math.floor(Date.now()/1000)))
watch(() => props.end, end => {
  anchor.value = end ?? null
  endInput.value = localDateTime(end ?? Math.floor(Date.now()/1000))
  rangeError.value = ''
})
const rangeError = ref('')
const dataRevision = ref(0)
const summaries = computed(() => {
  void dataRevision.value
  return dims.map((id, i) => {
    const dimension = props.chart.dimensions.find(d => d.id === id)
    return { id, name: dimension?.name || id, help: dimension ? dimensionHelp(props.chart, dimension) : metricHelp(props.chart), ...summarizeSeries(raw[i] || []) }
  })
})
const rangeLabel = computed(() => anchor.value === null ? `跟随当前时间 · 最近 ${props.window / 60} 分钟`
  : `固定历史时段：${new Date((anchor.value-props.window)*1000).toLocaleString()} — ${new Date(anchor.value*1000).toLocaleString()}`)
function setHistory() {
  try { anchor.value = historyEnd(endInput.value, props.window); rangeError.value = '' }
  catch (e) { rangeError.value = (e as Error).message }
}
function moveHistory(direction: number) {
  const end = Math.min(Math.floor(Date.now()/1000), (anchor.value ?? Math.floor(Date.now()/1000)) + direction * props.window)
  endInput.value = localDateTime(end); setHistory()
}
function freezeTime() { endInput.value = localDateTime(Math.floor(Date.now()/1000)); setHistory() }
function resumeTime() { anchor.value = null; rangeError.value = ''; endInput.value = localDateTime(Math.floor(Date.now()/1000)) }
function toggleComparison() {
  if (comparing.value) { comparing.value = false; return }
  if (anchor.value === null) freezeTime()
  if (anchor.value !== null) comparing.value = true
}
let dataNode = ''
const plotHeight = () => props.detail ? Math.max(240, Math.min(560, window.innerHeight * 0.55)) : 180
async function openDetail() {
  zoomed.value = true
  updateVisibility()
  await nextTick()
  dialog.value?.showModal()
}
function closeDetail() {
  if (disposed) return
  dialog.value?.close()
  zoomed.value = false
  updateVisibility()
  zoomButton.value?.focus()
}
function exportCSV() {
  if (!snapshotReady.value || !times.length) return
  const csv = chartCSV(props.chart, dataNode, times, dims, raw)
  const url = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' }))
  const link = document.createElement('a')
  link.href = url; link.download = `${props.chart.id.replace(/[^a-zA-Z0-9._-]/g, '_').slice(0,100)}.csv`; link.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
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
    const anomalous = anchor.value === null && ((anomaly.value[id] ?? 0) >= 50 || !!(dim?.anomaly || anomBits[di]))
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
    height: plotHeight(),
    series,
    legend: { show: true, live: true },
    cursor: { drag: { x: false, y: false }, y: false },
    scales: { x: { time: true }, y: { range: (_u, min, max) => [Math.min(0, min), max <= 0 ? 1 : max * 1.05] } },
    axes: [
      { stroke: '#94a3b8', space: 110, grid: { stroke: '#1e293b' }, ticks: { stroke: '#1e293b' } },
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
  snapshotReady.value = false
  loading.value = true
  const requestedNode = selection.node || 'local'
  const requestedDims = visibleDims()
  try {
    if (anchor.value !== null && anchor.value-props.window < 1) throw new Error('此结束时间无法容纳当前看板时长，请选择更晚的结束时间。')
    // One bucket per collection period, otherwise slow charts come back as
    // mostly-null 1s rows and uPlot draws nothing between isolated samples.
    const d = await api.data(props.chart.id, anchor.value === null ? -props.window : anchor.value-props.window, anchor.value ?? 0, Math.min(1200, Math.ceil(props.window / step())), current.signal)
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
    dataNode = requestedNode
    raw = dims.map((id) => {
      const col = idx.get(id)
      return d.result.data.map((r) => (col == null ? null : (r[col] as number | null)))
    })
    sampleCount.value = times.length
    snapshotReady.value = true
    sampleStep.value = Number.isFinite(d.view_update_every) && d.view_update_every > 0 ? d.view_update_every : null
    dataRevision.value++
  } catch (e) {
    if (disposed || current.signal.aborted || generation !== loadGeneration) return
    error.value = String(e)
    releasePlot()
  }
  request = undefined
  loading.value = false
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
    if (plot.width !== width || plot.height !== plotHeight()) plot.setSize({ width, height: plotHeight() })
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
  if (anchor.value !== null) return
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
  sampleCount.value = times.length
  dataRevision.value++
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
  if (!visible || disposed || anchor.value !== null) return
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
  sampleCount.value = 0; snapshotReady.value = false
  sampleStep.value = null
  dataRevision.value++
  anomaly.value = {}
}

function updateSubscription() {
  unsub?.()
  unsub = visible && anchor.value === null && props.window <= 1200
    ? live.subscribe(props.chart.id, (m) => onLive(m.t, m.v)) : null
}

function updateVisibility() {
  const active = inViewport && pageVisible.value && !zoomed.value
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
    loading.value = false
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
  ro = new ResizeObserver(() => { if (visible && plot && el.value) plot.setSize({ width: el.value.clientWidth, height: plotHeight() }) })
  ro.observe(el.value!)
})
onBeforeUnmount(() => {
  disposed = true
  dialog.value?.close()
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
  [props.chart.id, props.chart.context, props.chart.title, props.chart.units, props.chart.chart_type, props.chart.update_every, anchor.value === null && props.chart.anomaly ? 1 : 0, ...props.chart.dimensions.map((d) => `${d.id}\u0000${d.name}\u0000${d.hidden ? 1 : 0}\u0000${anchor.value === null && d.anomaly ? 1 : 0}`)].join('\u0001')
watch([() => props.window, defFingerprint, anchor], () => {
  releasePlot()
  clearTimeout(refreshTimer)
  updateSubscription()
  if (visible) void load(); else ++loadGeneration
})
</script>

<template>
  <div ref="card" class="card" :class="{ anom: anchor === null && chart.anomaly }">
    <div class="head">
      <div>
        <span class="title" :title="help">{{ chart.title }}</span>
        <span class="id" :title="help">{{ chart.id }}</span>
        <span v-if="anchor === null && (anomalous || chart.anomaly)" class="anom">ANOM</span>
      </div>
      <span v-if="stale && anchor === null" class="err" :title="lastSample ? new Date(lastSample * 1000).toLocaleString() : '尚无样本'">{{ lastSample ? '数据过期' : '暂无数据' }}</span>
      <span v-if="!detail && anchor !== null" class="history-badge" :title="rangeLabel">历史快照</span>
      <span class="units" :title="unitHelp(chart.units)">{{ chart.units }}</span>
    </div>
    <div class="chart-actions">
      <button v-if="!detail" ref="zoomButton" type="button" @click="openDetail">放大图表</button>
      <button type="button" :disabled="!snapshotReady || !sampleCount" title="导出当前已加载数据，空白单元格表示缺失；长时间范围可能为聚合采样。" @click="exportCSV">导出 CSV</button>
    </div>
    <div v-if="detail" class="history-controls" aria-label="历史时段控制">
      <p class="range-label">{{ rangeLabel }}</p>
      <p v-if="anchor !== null" class="data-note">历史查看已停止自动刷新；当前异常标记不代表历史状态，此处不显示。</p>
      <div class="chart-actions"><button v-if="anchor === null" type="button" @click="freezeTime">固定当前时间</button><button v-else type="button" @click="resumeTime">返回实时</button><button type="button" @click="moveHistory(-1)">上一时段</button><button type="button" :disabled="anchor === null || anchor >= Math.floor(Date.now()/1000)" @click="moveHistory(1)">下一时段</button></div>
      <form @submit.prevent="setHistory"><label>结束时间（本地）<input v-model="endInput" type="datetime-local" step="1" required /></label><button type="submit">查看该时段</button></form>
      <div class="chart-actions"><button type="button" :aria-pressed="comparing" title="开启对比会固定本次放大查看时间，不改变整个看板；前段为相邻的等长时段。" @click="toggleComparison">{{ comparing ? '关闭时段对比' : '对比上一时段' }}</button></div>
      <p v-if="rangeError" role="alert" class="err">{{ rangeError }}</p>
    </div>
    <ChartComparison v-if="detail && comparing && anchor !== null && snapshotReady" :chart="chart" :node="dataNode" :end="anchor" :duration="window" :current="summaries" :current-step="sampleStep" />
    <details class="metric-help">
      <summary :title="help">中文指标说明</summary>
      <p>{{ help }}</p>
      <ul><li v-for="dim in chart.dimensions.filter(d => !d.hidden)" :key="dim.id">{{ dimensionHelp(chart, dim) }}</li></ul>
    </details>
    <div ref="el" class="plot"></div>
    <p v-if="loading" class="data-note" role="status">正在加载采样…</p>
    <p v-else-if="snapshotReady && !sampleCount" class="data-note" role="status">该时间范围没有采样，请调整时段或检查采集与数据保留范围。</p>
    <div v-if="error" class="err" role="alert">采样加载失败：{{ error }} <button type="button" :disabled="loading" @click="load()">重试加载</button></div>
    <div v-if="detail && snapshotReady && sampleCount" class="sample-summary">
      <p class="data-note">已加载采样统计 · {{ chart.units }}。均值为非空采样的算术平均，长范围可能已聚合；非空采样数不代表服务可用率。</p>
      <div class="table-scroll" tabindex="0" aria-label="采样统计横向滚动区"><table aria-label="各维度采样统计"><thead><tr><th>指标维度</th><th title="最后一行采样中的值，缺失显示 —，不向前填充。">末行值</th><th title="已加载非空采样的最小值，不包含缺失值。">最小值</th><th title="已加载非空采样的最大值，不包含缺失值。">最大值</th><th title="仅对已加载的有限数值取算术平均；缺失不按 0 计算。">平均值</th><th title="已加载行中的有效数值数量 / 总行数，不是采集成功率。">非空采样</th></tr></thead><tbody><tr v-for="s in summaries" :key="s.id"><th :title="s.help">{{ s.name }}</th><td>{{ s.last === null ? '—' : fmt(s.last) }}</td><td>{{ s.min === null ? '—' : fmt(s.min) }}</td><td>{{ s.max === null ? '—' : fmt(s.max) }}</td><td>{{ s.mean === null ? '—' : fmt(s.mean) }}</td><td>{{ s.count }} / {{ s.total }}</td></tr></tbody></table></div>
    </div>
    <dialog v-if="!detail" ref="dialog" class="chart-dialog" :aria-label="`${chart.title} 放大图表`" @close="closeDetail">
      <div class="dialog-heading"><b>{{ chart.title }}</b><button type="button" autofocus @click="closeDetail">关闭放大图表</button></div>
      <MetricChart v-if="zoomed" :chart="chart" :window="window" :end="anchor" detail />
      <p class="dialog-note">使用当前节点；在此窗口调整时间只影响本次放大查看，关闭后恢复看板的查看时间。CSV 只包含已加载采样；空白表示缺失，不代表 0。</p>
    </dialog>
  </div>
</template>

<style scoped>
.history-badge { color:#fcd34d; font-size:11px; border:1px solid #a17c26; border-radius:4px; padding:2px 5px; }
.history-controls { padding:10px; margin:10px 0; background:#111e30; border-radius:8px; } .range-label, .data-note { font-size:12px; line-height:1.6; color:#94a3b8; overflow-wrap:anywhere; } .history-controls form { display:flex; flex-wrap:wrap; align-items:end; gap:8px; } .history-controls label { display:flex; flex-direction:column; gap:4px; font-size:12px; min-width:0; max-width:100%; } .history-controls input { min-width:0; max-width:100%; box-sizing:border-box; padding:5px; background:#0b1120; color:#e2e8f0; border:1px solid #475569; border-radius:4px; color-scheme:dark; } .history-controls form button, .err button { padding:6px 8px; cursor:pointer; }
.table-scroll { overflow-x:auto; } .sample-summary table { width:100%; border-collapse:collapse; font-size:12px; } .sample-summary th, .sample-summary td { text-align:right; padding:8px; border-bottom:1px solid #334155; white-space:nowrap; } .sample-summary th:first-child { text-align:left; max-width:220px; overflow:hidden; text-overflow:ellipsis; } .table-scroll:focus-visible { outline:2px solid #5eead4; }
.chart-actions { display:flex; gap:8px; margin:6px 0; } .chart-actions button, .dialog-heading button { cursor:pointer; color:#cbd5e1; background:#172337; border:1px solid #334155; border-radius:6px; padding:4px 8px; font:inherit; font-size:12px; } .chart-actions button:disabled { opacity:.4; cursor:default; }
.chart-dialog { width:min(1100px,calc(100vw - 40px)); max-height:calc(100dvh - 40px); box-sizing:border-box; padding:14px; background:#0b1120; color:#e2e8f0; border:1px solid #475569; border-radius:12px; } .chart-dialog::backdrop { background:#000a; }
.dialog-heading { display:flex; justify-content:space-between; align-items:center; gap:12px; margin-bottom:12px; overflow-wrap:anywhere; position:sticky; top:-14px; z-index:2; background:#0b1120; padding:8px 0; } .dialog-note { color:#94a3b8; font-size:12px; } button:focus-visible { outline:2px solid #5eead4; outline-offset:2px; }
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
.head .anom { margin-left: 8px; font-size: 10px; font-weight: 700; color: #fecaca; background: #7f1d1d; border-radius: 4px; padding: 1px 6px; letter-spacing: 0.04em; }
.units { color: #94a3b8; font-size: 12px; }
.plot { width: 100%; min-height: 180px; }
.err { color: #f87171; font-size: 12px; margin-top: 4px; }
:deep(.u-legend) { font-size: 11px; color: #cbd5e1; }
:deep(.u-legend .u-value) { font-family: ui-monospace, monospace; }
</style>
