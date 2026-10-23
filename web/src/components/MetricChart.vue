<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import type { Chart } from '../api/monitoring/types'
import { metricHelp, dimensionHelp, unitHelp } from '../metricHelp'
import { chartCSV } from '../chartExport'
import { historyEnd, localDateTime, summarizeSeries } from '../chartInspection'
import { formatSample as fmt } from '../features/metrics/model/chartSeries'
import { useMetricChart } from '../features/metrics/useMetricChart'
import ChartComparison from './ChartComparison.vue'
import AppIcon from './AppIcon.vue'

const props = defineProps<{ chart: Chart; window: number; end?: number | null; detail?: boolean }>()
const dialog = ref<HTMLDialogElement>()
const zoomButton = ref<HTMLButtonElement>()
const zoomed = ref(false)
const comparing = ref(false)
const anchor = ref<number | null>(props.end ?? null)
const endInput = ref(localDateTime(props.end ?? Math.floor(Date.now()/1000)))
const rangeError = ref('')
const inspectedDimension = ref('')
const card = ref<HTMLDivElement>()
const el = ref<HTMLDivElement>()
const { series, sampleCount, snapshotReady, loading, sampleStep, dataNode,
  error, lastSample, stale, anomalous, load } = useMetricChart(props, anchor, zoomed, { card, el })

watch(anchor, value => { if (value === null) comparing.value = false })
watch(() => props.end, end => {
  anchor.value = end ?? null
  endInput.value = localDateTime(end ?? Math.floor(Date.now()/1000))
  rangeError.value = ''
})

// Inspection and export share the accepted raw snapshot with the renderer.
const touchSample = computed(() => {
  const { dimensions, times, values } = series.value
  const index = Math.max(0, dimensions.indexOf(inspectedDimension.value))
  const id = dimensions[index]
  const timestamp = times.at(-1)
  if (!id || timestamp === undefined || !Number.isFinite(timestamp)) return null
  const value = values[index]?.at(-1)
  return { id, timestamp, value: value != null && Number.isFinite(value) ? value : null }
})
const summaries = computed(() => series.value.dimensions.map((id, index) => {
  const dimension = props.chart.dimensions.find(dimension => dimension.id === id)
  return { id, name: dimension?.name || id,
    help: dimension ? dimensionHelp(props.chart, dimension) : metricHelp(props.chart),
    ...summarizeSeries(series.value.values[index] || []) }
}))
const help = computed(() => metricHelp(props.chart))
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
let disposed = false
async function openDetail() {
  zoomed.value = true
  await nextTick()
  dialog.value?.showModal()
}
function closeDetail() {
  if (disposed) return
  dialog.value?.close()
  zoomed.value = false
  zoomButton.value?.focus()
}
function exportCSV() {
  const snapshot = series.value
  if (!snapshotReady.value || !snapshot.times.length) return
  const csv = chartCSV(props.chart, snapshot.node, snapshot.times, snapshot.dimensions, snapshot.values)
  const url = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' }))
  const link = document.createElement('a')
  link.href = url; link.download = `${props.chart.id.replace(/[^a-zA-Z0-9._-]/g, '_').slice(0,100)}.csv`; link.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
onBeforeUnmount(() => { disposed = true; dialog.value?.close() })
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
      <button v-if="!detail" ref="zoomButton" type="button" @click="openDetail"><AppIcon name="expand" :size="14" />放大图表</button>
      <button type="button" :disabled="!snapshotReady || !sampleCount" title="导出当前已加载数据，空白单元格表示缺失；长时间范围可能为聚合采样。" @click="exportCSV"><AppIcon name="download" :size="14" />导出 CSV</button>
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
      <summary :title="help"><AppIcon name="info" :size="14" />中文指标说明</summary>
      <p>{{ help }}</p>
      <ul><li v-for="dim in chart.dimensions.filter(d => !d.hidden)" :key="dim.id">{{ dimensionHelp(chart, dim) }}</li></ul>
    </details>
    <div ref="el" class="plot"></div>
    <div v-if="snapshotReady && sampleCount && touchSample" class="touch-sample" aria-label="已加载末行采样">
      <div class="touch-sample-heading"><b>已加载末行</b><span v-if="sampleStep && sampleStep > (chart.update_every || 1)">采样间隔 {{ sampleStep }} 秒</span></div>
      <label>查看维度<select :value="touchSample.id" @change="inspectedDimension = ($event.target as HTMLSelectElement).value"><option v-for="dim in chart.dimensions.filter(d => !d.hidden)" :key="dim.id" :value="dim.id">{{ dim.name || dim.id }}</option></select></label>
      <p class="touch-sample-value"><strong>{{ touchSample.value === null ? '缺失' : fmt(touchSample.value) }}</strong><span v-if="touchSample.value !== null">{{ chart.units }}</span></p>
      <p class="touch-sample-time">采样时间 <time :datetime="new Date(touchSample.timestamp * 1000).toISOString()">{{ new Date(touchSample.timestamp * 1000).toLocaleString() }}</time><span v-if="touchSample.value === null"> · 不按 0 处理</span></p>
    </div>
    <p v-if="loading" class="data-note" role="status">正在加载采样…</p>
    <p v-else-if="snapshotReady && !sampleCount" class="data-note" role="status">该时间范围没有采样，请调整时段或检查采集与数据保留范围。</p>
    <div v-if="error" class="err" role="alert">采样加载失败：{{ error }} <button type="button" :disabled="loading" @click="load()">重试加载</button></div>
    <div v-if="detail && snapshotReady && sampleCount" class="sample-summary">
      <p class="data-note">已加载采样统计 · {{ chart.units }}。均值为非空采样的算术平均，长范围可能已聚合；非空采样数不代表服务可用率。</p>
      <div class="table-scroll" tabindex="0" aria-label="采样统计横向滚动区"><table aria-label="各维度采样统计"><thead><tr><th>指标维度</th><th title="最后一行采样中的值，缺失显示 —，不向前填充。">末行值</th><th title="已加载非空采样的最小值，不包含缺失值。">最小值</th><th title="已加载非空采样的最大值，不包含缺失值。">最大值</th><th title="仅对已加载的有限数值取算术平均；缺失不按 0 计算。">平均值</th><th title="已加载行中的有效数值数量 / 总行数，不是采集成功率。">非空采样</th></tr></thead><tbody><tr v-for="s in summaries" :key="s.id"><th :title="s.help">{{ s.name }}</th><td>{{ s.last === null ? '—' : fmt(s.last) }}</td><td>{{ s.min === null ? '—' : fmt(s.min) }}</td><td>{{ s.max === null ? '—' : fmt(s.max) }}</td><td>{{ s.mean === null ? '—' : fmt(s.mean) }}</td><td>{{ s.count }} / {{ s.total }}</td></tr></tbody></table></div>
    </div>
    <dialog v-if="!detail" ref="dialog" class="chart-dialog" :aria-label="`${chart.title} 放大图表`" @close="closeDetail">
      <div class="dialog-heading"><b>{{ chart.title }}</b><button type="button" autofocus @click="closeDetail"><AppIcon name="close" :size="14" />关闭放大图表</button></div>
      <MetricChart v-if="zoomed" :chart="chart" :window="window" :end="anchor" detail />
      <p class="dialog-note">使用当前节点；在此窗口调整时间只影响本次放大查看，关闭后恢复看板的查看时间。CSV 只包含已加载采样；空白表示缺失，不代表 0。</p>
    </dialog>
  </div>
</template>

<style scoped>
.history-badge { color:#fcd34d; font-size:11px; border:1px solid #a17c26; border-radius:4px; padding:2px 5px; }
.history-controls { padding:10px; margin:10px 0; background:#111e30; border-radius:8px; } .range-label, .data-note { font-size:12px; line-height:1.6; color:#94a3b8; overflow-wrap:anywhere; } .history-controls form { display:flex; flex-wrap:wrap; align-items:end; gap:8px; } .history-controls label { display:flex; flex-direction:column; gap:4px; font-size:12px; min-width:0; max-width:100%; } .history-controls input { min-width:0; max-width:100%; box-sizing:border-box; padding:5px; background:#0b1120; color:#e2e8f0; border:1px solid #475569; border-radius:4px; color-scheme:dark; } .history-controls form button, .err button { padding:6px 8px; cursor:pointer; }
.table-scroll { overflow-x:auto; } .sample-summary table { width:100%; border-collapse:collapse; font-size:12px; } .sample-summary th, .sample-summary td { text-align:right; padding:8px; border-bottom:1px solid #334155; white-space:nowrap; } .sample-summary th:first-child { text-align:left; max-width:220px; overflow:hidden; text-overflow:ellipsis; } .table-scroll:focus-visible { outline:2px solid #5eead4; }
.chart-actions { display:flex; flex-wrap:wrap; gap:8px; margin:6px 0; } .chart-actions button, .dialog-heading button { cursor:pointer; color:#cbd5e1; background:#172337; border:1px solid #334155; border-radius:6px; padding:4px 8px; font:inherit; font-size:12px; } .chart-actions button:disabled { opacity:.4; cursor:default; }
.chart-actions button,.dialog-heading button { display:inline-flex; align-items:center; justify-content:center; gap:5px; }
.metric-help summary > .app-icon { margin-right:5px; }
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
.plot { width: 100%; min-width:0; min-height: 180px; }
.err { color: #f87171; font-size: 12px; margin-top: 4px; }
:deep(.uplot) { width:100%; max-width:100%; }
:deep(.u-legend) { display:block; max-width:100%; overflow-x:auto; overscroll-behavior-x:contain; white-space:nowrap; font-size: 11px; color: #cbd5e1; }
:deep(.u-legend .u-value) { font-family: ui-monospace, monospace; }
.touch-sample { display:none; }
@media (max-width:600px), (pointer:coarse) {
  .card { padding:10px; }
  .head > div { min-width:0; max-width:100%; }
  .chart-actions button, .dialog-heading button, .history-controls button, .err button { min-height:44px; min-width:44px; }
  .metric-help summary { box-sizing:border-box; min-height:44px; padding-block:12px; cursor:pointer; }
  .history-controls input, .touch-sample select { min-height:44px; font-size:16px; }
  .history-controls label { width:100%; }
  .touch-sample { display:block; min-width:0; border-top:1px solid #334155; margin-top:8px; padding-top:10px; }
  .touch-sample-heading { display:flex; flex-wrap:wrap; align-items:baseline; justify-content:space-between; gap:6px; color:#94a3b8; font-size:11px; }
  .touch-sample-heading b { color:#cbd5e1; font-size:12px; }
  .touch-sample label { display:flex; flex-direction:column; gap:4px; margin-top:8px; font-size:12px; color:#94a3b8; }
  .touch-sample select { box-sizing:border-box; min-width:0; width:100%; padding:6px 8px; color:#e2e8f0; background:#111e30; border:1px solid #475569; border-radius:6px; text-overflow:ellipsis; }
  .touch-sample select:focus-visible { outline:2px solid #5eead4; outline-offset:2px; }
  .touch-sample-value { display:flex; flex-wrap:wrap; align-items:baseline; gap:8px; margin:8px 0 4px; overflow-wrap:anywhere; }
  .touch-sample-value strong { font:600 22px ui-monospace,monospace; color:#e2e8f0; }
  .touch-sample-value span, .touch-sample-time { color:#94a3b8; font-size:12px; line-height:1.6; }
  .touch-sample-time { margin:0; overflow-wrap:anywhere; }
  :deep(.u-legend .u-series th), :deep(.u-legend .u-series td) { height:44px; box-sizing:border-box; }
}
@media (max-width:600px) {
  .chart-dialog { margin:0; width:100%; max-width:none; height:100dvh; max-height:100dvh; border:0; border-radius:0; padding:max(10px,env(safe-area-inset-top)) max(10px,env(safe-area-inset-right)) max(10px,env(safe-area-inset-bottom)) max(10px,env(safe-area-inset-left)); overscroll-behavior:contain; }
  .dialog-heading { top:calc(-1 * max(10px,env(safe-area-inset-top))); padding-top:max(10px,env(safe-area-inset-top)); gap:8px; }
  .dialog-heading b { min-width:0; flex:1; }
  .dialog-heading button { flex:none; }
}
</style>
