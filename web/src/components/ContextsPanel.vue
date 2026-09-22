<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api } from '../api'

export interface ContextInfo {
  family: string; title: string; units: string; charts: string[]; dimensions: string[]; priority: number
}
const emit = defineEmits<{ close: []; pick: [id: string] }>()
const rows = ref<{ id: string; info: ContextInfo }[]>([])
const error = ref('')
const q = ref('')

async function load() {
  try {
    const r = await api.contexts()
    rows.value = Object.entries(r.contexts ?? {}).map(([id, info]) => ({ id, info }))
      .sort((a, b) => (a.info.priority ?? 0) - (b.info.priority ?? 0) || a.id.localeCompare(b.id))
    error.value = ''
  } catch (e) {
    error.value = (e as Error).message
  }
}

onMounted(load)

function shown() {
  const s = q.value.trim().toLowerCase()
  if (!s) return rows.value
  return rows.value.filter((r) => (r.id + ' ' + r.info.title + ' ' + r.info.family).toLowerCase().includes(s))
}
</script>

<template>
  <div class="panel">
    <div class="head">
      <h3>Contexts <small>{{ rows.length }}</small></h3>
      <input v-model="q" placeholder="筛选 context…" />
      <button class="x" @click="emit('close')" title="关闭">×</button>
    </div>
    <div v-if="error" class="err">{{ error }}</div>
    <table v-else>
      <thead>
        <tr><th>Context</th><th>Family</th><th class="num">图</th><th>单位</th></tr>
      </thead>
      <tbody>
        <tr v-for="r in shown()" :key="r.id" @click="emit('pick', r.id)">
          <td>{{ r.info.title || r.id }} <span class="dim">{{ r.id }}</span></td>
          <td class="dim">{{ r.info.family }}</td>
          <td class="num">{{ r.info.charts?.length ?? 0 }}</td>
          <td class="dim">{{ r.info.units }}</td>
        </tr>
        <tr v-if="!shown().length"><td colspan="4" class="dim">暂无 context</td></tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.panel { background: #0f172a; border: 1px solid #334155; border-radius: 8px; padding: 12px 14px; margin-bottom: 14px; font-size: 13px; }
.head { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
h3 { margin: 0; font-size: 14px; flex: 1; }
h3 small { color: #64748b; font-weight: 400; }
input { background: #1e293b; color: #e2e8f0; border: 1px solid #334155; border-radius: 6px; padding: 3px 8px; font-size: 12px; }
.x { background: none; border: none; color: #94a3b8; font-size: 18px; cursor: pointer; }
.err { color: #fca5a5; }
table { width: 100%; border-collapse: collapse; }
th, td { text-align: left; padding: 4px 6px; border-bottom: 1px solid #1e293b; }
th { color: #64748b; font-size: 11px; font-weight: 500; }
.num { text-align: right; font-variant-numeric: tabular-nums; }
.dim { color: #64748b; font-size: 11px; margin-left: 6px; }
tr { cursor: pointer; }
tr:hover { background: #1e293b; }
</style>
