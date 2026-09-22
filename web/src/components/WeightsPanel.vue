<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import type { Weight } from '../api'
import { api } from '../api'

const emit = defineEmits<{ close: []; pick: [chart: string] }>()
const method = ref('anomaly-rate')
const group = ref('chart')
const top = ref(50)
const highlight = ref('')
const after = ref(-300)
const before = ref(0)
const baselineAfter = ref(-3600)
const baselineBefore = ref(-300)
const rows = ref<Weight[]>([])
const error = ref('')

const correlating = computed(() => method.value === 'ks2' || method.value === 'volume')
const maxScore = computed(() => Math.max(0, ...rows.value.map((w) => w.score)))

async function load() {
  try {
    const extra: Record<string, string | number> = { top: top.value }
    if (correlating.value) {
      extra.after = after.value
      extra.before = before.value
      extra.baseline_after = baselineAfter.value
      extra.baseline_before = baselineBefore.value
      extra.group = group.value
    }
    const r = await api.weights(method.value, extra)
    rows.value = r.weights ?? []
    highlight.value = rows.value[0]?.chart ?? rows.value[0]?.context ?? ''
    error.value = ''
  } catch (e) {
    error.value = (e as Error).message
  }
}

function pick(w: Weight) {
  const id = group.value === 'context' ? (w.context || w.chart) : w.chart
  highlight.value = id
  emit('pick', id)
}

function barWidth(score: number) {
  if (!maxScore.value) return '0%'
  return `${Math.max(4, Math.round((100 * score) / maxScore.value))}%`
}

onMounted(load)
</script>

<template>
  <div class="panel">
    <div class="head">
      <h3>
        关联分析
        <select v-model="method" @change="load">
          <option value="anomaly-rate">anomaly-rate</option>
          <option value="kmeans">kmeans</option>
          <option value="ks2">ks2</option>
          <option value="volume">volume</option>
        </select>
        <select v-if="correlating" v-model="group" @change="load" title="分组">
          <option value="chart">chart</option>
          <option value="context">context</option>
          <option value="dimension">dimension</option>
        </select>
        <input v-model.number="top" type="number" min="5" max="200" title="top N" style="width:56px" @change="load" />
        <template v-if="correlating">
          <input v-model.number="after" type="number" title="故障窗 after" style="width:72px" @change="load" />
          <input v-model.number="before" type="number" title="故障窗 before" style="width:56px" @change="load" />
          <input v-model.number="baselineAfter" type="number" title="基线 after" style="width:72px" @change="load" />
          <input v-model.number="baselineBefore" type="number" title="基线 before" style="width:72px" @change="load" />
        </template>
        <small>{{ rows.length }}</small>
      </h3>
      <button class="x" @click="emit('close')" title="关闭">×</button>
    </div>
    <p v-if="correlating" class="hint">故障窗 [after, before] 对比基线 [baseline_after, baseline_before]；点击一行筛选对应图表。</p>
    <div v-if="error" class="err">{{ error }}</div>
    <table v-else>
      <thead>
        <tr>
          <th>名称</th>
          <th v-if="group === 'dimension'">维</th>
          <th class="num">分数</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="w in rows" :key="(w.chart || '') + '/' + (w.dimension || w.context || '')" :class="{ hi: (w.chart || w.context) === highlight }" @click="pick(w)">
          <td>
            <div class="bar" :style="{ width: barWidth(w.score) }"></div>
            {{ w.title || w.chart }} <span class="dim">{{ w.chart }}{{ w.context && w.context !== w.chart ? ' · ' + w.context : '' }}</span>
          </td>
          <td v-if="group === 'dimension'" class="dim">{{ w.dimension }}</td>
          <td class="num">{{ w.score.toFixed(3) }}</td>
        </tr>
        <tr v-if="!rows.length"><td :colspan="group === 'dimension' ? 3 : 2" class="dim">暂无异常（ks2/volume 需要足够历史样本）</td></tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.panel { background: #0f172a; border: 1px solid #334155; border-radius: 8px; padding: 12px 14px; margin-bottom: 14px; font-size: 13px; }
.head { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
h3 { margin: 0; font-size: 14px; flex: 1; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
h3 small { color: #64748b; font-weight: 400; }
select, input { background: #1e293b; color: #e2e8f0; border: 1px solid #334155; border-radius: 6px; padding: 3px 6px; font-size: 12px; }
.x { background: none; border: none; color: #94a3b8; font-size: 18px; cursor: pointer; }
.err { color: #fca5a5; }
.hint { margin: 0 0 8px; color: #64748b; font-size: 11px; }
table { width: 100%; border-collapse: collapse; }
th, td { text-align: left; padding: 4px 6px; border-bottom: 1px solid #1e293b; position: relative; }
th { color: #64748b; font-size: 11px; font-weight: 500; }
.num { text-align: right; font-variant-numeric: tabular-nums; }
.dim { color: #64748b; font-size: 11px; margin-left: 6px; }
tr { cursor: pointer; }
tr.hi { background: #1e3a5f; }
tr:hover { background: #1e293b; }
.bar { position: absolute; left: 0; top: 4px; bottom: 4px; background: #1d4ed8; opacity: .35; border-radius: 3px; pointer-events: none; }
</style>
