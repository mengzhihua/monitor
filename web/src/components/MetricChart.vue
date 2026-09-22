<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import type { Chart } from '../api'
import { api } from '../api'
import { live } from '../live'

const props = defineProps<{ chart: Chart; window: number }>()

const el = ref<HTMLDivElement>()
const latest = ref<Record<string, number>>({})
const error = ref('')
const clock = ref(Date.now()/1000)
const lastSample = ref(props.chart.last_entry || 0)
const stale = computed(() => !lastSample.value || clock.value-lastSample.value > Math.max(15, 3*step()))
let loadGeneration = 0
let disposed = false
let refreshTimer: number | undefined

let plot: uPlot | null = null
let unsub: (() => void) | null = null
let times: number[] = []
let raw: (number | null)[][] = []
let dims: string[] = []
let pending = false

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
    const color = palette[di % palette.length]!
    series.push({
      label: props.chart.dimensions.find((d) => d.id === id)?.name ?? id,
      stroke: color,
      width: 1,
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

async function load() {
  const generation = ++loadGeneration
  error.value = ''
  dims = visibleDims()
  try {
    // One bucket per collection period, otherwise slow charts come back as
    // mostly-null 1s rows and uPlot draws nothing between isolated samples.
    const d = await api.data(props.chart.id, -props.window, 0, Math.min(1200, Math.ceil(props.window / step())))
    if (disposed || generation !== loadGeneration) return
    times = d.result.data.map((r) => r[0] as number)
    const idx = new Map(d.dimension_ids.map((id, i) => [id, i + 1]))
    raw = dims.map((id) => {
      const col = idx.get(id)
      return d.result.data.map((r) => (col == null ? null : (r[col] as number | null)))
    })
    const last = d.result.data.at(-1)
    if (last) {
      const lv: Record<string, number> = {}
      dims.forEach((id) => { const c = idx.get(id); if (c != null && last[c] != null) lv[id] = last[c] as number })
      latest.value = lv
    }
  } catch (e) {
    if (disposed || generation !== loadGeneration) return
    error.value = String(e)
    times = []
    raw = dims.map(() => [])
  }
  render()
}

function render() {
  if (!el.value) return
  const width = el.value.clientWidth || 600
  if (plot) plot.destroy()
  plot = new uPlot(makeOpts(width), buildData(), el.value)
}

function onLive(t: number, v: Record<string, number>) {
  lastSample.value = Math.max(lastSample.value, t)
  latest.value = v
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
  latest.value = v
  if (!pending) {
    pending = true
    requestAnimationFrame(() => { pending = false; plot?.setData(buildData()) })
  }
}

let ro: ResizeObserver | null = null

onMounted(() => {
  load()
  refreshTimer = window.setInterval(() => { clock.value = Date.now()/1000; load() }, 30000)
  unsub = live.subscribe(props.chart.id, (m) => onLive(m.t, m.v))
  ro = new ResizeObserver(() => { if (plot && el.value) plot.setSize({ width: el.value.clientWidth, height: 180 }) })
  ro.observe(el.value!)
})
onBeforeUnmount(() => { disposed = true; ++loadGeneration; clearInterval(refreshTimer); unsub?.(); ro?.disconnect(); plot?.destroy() })
watch(() => props.window, load)
watch(() => props.chart.last_entry, (t) => { lastSample.value = Math.max(lastSample.value, t || 0) })
/** Anything that feeds makeOpts/buildData/load: a changed definition needs a full reload. */
const defFingerprint = () =>
  [props.chart.chart_type, props.chart.update_every, ...props.chart.dimensions.map((d) => `${d.id}\u0000${d.name}\u0000${d.hidden ? 1 : 0}`)].join('\u0001')
watch(defFingerprint, load)
</script>

<template>
  <div class="card">
    <div class="head">
      <div>
        <span class="title">{{ chart.title }}</span>
        <span class="id">{{ chart.id }}</span>
      </div>
      <span v-if="stale" class="err" :title="lastSample ? new Date(lastSample * 1000).toLocaleString() : '尚无样本'">{{ lastSample ? '数据过期' : '暂无数据' }}</span>
      <span class="units">{{ chart.units }}</span>
    </div>
    <div ref="el" class="plot"></div>
    <div v-if="error" class="err">{{ error }}</div>
  </div>
</template>

<style scoped>
.card { background: #0f172a; border: 1px solid #1e293b; border-radius: 8px; padding: 10px 12px; min-width: 0; }
.head { display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 4px; }
.title { font-weight: 600; font-size: 14px; }
.id { color: #64748b; font-size: 11px; margin-left: 8px; font-family: ui-monospace, monospace; }
.units { color: #94a3b8; font-size: 12px; }
.plot { width: 100%; }
.err { color: #f87171; font-size: 12px; margin-top: 4px; }
:deep(.u-legend) { font-size: 11px; color: #cbd5e1; }
:deep(.u-legend .u-value) { font-family: ui-monospace, monospace; }
</style>
