<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { FunctionInfo, FunctionTable } from '../api'
import { api } from '../api'

const props = defineProps<{ functions: FunctionInfo[] }>()
const emit = defineEmits<{ close: [] }>()

const selected = ref(props.functions[0]?.name ?? '')
const sort = ref('cpu')
const filter = ref('')
const table = ref<FunctionTable | null>(null)
const raw = ref('')
const error = ref('')
const updated = ref(0)
let timer: ReturnType<typeof setInterval> | undefined

function isTable(v: unknown): v is FunctionTable {
  return !!v && typeof v === 'object' && Array.isArray((v as FunctionTable).columns) && Array.isArray((v as FunctionTable).rows)
}

async function load() {
  if (!selected.value) return
  try {
    const args: Record<string, string> = {}
    if (selected.value === 'processes') args.sort = sort.value
    if (selected.value === 'services' || selected.value === 'network-connections') {
      if (sort.value) args.sort = sort.value
    }
    const r = await api.function(selected.value, args)
    if (isTable(r.result)) {
      table.value = r.result
      raw.value = ''
    } else {
      table.value = null
      raw.value = JSON.stringify(r.result, null, 2)
    }
    updated.value = r.time
    error.value = ''
  } catch (e) {
    error.value = (e as Error).message
  }
}

function rows() {
  if (!table.value) return []
  const q = filter.value.trim().toLowerCase()
  let list = table.value.rows
  if (q) list = list.filter((r) => Object.values(r).some((v) => String(v).toLowerCase().includes(q)))
  const col = sort.value
  if (col && list.length && typeof list[0]![col] === 'number') {
    list = [...list].sort((a, b) => Number(b[col] ?? 0) - Number(a[col] ?? 0))
  }
  return list
}

function fmt(col: string, v: unknown): string {
  if (v === null || v === undefined) return ''
  if (typeof v !== 'number') return String(v)
  if (col === 'rss' || col === 'memory') return v >= 1 << 30 ? (v / (1 << 30)).toFixed(2) + ' GiB' : (v / (1 << 20)).toFixed(1) + ' MiB'
  if (col === 'cpu') return v.toFixed(1) + '%'
  if (col === 'time' || col === 'rtt' || col === 'latency') return v.toFixed(1) + ' ms'
  return Number.isInteger(v) ? String(v) : v.toFixed(2)
}

function sortable(col: string) {
  const row = table.value?.rows[0]
  return !!row && typeof row[col] === 'number'
}

watch([selected, sort], load)
onMounted(() => {
  load()
  timer = setInterval(load, 2000)
})
onBeforeUnmount(() => clearInterval(timer))
</script>

<template>
  <div class="panel">
    <div class="head">
      <h3>
        Functions
        <select v-model="selected">
          <option v-for="f in functions" :key="f.name" :value="f.name" :title="f.help">{{ f.name }}</option>
        </select>
        <small v-if="table">{{ rows().length }} / {{ table.total }}</small>
        <small v-if="updated"> · {{ new Date(updated * 1000).toLocaleTimeString() }}</small>
      </h3>
      <input v-model="filter" placeholder="筛选…" />
      <button class="x" @click="emit('close')" title="关闭">×</button>
    </div>
    <div v-if="error" class="err">{{ error }}</div>
    <div v-if="table" class="scroll">
      <table>
        <thead>
          <tr>
            <th v-for="c in table.columns" :key="c" :class="{ num: typeof table.rows[0]?.[c] === 'number', sortable: sortable(c), on: sort === c }"
              @click="sortable(c) && (sort = c)">{{ c }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(r, i) in rows()" :key="(r.pid as number) ?? i">
            <td v-for="c in table.columns" :key="c" :class="{ num: typeof r[c] === 'number', cmd: c === 'cmdline' }" :title="String(r[c] ?? '')">
              {{ fmt(c, r[c]) }}
            </td>
          </tr>
          <tr v-if="!rows().length"><td :colspan="table.columns.length" class="dim">无数据</td></tr>
        </tbody>
      </table>
    </div>
    <pre v-else-if="raw">{{ raw }}</pre>
  </div>
</template>

<style scoped>
.panel { background: #0f172a; border: 1px solid #334155; border-radius: 8px; padding: 12px 14px; margin-bottom: 14px; font-size: 13px; }
.head { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
h3 { margin: 0; font-size: 14px; flex: 1; display: flex; align-items: center; gap: 8px; }
h3 small { color: #64748b; font-weight: 400; }
select, input { background: #1e293b; color: #e2e8f0; border: 1px solid #334155; border-radius: 6px; padding: 3px 6px; font-size: 12px; }
.x { background: none; border: none; color: #94a3b8; font-size: 18px; cursor: pointer; }
.err { color: #fca5a5; margin-bottom: 8px; }
.scroll { max-height: 420px; overflow: auto; }
table { width: 100%; border-collapse: collapse; }
th { text-align: left; color: #64748b; font-weight: 500; font-size: 11px; padding: 4px 6px; border-bottom: 1px solid #1e293b; position: sticky; top: 0; background: #0f172a; white-space: nowrap; }
th.sortable { cursor: pointer; }
th.on { color: #e2e8f0; }
td { padding: 3px 6px; border-bottom: 1px solid #111827; white-space: nowrap; }
.num { text-align: right; font-variant-numeric: tabular-nums; }
.cmd { max-width: 420px; overflow: hidden; text-overflow: ellipsis; color: #94a3b8; }
.dim { color: #64748b; }
pre { margin: 0; max-height: 420px; overflow: auto; font-size: 12px; }
@media (max-width: 760px) { .cmd { display: none; } }
</style>
