<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { api, selection, type Chart, type DataResponse } from '../api'
import { pageVisible } from '../visibility'
import { compareSeries, comparisonCSV, type ComparisonSeries } from '../chartComparison'

const props = defineProps<{ chart: Chart; node: string; end: number; duration: number; current: ComparisonSeries[]; currentStep: number | null }>()
const previous = ref<DataResponse | null>(null)
const loading = ref(false)
const error = ref('')
let request: AbortController | undefined
let generation = 0
let disposed = false
const rows = computed(() => previous.value ? compareSeries(props.current, previous.value) : [])
const interval = (start: number, end: number) => `${new Date(start*1000).toLocaleString()} — ${new Date(end*1000).toLocaleString()}`
const display = (value: number | null) => value === null ? '—' : new Intl.NumberFormat('zh-CN', { maximumSignificantDigits: 6 }).format(value)
const signed = (value: number | null) => value === null ? '—' : `${value > 0 ? '+' : ''}${display(value)}`
const cadence = (value: number | null | undefined) => value != null && Number.isFinite(value) && value > 0 ? `${value} 秒` : '未知'
function cancel() { ++generation; request?.abort(); request = undefined; loading.value = false }
async function load() {
  cancel(); previous.value = null; error.value = ''
  if (disposed || !pageVisible.value) return
  const current = request = new AbortController(), version = generation
  loading.value = true
  try {
    if (props.end-props.duration*2 < 1) throw new Error('没有足够的时间范围查询前一时段，请选择更晚的结束时间。')
    if ((selection.node || 'local') !== props.node) throw new Error('节点已切换，请重新打开图表。')
    const data = await api.data(props.chart.id, props.end-props.duration*2, props.end-props.duration,
      Math.min(1200, Math.ceil(props.duration/Math.max(1, props.chart.update_every || 1))), current.signal)
    if (disposed || current.signal.aborted || version !== generation) return
    if ((selection.node || 'local') !== props.node) throw new Error('节点已切换，请重新打开图表。')
    if (data.units !== props.chart.units) throw new Error('两个时段的单位不一致，暂不计算对比。')
    previous.value = data
  } catch (e) {
    if (disposed || current.signal.aborted || version !== generation) return
    error.value = String(e)
  }
  request = undefined; loading.value = false
}
function download() {
  if (!previous.value) return
  const url = URL.createObjectURL(new Blob([comparisonCSV(props.chart, props.node, props.end, props.duration, rows.value)], { type: 'text/csv;charset=utf-8' }))
  const link = document.createElement('a'); link.href = url; link.download = `${props.chart.id.replace(/[^a-zA-Z0-9._-]/g, '_').slice(0,100)}-comparison.csv`; link.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
watch([() => props.end, () => props.duration, () => props.node, () => props.chart.id, () => props.chart.units, () => props.chart.update_every, pageVisible], () => { void load() }, { immediate: true })
onBeforeUnmount(() => { disposed = true; cancel() })
</script>

<template>
  <section class="comparison" aria-label="上一时段对比">
    <div class="heading"><b>相邻时段对比</b><button type="button" :disabled="!previous || loading" @click="download">导出时段对比 CSV</button></div>
    <p>本段：{{ interval(end-duration, end) }}<br />前段：{{ interval(end-duration*2, end-duration) }}</p>
    <p>同一节点、图表和单位（{{ chart.units }}），按维度自身数值比较。均值为已返回非空采样的算术平均；差值为本段减前段，不表示健康变好或变坏。</p>
    <p v-if="loading" role="status">正在加载前一时段…</p>
    <div v-else-if="error" role="alert" class="error">对比加载失败：{{ error }} <button type="button" @click="load">重试时段对比</button></div>
    <template v-if="previous">
      <p>返回采样网格：本段 {{ cadence(currentStep) }}，前段 {{ cadence(previous.view_update_every) }}。采样数量或聚合精度不同时需结合原曲线判断，非空采样比例不代表服务可用率。</p>
      <p v-if="rows.every(row => !row.prior.count)" role="status">前段没有有效采样，可能未采集或已超出数据保留范围；不会按 0 处理。</p>
      <div class="scroll" tabindex="0" aria-label="时段对比横向滚动区"><table aria-label="各维度时段对比">
        <thead><tr><th>指标维度</th><th title="当前固定时段内非空采样的算术平均。">本段均值</th><th title="前一个等长时段内非空采样的算术平均。">前段均值</th><th title="本段均值减前段均值，单位与图表相同；百分比指标的差值表示百分点。">均值差值</th><th title="均值差值 ÷ 前段均值 × 100%；仅在前段均值为正数时计算。">相对变化率</th><th title="本段与前段各自的非空采样数 / 总行数，不是成功率。">非空采样（本 / 前）</th><th>说明</th></tr></thead>
        <tbody><tr v-for="row in rows" :key="row.id"><th :title="row.help">{{ row.name }}</th><td>{{ display(row.mean) }}</td><td>{{ display(row.prior.mean) }}</td><td>{{ signed(row.delta) }}</td><td>{{ row.percent === null ? '—' : signed(row.percent) + '%' }}</td><td>{{ row.count }}/{{ row.total }} · {{ row.prior.count }}/{{ row.prior.total }}</td><td>{{ row.reason || '—' }}</td></tr></tbody>
      </table></div>
    </template>
  </section>
</template>

<style scoped>
.comparison { margin:12px 0; padding:12px; border:1px solid #36516d; border-radius:8px; background:#101e30; min-width:0; } .heading { display:flex; justify-content:space-between; align-items:center; flex-wrap:wrap; gap:8px; } b { font-size:13px; } p { font-size:12px; color:#94a3b8; line-height:1.6; overflow-wrap:anywhere; }
button { cursor:pointer; background:#172337; color:#cbd5e1; border:1px solid #475569; border-radius:6px; padding:6px 8px; font-size:12px; } button:disabled { opacity:.4; cursor:default; } button:focus-visible, .scroll:focus-visible { outline:2px solid #5eead4; outline-offset:2px; } .error { color:#fca5a5; font-size:12px; }
.scroll { overflow-x:auto; } table { width:100%; border-collapse:collapse; font-size:12px; } th, td { text-align:right; padding:8px; border-bottom:1px solid #334155; white-space:nowrap; } th:first-child { text-align:left; max-width:200px; overflow:hidden; text-overflow:ellipsis; } td:last-child { white-space:normal; min-width:160px; text-align:left; }
</style>
