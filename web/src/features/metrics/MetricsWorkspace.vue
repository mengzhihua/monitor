<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import type { Chart, Info } from '../../api/monitoring/types'
import type { NodeInfo } from '../../api/hub/types'
import MetricChart from '../../components/MetricChart.vue'
import ChartTimeline from '../../components/ChartTimeline.vue'
import DashboardsPanel from '../../components/DashboardsPanel.vue'
import CollectorStatus from './CollectorStatus.vue'
import { indexMetrics, metricDomains, metricMatches, type MetricDomain } from './metricCatalog'
import { metricWindows, useMetricsTimeline } from './useMetricsTimeline'

const props = defineProps<{ charts: Chart[]; node: string; info: Info | null; currentNode: NodeInfo | null }>()
const { windowSec, chartEnd, notice, showLiveChart } = useMetricsTimeline()
const viewKey = 'monitor.metrics-view.v1'
let initialView: 'all' | 'presets' = 'all'
try { if (sessionStorage.getItem(viewKey) === 'presets') initialView = 'presets' } catch { /* Viewing still works when browser storage is unavailable. */ }
const view = ref<'all' | 'presets'>(initialView)
watch(view, value => { try { sessionStorage.setItem(viewKey, value) } catch { /* Optional preference; never block navigation. */ } }, { flush: 'sync' })
const filter = ref('')
const category = ref<MetricDomain | 'all'>('all')
const page = ref(1)
const pageSize = 12
const resultsHeading = ref<HTMLElement>()
const indexed = computed(() => indexMetrics(props.charts))
const searched = computed(() => indexed.value.filter(metric => metricMatches(metric, filter.value)))
const domains = computed(() => metricDomains.map(domain => ({ ...domain, count: searched.value.filter(metric => metric.domain === domain.id).length })))
const filtered = computed(() => searched.value.filter(metric => category.value === 'all' || metric.domain === category.value))
const pageCount = computed(() => Math.max(1, Math.ceil(filtered.value.length / pageSize)))
const pageMetrics = computed(() => filtered.value.slice((page.value - 1) * pageSize, page.value * pageSize))
const sections = computed(() => metricDomains.map(domain => ({ ...domain, charts: pageMetrics.value.filter(metric => metric.domain === domain.id).map(metric => metric.chart) })).filter(section => section.charts.length))
const selectedDomain = computed(() => metricDomains.find(domain => domain.id === category.value))
const start = computed(() => filtered.value.length ? (page.value - 1) * pageSize + 1 : 0)
const end = computed(() => Math.min(page.value * pageSize, filtered.value.length))

watch([filter, category, () => props.node], () => { page.value = 1 }, { flush: 'sync' })
watch(pageCount, count => { page.value = Math.min(page.value, count) })
function resetFilters() { filter.value = ''; category.value = 'all' }
async function changePage(next: number) {
  page.value = Math.min(pageCount.value, Math.max(1, next))
  await nextTick()
  resultsHeading.value?.scrollIntoView({ block: 'start' })
  resultsHeading.value?.focus({ preventScroll: true })
}
function showChart(id: string) {
  showLiveChart()
  view.value = 'all'
  category.value = 'all'
  filter.value = id
  page.value = 1
}
defineExpose({ showChart })
</script>

<template>
  <div class="metrics-workspace" aria-label="指标工作区">
    <div class="workspace-heading">
      <h1>指标浏览器</h1>
      <span class="total-count">{{ charts.length }} 张可用图表</span>
    </div>
    <p class="description">按资源类型定位指标，或使用聚合看板查看常见场景。</p>
    <div class="view-switch" aria-label="看板视图">
      <button :aria-pressed="view === 'all'" @click="view = 'all'">全部指标</button>
      <button :aria-pressed="view === 'presets'" @click="view = 'presets'">常用聚合看板</button>
    </div>
    <div class="controls">
      <label class="search-field"><span>搜索指标</span><input v-model="filter" placeholder="筛选图表…" aria-label="筛选图表" type="search" /></label>
      <label class="range-field"><span>查看时长</span><select v-model.number="windowSec" aria-label="查看时长"><option v-for="range in metricWindows" :key="range.value" :value="range.value">{{ range.label }}</option></select></label>
      <button v-if="filter || category !== 'all'" class="clear-filter" @click="resetFilters">清除指标筛选</button>
    </div>
    <ChartTimeline :end="chartEnd" :window="windowSec" :notice="notice" @update:end="value => chartEnd = value" />
    <template v-if="view === 'all'">
      <nav class="domain-tabs" aria-label="指标分类">
        <button :aria-pressed="category === 'all'" @click="category = 'all'"><b>全部分类</b><span>{{ searched.length }}</span></button>
        <button v-for="domain in domains" :key="domain.id" :aria-pressed="category === domain.id" :title="domain.description" @click="category = domain.id"><b>{{ domain.title }}</b><span>{{ domain.count }}</span></button>
      </nav>
      <div ref="resultsHeading" class="results-heading" tabindex="-1">
        <div><h2>{{ selectedDomain?.title || '全部指标' }}</h2><p>{{ selectedDomain?.description || '按分类排列；每页最多展示 12 张图表。' }}</p></div>
        <p class="results-count" role="status">共 {{ filtered.length }} 张 · 显示 {{ start }}–{{ end }}</p>
      </div>
      <div v-if="!charts.length" class="empty-state" role="status"><h3>{{ currentNode?.status === 'offline' ? '节点已离线' : '暂未收到指标' }}</h3><p>{{ currentNode?.status === 'offline' ? '该节点暂无可展示的图表，请检查连接或切换节点。' : '请等待采集，或检查当前节点的采集配置。' }}</p></div>
      <div v-else-if="!filtered.length" class="empty-state" role="status"><h3>没有匹配的指标</h3><p>可搜索名称、标识、上下文和设备标签，或切换分类。</p><button @click="resetFilters">查看全部指标</button></div>
      <section v-for="section in sections" :key="section.id" class="metric-section" :aria-label="section.title + '图表'">
        <h3 v-if="category === 'all'">{{ section.title }}</h3>
        <div class="metric-grid"><MetricChart v-for="chart in section.charts" :key="node + ':' + chart.id" :chart="chart" :window="windowSec" :end="chartEnd" /></div>
      </section>
      <nav v-if="pageCount > 1" class="pagination" aria-label="指标分页">
        <button :disabled="page === 1" @click="changePage(page - 1)">上一页指标</button><span>第 {{ page }} / {{ pageCount }} 页</span><button :disabled="page === pageCount" @click="changePage(page + 1)">下一页指标</button>
      </nav>
    </template>
    <DashboardsPanel v-else :charts="charts" :window="windowSec" :end="chartEnd" :filter="filter" :node="node" />
    <CollectorStatus v-if="info && !currentNode" :info="info" />
  </div>
</template>

<style scoped>
.metrics-workspace { min-width:0; max-width:100%; } .workspace-heading { display:flex; justify-content:space-between; align-items:center; gap:16px; margin:0 0 8px; }
h1 { font-size:24px; color:#edf4fc; margin:0; letter-spacing:-.5px; }
.description { margin:0 0 18px; font-size:13px; color:#9bafc6; line-height:1.7; } .total-count { padding:7px 10px; border:1px solid #304052; border-radius:7px; white-space:nowrap; color:#c6d5e5; font-size:12px; }
button { padding:8px 12px; color:#cbd8e7; background:#152235; border:1px solid #35465c; border-radius:7px; cursor:pointer; font:inherit; font-size:13px; }
button:hover { border-color:#6b92a8; } button[aria-pressed=true] { color:#a2f5e7; border-color:#3c9f90; background:#143d3a; } button:disabled { opacity:.4; cursor:default; }
button:focus-visible, input:focus-visible, select:focus-visible { outline:2px solid #5eead4; outline-offset:3px; }
.view-switch { display:flex; gap:8px; margin-bottom:16px; flex-wrap:wrap; }
.controls { display:flex; align-items:end; gap:12px; margin-bottom:14px; flex-wrap:wrap; }
.controls label { display:flex; flex-direction:column; gap:6px; min-width:0; color:#b3c5da; font-size:12px; }
.search-field { flex:1 1 240px; } .range-field { flex:0 1 160px; }
input, select { background:#101c2b; border:1px solid #35465c; border-radius:7px; color:#e2e8f0; padding:10px; font:inherit; font-size:14px; min-width:0; width:100%; box-sizing:border-box; }
.domain-tabs { display:flex; flex-wrap:wrap; gap:8px; padding:0 0 16px; border-bottom:1px solid #27374a; }
.domain-tabs button { display:flex; gap:12px; align-items:center; justify-content:space-between; padding:10px 12px; }
.domain-tabs b { font-weight:500; } .domain-tabs span { font-size:11px; color:#9db4cc; background:#0d1825; padding:2px 6px; border-radius:5px; }
.results-heading { display:flex; align-items:center; justify-content:space-between; gap:12px; margin:20px 0 16px; scroll-margin-top:120px; }
h2 { margin:0; font-size:18px; color:#e0ebf7; } .results-heading p { margin:6px 0 0; font-size:12px; color:#91a8c0; line-height:1.7; } .results-count { white-space:nowrap; }
.metric-section { margin:0 0 20px; min-width:0; } .metric-section h3 { font-size:13px; color:#b5c9dd; margin:0 0 10px; }
.metric-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(min(390px,100%),1fr)); gap:16px; min-width:0; }
.empty-state { padding:30px 18px; text-align:center; border:1px dashed #35465c; border-radius:10px; color:#c4d4e5; line-height:1.8; }
.empty-state h3 { margin:0 0 8px; font-size:16px; } .empty-state p { font-size:13px; color:#9aafc6; margin:0 0 16px; }
.pagination { display:flex; flex-wrap:wrap; justify-content:center; align-items:center; gap:16px; margin:24px 0; font-size:13px; color:#bacadd; }
@media (max-width:760px), (pointer:coarse) { button, input, select { min-height:44px; } input, select { font-size:16px; } }
@media (max-width:760px) {
  .workspace-heading { gap:8px; margin-bottom:16px; } h1 { font-size:21px; } .description { display:none; } .total-count { font-size:11px; padding:5px 8px; }
  .controls { display:grid; grid-template-columns:minmax(0,1fr) 112px; gap:10px; } .clear-filter { grid-column:1 / -1; }
  .domain-tabs { flex-wrap:nowrap; overflow-x:auto; max-width:100%; padding-bottom:12px; gap:6px; scrollbar-width:thin; }
  .domain-tabs button { flex:none; white-space:nowrap; padding:8px 10px; gap:8px; font-size:12px; }
  .results-heading { align-items:start; flex-direction:column; gap:2px; scroll-margin-top:16px; } .metric-grid { grid-template-columns:minmax(0,1fr); gap:12px; } .pagination { gap:10px; }
}
</style>
