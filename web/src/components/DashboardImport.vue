<script setup lang="ts">
import { computed, ref } from 'vue'
import type { Chart } from '../api'
import { asDashboard, type PersonalBoard } from '../dashboardConfig'
import { chartsForGroup } from '../dashboards'

const props = defineProps<{ boards: PersonalBoard[]; fileName: string; existing: PersonalBoard[]; charts: Chart[] }>()
const emit = defineEmits<{ confirm: [boards: PersonalBoard[]]; cancel: [] }>()
const selected = ref(props.boards.map(b => b.id))
const chosen = computed(() => props.boards.filter(b => selected.value.includes(b.id)))
const remaining = computed(() => Math.max(0, 20-props.existing.length))
const coverage = computed(() => Object.fromEntries(props.boards.map(b => [b.id,
  new Set(asDashboard(b).groups.flatMap(g => chartsForGroup(props.charts, g).map(c => c.id))).size,
])))
const range = (seconds: number) => seconds === 0 ? '跟随顶部时间' : seconds < 3600 ? `${seconds/60} 分钟` : seconds < 86400 ? `${seconds/3600} 小时` : `${seconds/86400} 天`
function confirm() { if (chosen.value.length && chosen.value.length <= remaining.value) emit('confirm', chosen.value) }
</script>

<template>
  <section class="import-preview" aria-label="看板导入预览">
    <h2>预览并选择要导入的看板</h2>
    <p>文件：{{ fileName }} · {{ boards.length }} 个看板。确认后保存为独立副本，同名看板也不会覆盖。</p>
    <p>已选择 {{ chosen.length }} 个 · 还可添加 {{ remaining }} 个（最多 20 个）。当前节点未采集的分组和图表 ID 仍保留，采集后自动显示。</p>
    <div class="actions"><button type="button" @click="selected = boards.map(b => b.id)">全选待导入看板</button><button type="button" @click="selected = []">清空导入选择</button></div>
    <div class="entries">
      <label v-for="b in boards" :key="b.id" class="entry">
        <input v-model="selected" type="checkbox" :value="b.id" :aria-label="`导入 ${b.title}`" />
        <span><b>{{ b.title }}</b><small v-if="b.description">{{ b.description }}</small>
          <small>{{ b.groupIds.length }} 个分组 · {{ b.chartIds.length }} 张指定图表 · 当前节点匹配 {{ coverage[b.id] }} 张图表</small>
          <small>{{ range(b.windowSec) }} · {{ b.columns || '自适应' }} 列 · 每组 {{ b.limit }} 张 · {{ b.hideEmpty ? '隐藏' : '显示' }}未采集分组</small>
          <small v-if="existing.some(item => item.title === b.title)">已有同名看板，将新增副本。</small>
        </span>
      </label>
    </div>
    <p v-if="chosen.length > remaining" role="alert">选择数量超过剩余容量，请减少勾选或先删除不再使用的个人看板。</p>
    <div class="actions"><button type="button" :disabled="!chosen.length || chosen.length > remaining" @click="confirm">确认导入所选看板</button><button type="button" @click="emit('cancel')">取消导入</button></div>
  </section>
</template>

<style scoped>
.import-preview { margin:20px 0; padding:20px; border:1px solid #2dd4bf; border-radius:12px; background:#0f172a; min-width:0; overflow-wrap:anywhere; } h2 { margin-top:0; font-size:18px; } p, small { color:#94a3b8; font-size:13px; line-height:1.6; }
.entries { max-height:420px; overflow:auto; margin:12px 0; } .entry { display:flex; gap:10px; align-items:flex-start; padding:12px 4px; border-bottom:1px solid #334155; cursor:pointer; } .entry input { margin-top:5px; flex-shrink:0; } .entry span { min-width:0; } small { display:block; } .actions { display:flex; flex-wrap:wrap; gap:10px; }
button { font:inherit; padding:8px 12px; border:1px solid #475569; border-radius:6px; background:#172337; color:#e2e8f0; cursor:pointer; } button:disabled { opacity:.4; cursor:default; } :focus-visible { outline:2px solid #5eead4; outline-offset:2px; } [role=alert] { color:#fda4af; }
</style>
