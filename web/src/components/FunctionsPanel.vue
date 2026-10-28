<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { usePolling } from '../polling'
import { pageVisible } from '../visibility'
import type { FunctionInfo, FunctionTable } from '../api/monitoring/types'
import { monitoringApi } from '../api/monitoring/client'
import { auth } from '../api/auth'
import { functionRequest, isPluginFunction, pluginRequestTimeout } from '../features/functions/model/functionPolicy'

const props = defineProps<{ functions: FunctionInfo[] }>()
const emit = defineEmits<{ close: [] }>()

const selected = ref(props.functions[0]?.name ?? '')
const current = computed(() => props.functions.find(item => item.name === selected.value))
const onDemand = computed(() => isPluginFunction(selected.value))
const sort = ref('cpu')
const filter = ref('')
const table = shallowRef<FunctionTable | null>(null)
const raw = ref('')
const error = ref('')
const updated = ref(0)
const busy = ref(false)
let manualRequest: AbortController | undefined
let disposed = false

function clearResult() { table.value = null; raw.value = ''; updated.value = 0 }
function cancelExecution() {
  manualRequest?.abort()
  manualRequest = undefined
  busy.value = false
}

function isTable(v: unknown): v is FunctionTable {
  return !!v && typeof v === 'object' && Array.isArray((v as FunctionTable).columns) && Array.isArray((v as FunctionTable).rows)
}

async function load(signal: AbortSignal, explicit = false) {
  const request = functionRequest(selected.value, sort.value, explicit)
  if (!request) return
  const token = auth.token
  // Reading storage may itself publish a credential change and cancel this request.
  if (disposed || signal.aborted) return
  const currentRequest = () => !disposed && !signal.aborted && selected.value === request.name && token === auth.token
  try {
    const r = await monitoringApi.function(request.name, request.args, signal)
    if (!currentRequest()) return
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
    if (!currentRequest()) return
    clearResult()
    error.value = (e as Error).message
  }
}

async function execute() {
  if (disposed || busy.value || !current.value || !onDemand.value || !pageVisible.value) return
  const controller = manualRequest = new AbortController()
  busy.value = true
  clearResult()
  error.value = ''
  let expired = false
  const deadline = window.setTimeout(() => { expired = true; controller.abort() }, pluginRequestTimeout(current.value))
  try { await load(controller.signal, true) }
  finally {
    window.clearTimeout(deadline)
    if (manualRequest === controller) {
      manualRequest = undefined
      busy.value = false
      if (expired) error.value = '插件函数执行超时，结果未确认；请核对插件状态后再决定是否重新执行。'
    }
  }
}

const rows = computed(() => {
  if (!table.value) return []
  const q = filter.value.trim().toLowerCase()
  let list = table.value.rows
  if (q) list = list.filter((r) => Object.values(r).some((v) => String(v).toLowerCase().includes(q)))
  const col = sort.value
  if (col && list.length && typeof list[0]![col] === 'number') {
    list = [...list].sort((a, b) => Number(b[col] ?? 0) - Number(a[col] ?? 0))
  }
  return list
})

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

const refresh = usePolling(load, () => onDemand.value ? 0 : 2000)
const unsubscribeAuth = auth.onChange(() => {
  cancelExecution()
  clearResult()
  error.value = ''
  void refresh()
})
watch(selected, () => {
  cancelExecution()
  clearResult()
  error.value = ''
  void refresh()
}, { flush: 'sync' })
watch(sort, () => { if (!onDemand.value) void refresh() })
watch(() => props.functions, functions => {
  if (!functions.some(fn => fn.name === selected.value)) selected.value = functions[0]?.name ?? ''
})
onBeforeUnmount(() => { disposed = true; unsubscribeAuth(); cancelExecution() })
</script>

<template>
  <section class="panel" aria-label="函数与诊断">
    <div class="head">
      <h3>
        Functions
        <select v-model="selected" aria-label="选择函数">
          <option v-for="f in functions" :key="f.name" :value="f.name" :title="f.help">{{ f.name }}</option>
        </select>
        <small v-if="table">{{ rows.length }} / {{ table.total }}</small>
        <small v-if="updated"> · {{ new Date(updated * 1000).toLocaleTimeString() }}</small>
      </h3>
      <button v-if="onDemand" type="button" :disabled="busy || !current || !pageVisible" @click="execute">{{ busy ? '执行中…' : updated ? '再执行' : '执行' }}</button>
      <input v-model="filter" placeholder="筛选…" aria-label="筛选函数结果" />
      <button class="x" @click="emit('close')" title="关闭">×</button>
    </div>
    <p v-if="current?.help" class="dim">{{ current.help }}</p>
    <p v-if="onDemand" class="dim">插件函数仅在点击“执行”时调用；排序和返回页面不会再次执行。</p>
    <p v-if="busy" role="status">正在执行插件函数…</p>
    <div v-if="error" class="err" role="alert">{{ error }}</div>
    <div v-if="table" class="scroll">
      <table>
        <thead>
          <tr>
            <th v-for="c in table.columns" :key="c" :class="{ num: typeof table.rows[0]?.[c] === 'number', sortable: sortable(c), on: sort === c }"
              @click="sortable(c) && (sort = c)">{{ c }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(r, i) in rows" :key="(r.pid as number) ?? i">
            <td v-for="c in table.columns" :key="c" :class="{ num: typeof r[c] === 'number', cmd: c === 'cmdline' }" :title="String(r[c] ?? '')">
              {{ fmt(c, r[c]) }}
            </td>
          </tr>
          <tr v-if="!rows.length"><td :colspan="table.columns.length" class="dim">无数据</td></tr>
        </tbody>
      </table>
    </div>
    <pre v-else-if="raw">{{ raw }}</pre>
  </section>
</template>

<style scoped>
.panel { background: #0f172a; border: 1px solid #334155; border-radius: 8px; padding: 12px 14px; margin-bottom: 14px; font-size: 13px; }
.head { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; margin-bottom: 8px; }
h3 { margin: 0; font-size: 14px; flex: 1; min-width: 0; display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
h3 small { color: #64748b; font-weight: 400; }
select, input, button:not(.x) { background: #1e293b; color: #e2e8f0; border: 1px solid #334155; border-radius: 6px; padding: 3px 6px; font-size: 12px; }
select { max-width:100%; }button:disabled { opacity:.5; cursor:default; }
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
