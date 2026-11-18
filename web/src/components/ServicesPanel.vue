<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { textCell } from '../chartExport'
import { api, ApiError } from '../api'
import type { AvailabilityRow, ServiceReport, TopologyEdge } from '../api'
import { usePolling } from '../polling'
import AppIcon from './AppIcon.vue'

const report = ref<ServiceReport | null>(null)
const edges = ref<TopologyEdge[]>([])
const rows = ref<AvailabilityRow[]>([])
const error = ref('')
const windowRange = ref('24h')
const query = ref('')
const problemsOnly = ref(false)
const loading = ref(false)
const updatedAt = ref(0)
const loadedWindow = ref('')
const availabilityWindow = ref({ from: 0, to: 0 })
const matches = (values: string[]) => values.join(' ').toLocaleLowerCase().includes(query.value.trim().toLocaleLowerCase())
const visibleServices = computed(() => (report.value?.services || []).filter(s => matches([s.name, ...(s.alarms || []), ...(s.children || [])]) && (!problemsOnly.value || s.status !== 'CLEAR')).sort((a, b) => ({ CRITICAL: 3, WARNING: 2 }[b.status] || 0) - ({ CRITICAL: 3, WARNING: 2 }[a.status] || 0) || a.name.localeCompare(b.name)))
const visibleRows = computed(() => rows.value.filter(r => matches([r.name, r.chart]) && (!problemsOnly.value || r.raised_seconds > 0)).sort((a, b) => a.uptime - b.uptime || a.name.localeCompare(b.name)))
const visibleEdges = computed(() => edges.value.filter(e => matches([e.source, e.target, e.kind])))
const problemCount = computed(() => (report.value?.services || []).filter(s => s.status !== 'CLEAR').length)
const timestamp = (value: number) => new Date(value * 1000).toLocaleString()
function exportCSV() {
  const header = ['告警', '图表', '未告警时间占比 (%)', '告警秒数', '窗口秒数', '开始 (UTC)', '结束 (UTC)']
  const data = visibleRows.value.map(r => [textCell(r.name), textCell(r.chart), r.uptime, r.raised_seconds, r.window_seconds, textCell(new Date(availabilityWindow.value.from * 1000).toISOString()), textCell(new Date(availabilityWindow.value.to * 1000).toISOString())].join(','))
  const url = URL.createObjectURL(new Blob(['\uFEFF' + [header.map(textCell).join(','), ...data].join('\r\n')], { type: 'text/csv;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = `monitor-availability-${loadedWindow.value}.csv`
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}


const refresh = usePolling(async signal => {
  loading.value = true
  const requestedWindow = windowRange.value
  try {
    const [services, topology, availability] = await Promise.all([api.services(signal, requestedWindow), api.topology(signal), api.availability(signal, requestedWindow)])
    if (signal.aborted) return
    report.value = services
    edges.value = topology.edges || []
    rows.value = availability.alarms || []
    error.value = ''
    updatedAt.value = Date.now() / 1000
    loadedWindow.value = requestedWindow
    availabilityWindow.value = { from: availability.from, to: availability.to }
  } catch (e) {
    if (signal.aborted) return
    error.value = e instanceof ApiError ? e.message : '读取服务与拓扑失败'
  } finally {
    if (!signal.aborted) loading.value = false
  }
}, 15000)
watch(windowRange, () => { void refresh() })

function pie(sla: number) {
  const p = Math.max(0, Math.min(100, sla))
  const a = (p / 100) * Math.PI * 2
  const x = 16 + Math.sin(a) * 14
  const y = 16 - Math.cos(a) * 14
  const large = p > 50 ? 1 : 0
  if (p >= 100) return ''
  if (p <= 0) return 'M 16 16'
  return `M 16 16 L 16 2 A 14 14 0 ${large} 1 ${x} ${y} Z`
}
</script>

<template>
  <section class="services" aria-label="服务与拓扑">
    <h1><AppIcon name="services" :size="24" />服务与拓扑</h1>
    <p class="muted">从业务服务定位告警与依赖，查看当前状态及历史告警时间。</p>
    <div class="toolbar">
      <label>统计窗口 <select v-model="windowRange" aria-label="统计窗口"><option value="1h">最近 1 小时</option><option value="24h">最近 24 小时</option><option value="168h">最近 7 天</option><option value="720h">最近 30 天</option></select></label>
      <label>搜索 <input v-model="query" type="search" placeholder="服务、告警、图表或依赖" aria-label="搜索服务与告警" /></label>
      <label><input v-model="problemsOnly" type="checkbox" />只看异常</label>
      <button :disabled="loading" @click="refresh">{{ loading ? '刷新中…' : '刷新' }}</button>
      <button :disabled="loading || !!error || !visibleRows.length" @click="exportCSV">导出筛选结果 CSV</button>
    </div>
    <p v-if="error" role="alert">{{ error }}。请重试。<span v-if="report">当前保留上次成功数据，不代表实时状态。</span></p>
    <p v-if="updatedAt" class="muted" role="status">上次成功：{{ timestamp(updatedAt) }} · 已加载窗口 {{ loadedWindow }} · 每 15 秒自动刷新</p>
    <p v-if="!report && !error">正在读取…</p>
    <template v-if="report">
      <div class="summary" aria-label="服务摘要"><strong>{{ report.services.length }} 个服务</strong><strong>{{ problemCount }} 个异常服务</strong><span>{{ rows.length }} 个告警统计</span><span>{{ edges.length }} 条连接</span></div>
      <p class="muted">统计区间：{{ timestamp(report.from) }} — {{ timestamp(report.to) }}。占比依据保留的告警历史计算，服务取关联项最小值；不代表完整探测覆盖率或业务 SLA 承诺。</p>
      <p v-if="!report.services.length" class="muted">还没有配置 services。在 monitor.yaml 里声明服务树后，这里显示服务状态与关联告警统计。</p>
      <div class="cards">
        <article v-for="s in visibleServices" :key="s.name" class="card" :data-status="s.status" :data-service="s.name">
          <svg viewBox="0 0 32 32" class="pie" :aria-label="s.name + ' 告警历史占比 ' + s.sla.toFixed(1) + '%'">
            <circle cx="16" cy="16" r="14" class="track" />
            <path v-if="pie(s.sla)" :d="pie(s.sla)" class="slice" />
            <circle v-else cx="16" cy="16" r="14" class="slice" />
          </svg>
          <div>
            <h2>{{ s.name }}</h2>
            <p>{{ s.status }} · 未告警占比 {{ s.sla.toFixed(1) }}%</p>
            <p v-if="s.alarms?.length" class="muted">关联告警：<button v-for="alarm in s.alarms" :key="alarm" class="text-button" @click="query = alarm; problemsOnly = false">{{ alarm }}</button></p>
            <p v-if="s.children?.length" class="muted">依赖服务：<button v-for="child in s.children" :key="child" class="text-button" @click="query = child; problemsOnly = false">{{ child }}</button></p>
          </div>
        </article>
      </div>
      <p v-if="report.services.length && !visibleServices.length" class="muted">没有匹配的服务，请调整搜索或异常筛选。</p>
      <h2><AppIcon name="contexts" />拓扑</h2>
      <p v-if="!edges.length" class="muted">没有 LLDP 邻居或手工 topology 边。</p>
      <ul v-else class="topology">
        <li v-for="(e, i) in visibleEdges" :key="i"><span>{{ e.source }} → {{ e.target }}</span> <small>{{ e.kind }}</small></li>
      </ul>
      <p v-if="edges.length && !visibleEdges.length" class="muted">没有匹配的连接。</p>
      <h2><AppIcon name="shield" />告警历史统计</h2>
      <p class="muted">“只看异常”筛选当前异常服务和窗口内曾触发的告警；拓扑仅按搜索词筛选。</p>
      <p v-if="!visibleRows.length" class="muted">没有符合条件的告警统计。</p>
      <table v-if="visibleRows.length" aria-label="告警可用性">
        <thead><tr><th>告警</th><th>图表</th><th>未告警占比</th><th>告警时长</th></tr></thead>
        <tbody>
          <tr v-for="r in visibleRows" :key="r.name + r.chart"><td data-label="告警"><span>{{ r.name }}</span></td><td data-label="图表"><span>{{ r.chart }}</span></td><td data-label="未告警占比"><span>{{ r.uptime.toFixed(1) }}%</span></td><td data-label="告警时长"><span>{{ r.raised_seconds }} 秒</span></td></tr>
        </tbody>
      </table>
    </template>
  </section>
</template>

<style scoped>
.services { max-width: 1100px; min-width: 0; margin: 0 auto; padding: 24px; color: #e2e8f0; overflow-wrap: anywhere; }
.toolbar, .summary { display:flex; align-items:center; flex-wrap:wrap; gap:12px; margin:16px 0; }
.toolbar label { display:flex; align-items:center; gap:6px; }
.toolbar input[type="search"] { min-width:0; width:220px; }
.toolbar select, .toolbar input, button { color:inherit; background:#0f172a; border:1px solid #475569; border-radius:6px; padding:8px; }
button { cursor:pointer; } button:disabled { opacity:.5; cursor:default; }
.text-button { color:#5eead4; margin:3px; padding:3px 6px; }
.card[data-status="CRITICAL"] { border-color:#fb7185; }
.card[data-status="WARNING"] { border-color:#fbbf24; }
.summary { padding:16px; background:#0f172a; border-radius:12px; }
.services > h2 { margin-top:24px; margin-bottom:12px; }
.muted { color: #94a3b8; }
.cards { display: flex; flex-wrap: wrap; gap: 12px; }
.card { display: flex; gap: 12px; align-items: center; background: #0f172a; border: 1px solid #1e293b; border-radius: 12px; padding: 12px 16px; min-width: 220px; }
.card > div { min-width: 0; }
.pie { width: 48px; height: 48px; flex-shrink: 0; }
.track { fill: #1e293b; }
.slice { fill: #34d399; }
h1, h2 { font-weight: 650; }
h1 > .app-icon, h2 > .app-icon { margin-right:8px; color:#5eead4; }
h2 { font-size: 16px; margin: 0; }
table { width: 100%; border-collapse: collapse; }
td, th { text-align: left; padding: 6px 8px; border-bottom: 1px solid #1e293b; }
@media (max-width: 760px) {
  .services { padding: 16px 12px; }
  h1 { font-size: 22px; }
  .services > h2 { margin-top: 20px; margin-bottom: 10px; }
  .cards { display: grid; grid-template-columns: minmax(0, 1fr); }
  .card { min-width: 0; padding: 12px; }
  .card p { line-height: 1.5; margin-bottom: 0; }
  .topology { list-style: none; padding: 0; }
  .topology li { padding: 12px; margin-bottom: 8px; background: #0f172a; border: 1px solid #1e293b; border-radius: 8px; line-height: 1.6; }
  .topology small { display: block; color: #94a3b8; }
  table, tbody { display: block; }
  thead { position: absolute; width: 1px; height: 1px; padding: 0; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
  tbody { display: grid; gap: 8px; }
  tbody tr { display: block; min-width: 0; padding: 10px 12px; background: #0f172a; border: 1px solid #1e293b; border-radius: 8px; }
  td { display: grid; grid-template-columns: 60px minmax(0, 1fr); gap: 10px; padding: 5px 0; border: 0; }
  td::before { content: attr(data-label); color: #94a3b8; font-size: 12px; }
  td > span { min-width: 0; }
}
</style>
