<script setup lang="ts">
import { ref, shallowRef } from 'vue'
import { usePolling } from '../polling'
import type { FunctionTable, LogRow } from '../api'
import { api } from '../api'

const emit = defineEmits<{ close: [] }>()
const query = ref('')
const source = ref('')
const table = shallowRef<FunctionTable | null>(null)
const error = ref('')
const updated = ref(0)

async function load(signal: AbortSignal) {
  try {
    const args: Record<string, string> = { limit: '200' }
    if (query.value.trim()) args.query = query.value.trim()
    if (source.value) args.source = source.value
    const r = await api.logs(args, signal)
    if (signal.aborted) return
    const res = r.result as FunctionTable
    if (res && Array.isArray(res.rows)) table.value = res
    updated.value = r.time
    error.value = ''
  } catch (e) {
    if (signal.aborted) return
    error.value = (e as Error).message
  }
}

function when(t: number) {
  if (!t) return ''
  return new Date(t * 1000).toLocaleTimeString()
}

function priClass(p: string) {
  if (p === 'emerg' || p === 'alert' || p === 'crit' || p === 'err') return 'err'
  if (p === 'warning') return 'warn'
  return ''
}

const refresh = usePolling(load, 4000)
</script>

<template>
  <div class="panel">
    <div class="head">
      <h3>
        日志
        <small v-if="table">{{ table.total }}</small>
        <small v-if="updated"> · {{ new Date(updated * 1000).toLocaleTimeString() }}</small>
      </h3>
      <select v-model="source" @change="refresh">
        <option value="">自动</option>
        <option value="journal">journald</option>
        <option value="file">文件</option>
        <option value="eventlog">Event Log</option>
      </select>
      <input v-model="query" placeholder="筛选…" @keyup.enter="refresh" />
      <button class="x" @click="emit('close')" title="关闭">×</button>
    </div>
    <div v-if="error" class="err">{{ error }}</div>
    <div v-else class="scroll">
      <table>
        <thead>
          <tr><th>时间</th><th>级别</th><th>单元</th><th>消息</th></tr>
        </thead>
        <tbody>
          <tr v-for="(r, i) in (table?.rows as LogRow[] | undefined) ?? []" :key="i" :class="priClass(r.priority)">
            <td class="dim">{{ when(r.time) }}</td>
            <td>{{ r.priority }}</td>
            <td class="dim">{{ r.unit }}<span v-if="r.pid">[{{ r.pid }}]</span></td>
            <td class="msg" :title="r.message">{{ r.message }}</td>
          </tr>
          <tr v-if="!table?.rows?.length"><td colspan="4" class="dim">暂无日志</td></tr>
        </tbody>
      </table>
    </div>
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
th { text-align: left; color: #64748b; font-weight: 500; font-size: 11px; padding: 4px 6px; border-bottom: 1px solid #1e293b; position: sticky; top: 0; background: #0f172a; }
td { padding: 3px 6px; border-bottom: 1px solid #111827; }
.msg { max-width: 720px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dim { color: #94a3b8; }
tr.err td { color: #fca5a5; }
tr.warn td { color: #fde68a; }
</style>
