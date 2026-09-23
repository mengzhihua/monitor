<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api, ApiError } from '../api'
import type { HandlingAction, HandlingHistoryPage, HandlingHistoryRecord, HandlingStatus } from '../api'

const props = defineProps<{ activeIds: Set<string>; refreshKey: number }>()
const form = ref({ q: '', assignee: '', actor: '', severity: '', status: '', acknowledged: '', days: '' })
const appliedForm = ref('')
let applied: Record<string, string> = {}
const dirty = computed(() => JSON.stringify(form.value) !== appliedForm.value)
const page = ref<HandlingHistoryPage | null>(null)
const records = ref<HandlingHistoryRecord[]>([])
const error = ref('')
const exporting = ref(false)
const loading = ref(false)
const loadedAt = ref(0)
let queryController: AbortController | undefined
let exportController: AbortController | undefined
let disposed = false
const progressName: Record<HandlingStatus, string> = { open: '待处理', investigating: '排查中', watching: '观察中' }
const actionName: Record<string, string> = { acknowledge: '确认问题', unacknowledge: '撤销确认', comment: '补充备注', assign: '指派责任人', unassign: '取消指派', progress: '更新进度' }
const formatTime = (t: number) => t > 0 ? new Date(t * 1000).toLocaleString() : '时间未知'
function describeAction(h: HandlingAction) {
  if (h.action === 'assign' || h.action === 'unassign') return `${actionName[h.action]}：${h.previous_assignee || '未分配'} → ${h.assignee || '未分配'}`
  if (h.action === 'progress') return `更新进度：${progressName[h.previous_status || 'open']} → ${progressName[h.status || 'open']}`
  return actionName[h.action] || h.action
}
function message(e: unknown) {
  if (e instanceof ApiError && e.status === 409) return '处理记录已变化或服务已重启，请重新查询后再翻页或导出。当前列表保留供参考。'
  if (e instanceof ApiError && [401, 403].includes(e.status)) return '当前登录无法读取处理历史，请重新登录。'
  return '历史查询或导出失败，请检查连接和筛选条件后重试。'
}
async function load(more = false) {
  queryController?.abort()
  exportController?.abort()
  exporting.value = false
  const controller = queryController = new AbortController()
  loading.value = true
  error.value = ''
  try {
    const next = await api.handlingHistory({ ...applied, limit: '25', ...(more && page.value ? { cursor: page.value.next_cursor } : {}) }, controller.signal)
    if (disposed || controller.signal.aborted) return
    records.value = more ? [...records.value, ...next.records] : next.records
    page.value = next
    loadedAt.value = Math.floor(Date.now() / 1000)
  } catch (e) {
    if (disposed || controller.signal.aborted) return
    error.value = message(e)
    if (e instanceof ApiError && [401, 403].includes(e.status)) { page.value = null; records.value = [] }
  } finally { if (!disposed && !controller.signal.aborted) loading.value = false }
}
function search() {
  const { days, ...fields } = form.value
  applied = Object.fromEntries(Object.entries(fields).map(([k,v]) => [k,v.trim()]))
  if (days) applied.from = String(Math.floor(Date.now() / 1000) - Number(days) * 86400)
  appliedForm.value = JSON.stringify(form.value)
  // Never leave an old result labeled with newly submitted filter conditions.
  page.value = null
  records.value = []
  void load()
}
async function download(format: 'json' | 'csv') {
  if (!page.value || dirty.value || loading.value || exporting.value || error.value) return
  const controller = exportController = new AbortController()
  exporting.value = true
  try {
    const blob = await api.exportHandlingHistory({ ...applied, snapshot: page.value.snapshot, format }, controller.signal)
    if (disposed || controller.signal.aborted) return
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url; a.download = `monitor-handling-history-${loadedAt.value}.${format}`; a.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  } catch (e) {
    if (disposed || controller.signal.aborted) return
    error.value = message(e)
    if (e instanceof ApiError && [401, 403].includes(e.status)) { page.value = null; records.value = [] }
  } finally { if (!disposed && !controller.signal.aborted) exporting.value = false }
}
onMounted(search)
watch(() => props.refreshKey, () => { void load() })
onBeforeUnmount(() => { disposed = true; queryController?.abort(); exportController?.abort() })
</script>

<template>
  <section class="history" aria-label="处理记录查询">
    <h2>处理记录查询</h2>
    <p class="muted">检索服务端保存的全部处理阶段。时间范围按最后一次处理时间筛选，操作者匹配保留的操作记录；列表按最后处理时间倒序。</p>
    <form @submit.prevent="search" class="history-filters">
      <input v-model="form.q" aria-label="历史关键词" placeholder="搜索主机、问题或备注…" maxlength="64" class="search" />
      <input v-model="form.assignee" aria-label="历史责任人" placeholder="责任人（完整姓名）" maxlength="128" />
      <input v-model="form.actor" aria-label="历史操作者" placeholder="操作者（完整姓名）" maxlength="128" />
      <select v-model="form.severity" aria-label="历史严重级别"><option value="">全部级别</option><option value="CRITICAL">严重</option><option value="WARNING">警告</option></select>
      <select v-model="form.status" aria-label="历史处理进度"><option value="">全部进度</option><option value="open">待处理</option><option value="investigating">排查中</option><option value="watching">观察中</option></select>
      <select v-model="form.acknowledged" aria-label="历史确认状态"><option value="">全部确认状态</option><option value="true">已确认</option><option value="false">未确认</option></select>
      <select v-model="form.days" aria-label="历史时间范围"><option value="">全部时间</option><option value="1">最近 24 小时</option><option value="7">最近 7 天</option><option value="30">最近 30 天</option></select>
      <button type="submit">{{ loading ? '重新查询' : '查询历史' }}</button>
    </form>
    <p v-if="dirty" class="muted">筛选条件已修改，请先查询后导出。当前列表仍是上次查询结果。</p>
    <p v-if="loading" role="status">正在读取处理历史…</p>
    <p v-if="error" role="alert" class="notice">{{ error }}</p>
    <template v-if="page">
      <div class="history-toolbar">
        <span role="status" class="history-count">已显示 {{ records.length }} / {{ page.total }} 个处理阶段</span>
        <button :disabled="dirty || loading || exporting || !!error" @click="download('json')">导出全部匹配 JSON</button>
        <button :disabled="dirty || loading || exporting || !!error" @click="download('csv')">导出全部匹配 CSV</button>
      </div>
      <p class="muted">查询时间 {{ formatTime(loadedAt) }} · 存储 {{ page.stored }} / {{ page.capacity }} 个阶段 · 每阶段最多保留最近 {{ page.actions_per_record }} 次操作</p>
      <p v-if="page.stored >= page.capacity" class="notice">处置存储已满。请先停止服务并备份数据，再由管理员按文档归档历史；系统不会自动删除记录。</p>
      <p v-if="!records.length && !loading" class="muted">没有匹配的处理历史。</p>
      <details v-for="record in records" :key="record.id" class="history-record" :data-history-id="record.id">
        <summary>{{ record.problem.hostname }} · {{ record.problem.name }} · {{ formatTime(record.updated_at) }}<span>{{ activeIds.has(record.id) ? '当前告警阶段' : '不在当前告警快照' }}</span></summary>
        <p class="muted">{{ record.problem.chart }} · {{ record.problem.severity }} · 阶段起始 {{ formatTime(record.problem.since) }}</p>
        <p>责任人：{{ record.assignee || '未分配' }} · {{ progressName[record.status] }} · {{ record.acknowledged ? '已确认' : '未确认' }}</p>
        <p v-if="record.history_truncated" class="retention">该阶段共发生 {{ record.revision }} 次操作，仅保留最近 {{ record.history.length }} 次；更早操作不在查询或导出范围内。</p>
        <ol><li v-for="(h,i) in [...record.history].reverse()" :key="i">{{ formatTime(h.at) }} · {{ h.actor }} · {{ describeAction(h) }}<p v-if="h.note">{{ h.note }}</p></li></ol>
      </details>
      <button v-if="page.next_cursor" :disabled="loading || dirty || !!error" @click="load(true)">加载更多历史</button>
      <p class="muted">不在当前告警快照不等于已恢复。导出覆盖全部匹配阶段及其保留的操作，不受已加载页数限制；JSON 保留原文，CSV 每行一条操作并对可能被表格软件识别为公式的文本添加保护前缀。</p>
    </template>
  </section>
</template>

<style scoped>
.history { border-top:1px solid #27364c; margin-top:28px; padding-top:8px; }
h2 { font-size:17px; margin:18px 0 10px; } p { font-size:12px; line-height:1.7; } .muted { color:#94a3b8; }
.history-filters,.history-toolbar { display:flex; flex-wrap:wrap; align-items:center; gap:8px; margin:14px 0; }
.history-filters { background:#0c1526; padding:14px; border:1px solid #27364c; border-radius:10px; }
input,select,button { font:inherit; font-size:12px; color:#e2e8f0; background:#111d31; border:1px solid #334155; border-radius:7px; padding:8px 10px; max-width:100%; min-width:0; }
input { width:175px; box-sizing:border-box; }.search { flex:1; min-width:min(220px,100%); } button { cursor:pointer; }button:disabled { opacity:.5; cursor:default; }button:hover:not(:disabled) { color:#5eead4; border-color:#5eead4; }input:focus,select:focus,button:focus-visible { outline:2px solid #5eead4; outline-offset:2px; }
.history-count { font-size:12px; margin-right:auto; }.history-record { border:1px solid #27364c; background:#0c1526; border-radius:8px; padding:12px; margin:10px 0; font-size:12px; overflow-wrap:anywhere; }
summary { cursor:pointer; color:#cbd5e1; line-height:1.8; } summary span { color:#94a3b8; margin-left:12px; }.notice { background:#332b18; color:#fde68a; border:1px solid #625025; padding:12px; border-radius:8px; }.retention { color:#fcd34d; }ol { padding-left:20px; color:#94a3b8; }li { margin:10px 0; }li p { white-space:pre-wrap; color:#e2e8f0; }
@media(max-width:600px) { .history-filters input { flex:1 1 100%; }.history-toolbar button { flex:1; }.history-count { flex-basis:100%; } }
</style>
