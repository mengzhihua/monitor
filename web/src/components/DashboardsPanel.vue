<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { Chart } from '../api'
import { dashboards, chartsForGroup } from '../dashboards'
import MetricChart from './MetricChart.vue'

const props = defineProps<{ charts: Chart[]; window: number; filter: string; node: string }>()
const selected = ref('developer')
const expanded = ref<string[]>([])
const board = computed(() => dashboards.find(b => b.id === selected.value)!)
const groups = computed(() => board.value.groups.map(group => {
  const matched = chartsForGroup(props.charts, group)
  const query = props.filter.trim().toLowerCase()
  const filtered = matched.filter(c => !query || `${c.id} ${c.title} ${c.family}`.toLowerCase().includes(query))
  return { ...group, matched, filtered, visible: expanded.value.includes(group.id) ? filtered : filtered.slice(0, 4) }
}))
const count = computed(() => new Set(groups.value.flatMap(g => g.matched.map(c => c.id))).size)
const available = computed(() => groups.value.filter(g => g.matched.length).length)
watch([selected, () => props.node, () => props.filter], () => { expanded.value = [] })
</script>

<template>
  <div class="dashboards" aria-label="常用聚合看板">
    <div class="intro"><div><span class="eyebrow">开箱即用 · 自动匹配当前节点</span><h1>常用聚合看板</h1></div><span class="badge">6 个内置样板</span></div>
    <p class="muted">无需选指标或编写查询。样板直接使用当前节点已有采集数据，切换节点后自动更新。</p>
    <div class="presets" aria-label="选择看板">
      <button v-for="item in dashboards" :key="item.id" :aria-pressed="selected === item.id" @click="selected = item.id">
        <b>{{ item.title }}</b><span>{{ item.description }}</span>
      </button>
    </div>
    <div class="board-heading"><h2>{{ board.title }}</h2><span>{{ available }}/{{ groups.length }} 类指标已采集 · {{ count }} 张图表</span></div>
    <p class="muted">{{ board.description }} 各图保留原始单位；不同指标不混算。</p>
    <section v-for="group in groups" :key="group.id" class="board-group" :aria-label="group.title">
      <div class="group-heading"><h3>{{ group.title }}</h3><span>{{ group.filtered.length }} 张图表</span></div>
      <p class="muted">{{ group.hint }}</p>
      <div v-if="!group.matched.length" class="missing">当前节点尚未采集这类指标。已有采集数据接入后会自动展示，无需配置看板。</div>
      <div v-else-if="!group.filtered.length" class="missing">没有匹配当前筛选条件的图表，请清空或修改顶部筛选。</div>
      <div v-else class="board-grid">
        <MetricChart v-for="chart in group.visible" :key="node + ':' + chart.id" :chart="chart" :window="window" />
      </div>
      <button v-if="group.filtered.length > 4" class="more" @click="expanded = expanded.includes(group.id) ? expanded.filter(id => id !== group.id) : [...expanded, group.id]">
        {{ expanded.includes(group.id) ? '收起' : `展开其余 ${group.filtered.length - 4} 张图表` }}
      </button>
    </section>
  </div>
</template>

<style scoped>
.dashboards { max-width: 1800px; margin: 0 auto; }
.intro, .board-heading, .group-heading { display:flex; align-items:center; justify-content:space-between; gap:12px; flex-wrap:wrap; }
h1 { font-size:24px; margin:6px 0; } h2 { font-size:20px; margin:0; } h3 { font-size:16px; margin:0; }
.eyebrow { color:#5eead4; font-size:12px; } .badge { background:#134e4a; color:#99f6e4; border-radius:20px; padding:5px 12px; }
.muted, .board-heading span, .group-heading span { color:#94a3b8; font-size:13px; }
.presets { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:10px; margin:20px 0 28px; }
button { cursor:pointer; font:inherit; color:#e2e8f0; border:1px solid #334155; background:#0f172a; border-radius:10px; }
.presets button { text-align:left; padding:16px; display:flex; flex-direction:column; gap:8px; }
.presets button span { font-size:12px; color:#94a3b8; line-height:1.6; }
.presets button[aria-pressed=true] { border-color:#2dd4bf; background:#102d32; }
button:focus-visible { outline:2px solid #5eead4; outline-offset:3px; }
.board-group { margin:20px 0; padding-top:16px; border-top:1px solid #1e293b; }
.board-grid { display:grid; grid-template-columns:repeat(auto-fill,minmax(min(420px,100%),1fr)); gap:12px; }
.missing { padding:18px; border:1px dashed #334155; border-radius:8px; color:#94a3b8; font-size:13px; }
.more { padding:7px 14px; margin-top:12px; }
@media(max-width:900px) { .presets { grid-template-columns:repeat(2,minmax(0,1fr)); } }
@media(max-width:480px) { .presets button { padding:12px; } h1 { font-size:21px; } }
</style>
