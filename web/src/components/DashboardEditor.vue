<script setup lang="ts">
import { computed, ref } from 'vue'
import type { Chart } from '../api'
import { groupOptions, type PersonalBoard } from '../dashboardConfig'
const props = defineProps<{ initial: PersonalBoard; charts: Chart[]; error?: string }>()
const emit = defineEmits<{ save: [board: PersonalBoard]; cancel: []; 'save-copy': [board: PersonalBoard] }>()
const draft = ref<PersonalBoard>(JSON.parse(JSON.stringify(props.initial)))
const groupSearch = ref('')
const chartSearch = ref('')
const matchingGroups = computed(() => groupOptions.filter(g => `${g.title} ${g.id}`.toLowerCase().includes(groupSearch.value.toLowerCase())))
const matchingCharts = computed(() => props.charts.filter(c => `${c.id} ${c.title}`.toLowerCase().includes(chartSearch.value.toLowerCase())))
function move(list: string[], index: number, delta: number) {
  const next = index + delta
  if (next >= 0 && next < list.length) [list[index], list[next]] = [list[next]!, list[index]!]
}
</script>
<template>
  <form class="editor" aria-label="个人看板编辑器" @submit.prevent="emit('save', draft)">
    <h2>配置个人看板</h2>
    <p>保存在当前浏览器，仅包含看板配置。最多 20 个看板、每个 30 个分组及 100 张指定图表。</p>
    <label>看板名称<input v-model="draft.title" required maxlength="100" /></label>
    <label>看板说明<textarea v-model="draft.description" maxlength="500" /></label>
    <div class="settings">
      <label>默认时间范围<select v-model.number="draft.windowSec"><option :value="0">跟随顶部时间</option><option :value="60">1 分钟</option><option :value="300">5 分钟</option><option :value="900">15 分钟</option><option :value="3600">1 小时</option><option :value="21600">6 小时</option><option :value="86400">1 天</option><option :value="604800">7 天</option></select></label>
      <label>图表列数<select v-model.number="draft.columns"><option :value="0">自适应</option><option :value="1">1 列</option><option :value="2">2 列</option><option :value="3">3 列</option></select></label>
      <label>每组默认显示<select v-model.number="draft.limit"><option v-for="n in [2,4,8,12]" :key="n" :value="n">{{ n }} 张</option></select></label>
    </div>
    <label class="check"><input v-model="draft.hideEmpty" type="checkbox" />隐藏未采集的分组</label>
    <fieldset><legend>自动匹配指标分组（{{ draft.groupIds.length }}/30）</legend>
      <input v-model="groupSearch" aria-label="搜索可选分组" placeholder="搜索分组" type="search" />
      <div class="choices"><label v-for="g in matchingGroups" :key="g.id" class="check"><input v-model="draft.groupIds" type="checkbox" :value="g.id" />{{ g.title }}</label></div>
      <ol aria-label="已选分组顺序"><li v-for="(id, i) in draft.groupIds" :key="id"><span>{{ groupOptions.find(g => g.id === id)?.title }}</span><button type="button" :disabled="i === 0" :aria-label="`上移分组 ${id}`" @click="move(draft.groupIds, i, -1)">↑</button><button type="button" :disabled="i === draft.groupIds.length-1" :aria-label="`下移分组 ${id}`" @click="move(draft.groupIds, i, 1)">↓</button><button type="button" :aria-label="`移除分组 ${id}`" @click="draft.groupIds.splice(i, 1)">移除</button></li></ol>
    </fieldset>
    <fieldset><legend>指定图表（{{ draft.chartIds.length }}/100）</legend>
      <input v-model="chartSearch" aria-label="搜索可选图表" placeholder="搜索当前节点的图表 ID 或名称" type="search" />
      <p>{{ matchingCharts.length }} 张匹配，最多展示前 50 张，可搜索缩小范围。切换节点后按相同 ID 匹配。</p>
      <div class="choices"><label v-for="c in matchingCharts.slice(0,50)" :key="c.id" class="check"><input v-model="draft.chartIds" type="checkbox" :value="c.id" />{{ c.id }} · {{ c.title }}</label></div>
      <ol aria-label="已选图表顺序"><li v-for="(id,i) in draft.chartIds" :key="id"><span>{{ id }}{{ charts.some(c => c.id === id) ? '' : '（当前节点未采集）' }}</span><button type="button" :disabled="i === 0" :aria-label="`上移图表 ${id}`" @click="move(draft.chartIds, i, -1)">↑</button><button type="button" :disabled="i === draft.chartIds.length-1" :aria-label="`下移图表 ${id}`" @click="move(draft.chartIds, i, 1)">↓</button><button type="button" :aria-label="`移除图表 ${id}`" @click="draft.chartIds.splice(i,1)">移除</button></li></ol>
    </fieldset>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <div class="actions"><button type="submit">保存看板</button><button type="button" @click="emit('save-copy', draft)">另存为新看板</button><button type="button" @click="emit('cancel')">取消编辑</button></div>
  </form>
</template>
<style scoped>
.editor { margin:20px 0; padding:20px; border:1px solid #2dd4bf; border-radius:12px; background:#0f172a; color:#e2e8f0; }
h2 { margin-top:0; } p { color:#94a3b8; font-size:13px; }
label { display:flex; flex-direction:column; gap:8px; margin:10px 0; }
input, textarea, select, button { font:inherit; background:#172337; color:#e2e8f0; border:1px solid #475569; border-radius:6px; padding:8px; min-width:0; }
button { cursor:pointer; } button:disabled { opacity:.35; cursor:default; } :focus-visible { outline:2px solid #5eead4; }
.settings { display:grid; grid-template-columns:repeat(auto-fit,minmax(160px,1fr)); gap:12px; }
.check { flex-direction:row; align-items:center; overflow-wrap:anywhere; } input[type=checkbox] { flex-shrink:0; }
fieldset { min-width:0; border:1px solid #334155; border-radius:8px; margin:16px 0; } fieldset > input { box-sizing:border-box; width:100%; }
.choices { max-height:180px; overflow:auto; } ol { padding-left:22px; } li { margin:8px 0; overflow-wrap:anywhere; } li span { margin-right:8px; } li button { margin:2px; }
.error { color:#fda4af; overflow-wrap:anywhere; }
.actions { display:flex; gap:12px; flex-wrap:wrap; }
</style>
