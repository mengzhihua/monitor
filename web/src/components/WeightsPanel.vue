<script setup lang="ts">
import { onMounted, ref } from 'vue'
import type { Weight } from '../api'
import { api } from '../api'

const emit = defineEmits<{ close: []; pick: [chart: string] }>()
const method = ref('anomaly-rate')
const highlight = ref('')
const after = ref(-300)
const before = ref(0)
const baseline = ref(-3600)
const rows = ref<Weight[]>([])
const error = ref('')

async function load() {
  try {
    const extra: Record<string, string | number> = {}
    if (method.value === 'ks2' || method.value === 'volume') {
      extra.after = after.value
      extra.before = before.value
      extra.baseline = baseline.value
    }
    const r = await api.weights(method.value, extra)
    rows.value = r.weights ?? []
    highlight.value = rows.value[0]?.chart ?? ''
    error.value = ''
  } catch (e) {
    error.value = (e as Error).message
  }
}

onMounted(load)
</script>

<template>
  <div class="panel">
    <div class="head">
      <h3>
        异常顾问
        <select v-model="method" @change="load">
          <option value="anomaly-rate">anomaly-rate</option>
          <option value="kmeans">kmeans</option>
          <option value="ks2">ks2</option>
          <option value="volume">volume</option>
        </select>
        <template v-if="method === 'ks2' || method === 'volume'">
          <input v-model.number="after" type="number" title="故障窗 after" style="width:72px" @change="load" />
          <input v-model.number="baseline" type="number" title="基线 after" style="width:72px" @change="load" />
        </template>
        <small>{{ rows.length }}</small>
      </h3>
      <button class="x" @click="emit('close')" title="关闭">×</button>
    </div>
    <div v-if="error" class="err">{{ error }}</div>
    <table v-else>
      <thead>
        <tr><th>图表</th><th class="num">分数</th></tr>
      </thead>
      <tbody>
        <tr v-for="w in rows" :key="w.chart" :class="{ hi: w.chart === highlight }" @click="emit('pick', w.chart)">
          <td>{{ w.title || w.chart }} <span class="dim">{{ w.chart }}</span></td>
          <td class="num">{{ w.score.toFixed(3) }}</td>
        </tr>
        <tr v-if="!rows.length"><td colspan="2" class="dim">暂无异常（ks2/volume 需要足够历史样本）</td></tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.panel { background: #0f172a; border: 1px solid #334155; border-radius: 8px; padding: 12px 14px; margin-bottom: 14px; font-size: 13px; }
.head { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
h3 { margin: 0; font-size: 14px; flex: 1; display: flex; align-items: center; gap: 8px; }
h3 small { color: #64748b; font-weight: 400; }
select { background: #1e293b; color: #e2e8f0; border: 1px solid #334155; border-radius: 6px; padding: 3px 6px; font-size: 12px; }
.x { background: none; border: none; color: #94a3b8; font-size: 18px; cursor: pointer; }
.err { color: #fca5a5; }
table { width: 100%; border-collapse: collapse; }
th, td { text-align: left; padding: 4px 6px; border-bottom: 1px solid #1e293b; }
th { color: #64748b; font-size: 11px; font-weight: 500; }
.num { text-align: right; font-variant-numeric: tabular-nums; }
.dim { color: #64748b; font-size: 11px; margin-left: 6px; }
tr { cursor: pointer; }
tr.hi { background: #1e3a5f; }
tr:hover { background: #1e293b; }
</style>
