<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, ref, watch } from 'vue'
import { api, ApiError } from '../api'
import type { HandlingAction, HandlingChange, HandlingStatus, OperationsSnapshot, OperationsView, Problem } from '../api'
import { usePolling } from '../polling'
import OperationsNodes from '../features/operations/OperationsNodes.vue'
import OperationsNavigation from '../features/operations/OperationsNavigation.vue'
import { useOperationsSection, type OperationsSection } from '../features/operations/useOperationsSection'
const OperationsHistory = defineAsyncComponent(() => import('./OperationsHistory.vue'))
import OperationsViews from './OperationsViews.vue'
import OperationsBatch from './OperationsBatch.vue'
const NotificationDiagnostics = defineAsyncComponent(() => import('./NotificationDiagnostics.vue'))
const MaintenancePlans = defineAsyncComponent(() => import('./MaintenancePlans.vue'))
import AppIcon from './AppIcon.vue'

const props = defineProps<{ role: string }>()
const emit = defineEmits<{ drill: [node: string, chart: string]; solve: [problem: Problem] }>()
const { section, selectSection } = useOperationsSection()
const maintenancePanel = ref<InstanceType<typeof MaintenancePlans> | null>(null)
const viewsPanel = ref<InstanceType<typeof OperationsViews> | null>(null)
const viewDraftName = ref('')
function changeSection(value: OperationsSection) {
  if (section.value === value) return
  if (viewsPanel.value?.isSaving()) return
  if (section.value === 'maintenance' && maintenancePanel.value?.canLeave() === false) return
  selectSection(value)
}
const snapshot = ref<OperationsSnapshot | null>(null)
const error = ref('')
const actionError = ref('')
const busy = ref('')
const loading = ref(true)
const historyRefreshKey = ref(0)
const query = ref('')
const severity = ref('all')
const nodeStatus = ref('all')
const pendingOnly = ref(false)
const ownerFilter = ref('all')
const progressFilter = ref('all')
const moreFilters = ref(false)
const advancedFilterCount = computed(() => Number(nodeStatus.value !== 'all') + (section.value === 'problems'
  ? Number(ownerFilter.value !== 'all') + Number(progressFilter.value !== 'all') + Number(pendingOnly.value) : 0))
const activeFilterCount = computed(() => advancedFilterCount.value + Number(!!query.value.trim())
  + (section.value === 'problems' ? Number(severity.value !== 'all') : 0))
function clearFilters() {
  query.value = ''; severity.value = 'all'; nodeStatus.value = 'all'
  ownerFilter.value = 'all'; progressFilter.value = 'all'; pendingOnly.value = false
}
const notes = ref<Record<string, string>>({})
const owners = ref<Record<string, string>>({})
const progress = ref<Record<string, HandlingStatus>>({})
const draftRevisions = ref<Record<string, number>>({})
const visibleCount = ref(50)
const selectedProblems = ref<Problem[]>([])
const batchError = ref('')
const batchMessage = ref('')
const batchCompleted = ref(0)
const batchAction = ref<HandlingChange['action']>('acknowledge')
const batchAssignee = ref('')
const batchStatus = ref<HandlingStatus>('investigating')
const batchNote = ref('')
function canLeave() {
  if (busy.value || maintenancePanel.value?.isBusy() || viewsPanel.value?.isSaving()) return false
  const byID = new Map(snapshot.value?.problems.map(p => [p.id, p]))
  const dirty = Object.values(notes.value).some(note => !!note.trim())
    || Object.entries(owners.value).some(([id, owner]) => owner !== byID.get(id)?.handling.assignee)
    || Object.entries(progress.value).some(([id, state]) => state !== byID.get(id)?.handling.status)
    || !!batchNote.value.trim() || !!viewDraftName.value.trim() || maintenancePanel.value?.isDirty()
  return !dirty || confirm('仍有未提交的问题处理、个人视图或维护草稿，离开运维总览将丢弃这些内容，确定继续？')
}
defineExpose({ canLeave })
const exporting = ref(false)
const exportingDiagnostic = ref(false)
const exportError = ref('')
const canExportDiagnostic = computed(() => props.role === 'admin' || props.role === 'troubleshooter')
let overviewRequest: AbortController | undefined
let exportRequest: AbortController | undefined
let diagnosticRequest: AbortController | undefined
let searchTimer: number | undefined
function cancelExport() {
  exportRequest?.abort()
  exportRequest = undefined
  exporting.value = false
  diagnosticRequest?.abort()
  diagnosticRequest = undefined
  exportingDiagnostic.value = false
}
function listQuery(limit = visibleCount.value) {
  return {
    limit: String(limit), q: query.value.trim(), node_status: nodeStatus.value, severity: severity.value,
    owner: ownerFilter.value, progress: progressFilter.value, ...(pendingOnly.value ? { pending: '1' } : {}),
  }
}
watch([query, severity, nodeStatus, pendingOnly, ownerFilter, progressFilter], (next, previous) => {
  visibleCount.value = 50
  if (selectedProblems.value.length) {
    selectedProblems.value = []
    batchMessage.value = '筛选条件已改变，已清空原选择，请重新选择问题。'
  }
  overviewRequest?.abort()
  cancelExport()
  exportError.value = ''
  loading.value = true
  clearTimeout(searchTimer)
  const textOnly = next[0] !== previous[0] && next.slice(1).every((value, index) => value === previous[index + 1])
  if (textOnly) searchTimer = window.setTimeout(() => { void refresh() }, 250)
  else void refresh()
})
let disposed = false
onBeforeUnmount(() => {
  disposed = true
  clearTimeout(searchTimer)
  overviewRequest?.abort()
  cancelExport()
})
const canHandle = computed(() => ['admin', 'troubleshooter'].includes(props.role))
const canSelfAssign = computed(() => snapshot.value?.assignees.some(u => u.name === snapshot.value?.current_user.name))
const mineCount = computed(() => snapshot.value?.summary.mine || 0)
const pollRefresh = usePolling(async signal => {
  if (searchTimer !== undefined) return
  overviewRequest?.abort()
  const request = overviewRequest = new AbortController()
  const cancel = () => request.abort()
  signal.addEventListener('abort', cancel, { once: true })
  if (signal.aborted) request.abort()
  loading.value = true
  try {
    const next = await api.operations(request.signal, listQuery())
    if (request.signal.aborted || disposed) return
    snapshot.value = next
    error.value = ''
  } catch (e) {
    if (request.signal.aborted || disposed) return
    if (e instanceof ApiError && [401, 403].includes(e.status)) {
      snapshot.value = null; selectedProblems.value = []; cancelExport()
    }
    error.value = e instanceof ApiError && e.status === 401 ? '登录已失效，请重新登录。' : '刷新失败，正在展示上次成功的数据；请检查连接并重试。'
  } finally {
    signal.removeEventListener('abort', cancel)
    if (overviewRequest === request) overviewRequest = undefined
    if (!request.signal.aborted && !disposed) loading.value = false
  }
}, 15000)
function refresh() {
  clearTimeout(searchTimer)
  searchTimer = undefined
  return pollRefresh()
}

const filteredNodes = computed(() => {
  const q = query.value.trim().toLowerCase()
  return (snapshot.value?.nodes || []).filter(n => (nodeStatus.value === 'all' || n.status === nodeStatus.value)
    && (!q || `${n.hostname} ${n.id} ${n.os} ${JSON.stringify(n.labels)}`.toLowerCase().includes(q)))
    .sort((a,b) => (b.alarms?.critical || 0) - (a.alarms?.critical || 0) || a.hostname.localeCompare(b.hostname))
})
const problems = computed(() => {
  const q = query.value.trim().toLowerCase()
  return (snapshot.value?.problems || []).filter(p => (severity.value === 'all' || p.severity === severity.value)
    && (nodeStatus.value === 'all' || p.node_status === nodeStatus.value)
    && (!pendingOnly.value || !p.handling.acknowledged)
    && (ownerFilter.value === 'all' || (ownerFilter.value === 'mine' && !!p.handling.assignee && p.handling.assignee === snapshot.value?.current_user.name)
      || (ownerFilter.value === 'unassigned' && !p.handling.assignee) || (ownerFilter.value === 'assigned' && !!p.handling.assignee))
    && (progressFilter.value === 'all' || p.handling.status === progressFilter.value)
    && (!q || `${p.hostname} ${p.node} ${p.name} ${p.chart} ${p.family} ${p.info} ${p.handling.assignee}`.toLowerCase().includes(q)))
})
const families = computed(() => {
  if (snapshot.value?.page) return snapshot.value.page.families.map(f => [f.name, f.count] as [string, number])
  const counts = new Map<string, number>()
  for (const p of problems.value) counts.set(p.family || '其他', (counts.get(p.family || '其他') || 0) + 1)
  return [...counts].sort((a,b) => b[1] - a[1]).slice(0, 8)
})
const nodesVisible = computed(() => snapshot.value?.page ? filteredNodes.value : filteredNodes.value.slice(0, visibleCount.value))
const problemsVisible = computed(() => snapshot.value?.page ? problems.value : problems.value.slice(0, visibleCount.value))
const nodeTotal = computed(() => snapshot.value?.page?.nodes_matched ?? filteredNodes.value.length)
const problemTotal = computed(() => snapshot.value?.page?.problems_matched ?? problems.value.length)
const activeIDs = computed(() => new Set(snapshot.value?.problems.map(p => p.id) || []))
function matchingActivity(records: OperationsSnapshot['activity'], search: string) {
  const q = search.trim().toLowerCase()
  return records.filter(r => !q || `${r.problem.hostname} ${r.problem.name} ${r.problem.chart} ${r.assignee} ${r.history.map(h => `${h.actor} ${h.assignee || ''} ${h.previous_assignee || ''} ${h.note}`).join(' ')}`.toLowerCase().includes(q))
}
const actionName: Record<string, string> = { acknowledge: '确认问题', unacknowledge: '撤销确认', comment: '补充备注', assign: '指派责任人', unassign: '取消指派', progress: '更新进度', auto_command: '自动动作', inhibited: '动作已抑制' }
const progressName: Record<HandlingStatus, string> = { open: '待处理', investigating: '排查中', watching: '观察中' }
function describeAction(h: HandlingAction) {
  const label = actionName[h.action] || h.action
  if (h.action === 'assign' || h.action === 'unassign') return `${label}：${h.previous_assignee || '未分配'} → ${h.assignee || '未分配'}`
  if (h.action === 'progress') return `${label}：${progressName[h.previous_status || 'open']} → ${progressName[h.status || 'open']}`
  return label
}
const formatTime = (t: number) => t > 0 ? new Date(t * 1000).toLocaleString() : '暂无样本'
const deliveryText = (d: NonNullable<Problem['delivery']>) => {
  const outcome = d.outcome === 'accepted' ? '通道已接受' : d.outcome === 'failed' ? '通道失败' : d.outcome === 'suppressed' ? '已抑制' : d.outcome
  return outcome + (d.reason ? `（${d.reason}）` : '')
}
const age = (t: number) => {
  if (!t) return '时间未知'
  const s = Math.max(0, (snapshot.value?.now || Date.now() / 1000) - t)
  return s < 60 ? `${Math.floor(s)} 秒` : s < 3600 ? `${Math.floor(s / 60)} 分钟` : s < 86400 ? `${Math.floor(s / 3600)} 小时` : `${Math.floor(s / 86400)} 天`
}
function edit(p: Problem) {
  draftRevisions.value[p.id] ??= p.handling.revision
}
async function update(p: Problem, change: HandlingChange) {
  if (busy.value) return
  busy.value = p.id
  actionError.value = ''
  try {
    await api.handleProblem({ id: p.id, ...change, note: notes.value[p.id] || '', revision: draftRevisions.value[p.id] ?? p.handling.revision })
    if (disposed) return
    historyRefreshKey.value++
    delete notes.value[p.id]
    delete draftRevisions.value[p.id]
    if (change.action === 'assign' || change.action === 'unassign') delete owners.value[p.id]
    if (change.action === 'progress') delete progress.value[p.id]
    await refresh()
  } catch (e) {
    if (disposed) return
    if (e instanceof ApiError && e.status === 409) {
      await refresh()
      if (disposed) return
      actionError.value = error.value
        ? '问题或处理记录已变化，但最新记录读取失败。草稿已保留，请恢复连接并刷新后重试。'
        : '问题或处理记录已变化，已重新读取。草稿已保留，请核对责任人和进度后重试。'
      if (!error.value) delete draftRevisions.value[p.id]
      if (snapshot.value && !snapshot.value.problems.some(x => x.id === p.id) && notes.value[p.id]) {
        actionError.value = `原问题已恢复或进入新阶段，未提交备注：${notes.value[p.id]}`
      }
    } else actionError.value = e instanceof ApiError && e.status === 403 ? '当前账号没有问题处理权限。' : `保存失败：${String(e)}`
  } finally { if (!disposed) busy.value = '' }
}
function toggleProblem(p: Problem, checked: boolean) {
  if (busy.value) return
  if (!checked) selectedProblems.value = selectedProblems.value.filter(x => x.id !== p.id)
  else if (selectedProblems.value.length < 50 && !selectedProblems.value.some(x => x.id === p.id)) selectedProblems.value.push(p)
  batchError.value = ''; batchMessage.value = ''
}
async function updateBatch(change: HandlingChange & { note: string }) {
  if (busy.value || !canHandle.value || !selectedProblems.value.length || error.value) return
  const items = selectedProblems.value.map(p => ({ id: p.id, revision: p.handling.revision }))
  busy.value = 'batch'; batchError.value = ''; batchMessage.value = ''
  try {
    const result = await api.handleProblems({ ...change, items })
    if (disposed) return
    selectedProblems.value = []
    batchCompleted.value++
    batchNote.value = ''
    historyRefreshKey.value++
    batchMessage.value = `已对 ${result.count} 个问题完成${actionName[change.action] || change.action}。`
    await refresh()
  } catch (e) {
    if (disposed) return
    if (e instanceof ApiError && e.status === 409) {
      await refresh()
      if (disposed) return
      batchError.value = error.value
        ? '所选问题或处理记录已变化，整批未提交；刷新也未成功。请恢复连接、刷新总览并重新选择，统一备注已保留。'
        : '所选问题或处理记录已变化，整批未提交。已读取最新状态，请重新选择后再执行；统一备注已保留。'
    } else if (e instanceof ApiError && e.status === 403) batchError.value = '当前账号没有批量处置权限。'
    else if (e instanceof ApiError && e.status === 401) { selectedProblems.value = []; batchError.value = '登录已失效，请重新登录。' }
    else if (e instanceof ApiError && e.status === 503) batchError.value = '服务端保存失败，整批未提交，选择与统一备注已保留。'
    else if (e instanceof ApiError && e.status === 400) batchError.value = '批量请求无效，请检查责任人、备注或选择数量。'
    else {
      await refresh()
      if (disposed) return
      batchError.value = '请求中断，结果暂不确定。请核对最新处理记录后重新选择；统一备注已保留。'
    }
  } finally { if (!disposed) busy.value = '' }
}
function assign(p: Problem) {
  const assignee = owners.value[p.id] ?? p.handling.assignee
  void update(p, assignee ? { action: 'assign', assignee } : { action: 'unassign' })
}
function applyView(v: OperationsView) {
  query.value = v.query; severity.value = v.severity; nodeStatus.value = v.nodeStatus; pendingOnly.value = v.pendingOnly; visibleCount.value = 50
  ownerFilter.value = v.ownerFilter; progressFilter.value = v.progressFilter
}
async function exportSnapshot() {
  if (!snapshot.value || exporting.value || exportingDiagnostic.value || loading.value || disposed) return
  const request = exportRequest = new AbortController()
  const filters = { query: query.value, severity: severity.value, node_status: nodeStatus.value,
    pending_only: pendingOnly.value, owner: ownerFilter.value, progress: progressFilter.value }
  const params = listQuery(200)
  exporting.value = true
  exportError.value = ''
  try {
    const full = await api.operations(request.signal, params)
    if (disposed || request.signal.aborted || exportRequest !== request) return
    const data = { captured_at: full.now, stale: false, scope: 'filtered', filters,
      nodes: full.nodes, problems: full.problems, activity: matchingActivity(full.activity, filters.query), matched: full.page }
    const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' }))
    const a = document.createElement('a'); a.href = url; a.download = `monitor-operations-${full.now}.json`; a.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  } catch (e) {
    if (disposed || request.signal.aborted || exportRequest !== request) return
    if (e instanceof ApiError && [401, 403].includes(e.status)) {
      overviewRequest?.abort()
      snapshot.value = null; selectedProblems.value = []; loading.value = false
      error.value = '登录已失效或没有读取权限，请重新登录。'
      exportError.value = '当前登录无法导出快照，本次未生成文件。'
    } else exportError.value = '快照导出失败，本次未生成文件。请检查连接后重试。'
  } finally {
    if (exportRequest === request) {
      exportRequest = undefined
      exporting.value = false
    }
  }
}
async function exportDiagnostic() {
  if (!canExportDiagnostic.value || exporting.value || exportingDiagnostic.value || loading.value || disposed) return
  const request = diagnosticRequest = new AbortController()
  exportingDiagnostic.value = true
  exportError.value = ''
  try {
    const { blob, filename } = await api.exportDiagnostic({ include: 'ops,alarms,notify,history,ai' }, request.signal)
    if (disposed || request.signal.aborted || diagnosticRequest !== request) return
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a'); a.href = url; a.download = filename; a.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  } catch (e) {
    if (disposed || request.signal.aborted || diagnosticRequest !== request) return
    if (e instanceof ApiError && [401, 403].includes(e.status)) {
      exportError.value = '当前账号不能导出诊断包；需要管理员或排障人员登录。'
    } else exportError.value = '诊断包导出失败，本次未生成文件。请检查连接后重试。'
  } finally {
    if (diagnosticRequest === request) {
      diagnosticRequest = undefined
      exportingDiagnostic.value = false
    }
  }
}
</script>

<template>
  <div class="operations" aria-label="运维总览">
    <div class="hero">
      <div><h1>运维总览</h1><p class="muted hero-description">聚焦待处理问题，按需进入主机、历史、维护或通知诊断。</p></div>
      <div class="hero-actions">
        <button @click="refresh()"><AppIcon name="refresh" :size="16" />刷新总览</button>
        <details class="export-menu" aria-label="运维导出">
          <summary><AppIcon name="download" :size="16" />导出</summary>
          <div class="export-options">
            <button :disabled="!snapshot || loading || exporting || exportingDiagnostic" @click="exportSnapshot">{{ exporting ? '正在导出…' : '导出当前快照' }}</button>
            <button v-if="canExportDiagnostic" :disabled="loading || exporting || exportingDiagnostic" @click="exportDiagnostic" title="打包当前问题、告警转移、通知诊断、处理历史与个人 AI 方案，便于分享给他人协助">{{ exportingDiagnostic ? '正在打包…' : '导出诊断包' }}</button>
          </div>
        </details>
      </div>
    </div>
    <p v-if="loading" role="status">正在读取节点和问题…</p>
    <p v-if="error" class="notice error" role="alert">{{ error }}</p>
    <p v-if="exportError" class="notice error" role="alert">{{ exportError }}</p>
    <template v-if="snapshot">
      <div class="summary">
        <div><span class="summary-label"><AppIcon name="server" />受监控节点</span><strong>{{ snapshot.summary.nodes }}</strong><small>{{ snapshot.summary.live }} 在线</small></div>
        <div :class="{ attention: snapshot.summary.offline || snapshot.summary.stale }"><span class="summary-label"><AppIcon name="network" />需要检查的连接</span><strong>{{ (snapshot.summary.offline || 0) + (snapshot.summary.stale || 0) }}</strong><small>{{ snapshot.summary.offline }} 离线 · {{ snapshot.summary.stale }} 数据过期</small></div>
        <div :class="{ critical: snapshot.summary.critical }"><span class="summary-label"><AppIcon name="warning" />严重问题</span><strong>{{ snapshot.summary.critical }}</strong><small>{{ snapshot.summary.warning }} 条警告</small></div>
        <div :class="{ attention: snapshot.summary.unacknowledged }"><span class="summary-label"><AppIcon name="clock" />待确认问题</span><strong>{{ snapshot.summary.unacknowledged }}</strong><small>确认不影响告警求值与通知</small></div>
      </div>
      <p class="updated">快照时间 {{ formatTime(snapshot.now) }} · 前台每 15 秒刷新 · {{ snapshot.persistent ? '处理记录保存到服务端' : '处理记录仅在内存中，重启会丢失' }}</p>
      <OperationsNavigation :selected="section" @select="changeSection" />
      <div id="operations-content" role="tabpanel" :aria-labelledby="`operations-tab-${section}`">
      <p v-if="snapshot.summary.coverage_unknown" class="notice">{{ snapshot.summary.coverage_unknown }} 个节点尚无告警规则、告警未启用或不在此 Hub 的可见范围内，零问题不代表已完成健康检查。</p>
      <div v-if="section === 'problems' || section === 'nodes'" class="filters">
        <input v-model="query" placeholder="搜索主机、问题、图表或标签…" aria-label="搜索主机或问题" @input="visibleCount = 50" />
        <select v-if="section === 'problems'" v-model="severity" aria-label="问题级别"><option value="all">全部问题级别</option><option value="CRITICAL">严重</option><option value="WARNING">警告</option></select>
        <button :aria-expanded="moreFilters" aria-controls="operations-more-filters" @click="moreFilters = !moreFilters">更多筛选<span v-if="advancedFilterCount">（{{ advancedFilterCount }}）</span></button>
        <span v-if="activeFilterCount" class="filter-count" role="status">已启用 {{ activeFilterCount }} 项筛选</span>
        <button v-if="activeFilterCount" @click="clearFilters">清除筛选</button>
        <div v-show="moreFilters" id="operations-more-filters" class="advanced-filters">
          <select v-model="nodeStatus" aria-label="节点状态"><option value="all">全部连接状态</option><option value="live">在线</option><option value="stale">数据过期</option><option value="offline">离线</option></select>
          <select v-if="section === 'problems'" v-model="ownerFilter" aria-label="责任人筛选" @change="visibleCount = 50"><option value="all">全部责任人</option><option value="mine">分配给我</option><option value="unassigned">未分配</option><option value="assigned">已分配</option></select>
          <select v-if="section === 'problems'" v-model="progressFilter" aria-label="处理进度筛选" @change="visibleCount = 50"><option value="all">全部处理进度</option><option value="open">待处理</option><option value="investigating">排查中</option><option value="watching">观察中</option></select>
          <label v-if="section === 'problems'"><input type="checkbox" v-model="pendingOnly" />只看待确认</label>
          <div v-if="section === 'problems'" class="workflow-overview" aria-label="处理进度概况">
            <span>未分配 <b>{{ snapshot.summary.unassigned }}</b></span>
            <span>排查中 <b>{{ snapshot.summary.investigating }}</b></span>
            <span>观察中 <b>{{ snapshot.summary.watching }}</b></span>
            <button @click="ownerFilter = 'mine'; visibleCount = 50">分配给我 {{ mineCount }}</button>
          </div>
        </div>
      </div>
      <OperationsViews v-if="section === 'problems'" ref="viewsPanel" v-model:draft-name="viewDraftName" :filters="{ query, severity, nodeStatus, pendingOnly, ownerFilter, progressFilter }" @apply="applyView" />

      <OperationsNodes v-if="section === 'nodes'" :nodes="nodesVisible" :total="nodeTotal" @drill="(node, chart) => emit('drill', node, chart)" />
      <template v-if="section === 'problems'">
      <div class="row section-title"><h2><AppIcon name="alarms" />问题中心 <small>{{ problemTotal }} 条</small></h2><div class="families"><span v-for="[family, count] in families" :key="family">{{ family }} <b>{{ count }}</b></span></div><span class="muted sort-hint">严重优先，待确认优先</span></div>
      <p v-if="actionError" class="notice error" role="alert">{{ actionError }}</p>
      <p v-if="!canHandle" class="muted">当前账号只读；管理员和排障人员可以确认问题、指派责任人、更新进度及添加备注。</p>
      <OperationsBatch v-if="canHandle" v-model:selected="selectedProblems" v-model:action="batchAction" v-model:assignee="batchAssignee" v-model:status="batchStatus" v-model:note="batchNote" :current="snapshot.problems" :visible="problemsVisible" :assignees="snapshot.assignees"
        :busy="!!busy" :stale="!!error" :error="batchError" :message="batchMessage" :completed="batchCompleted" @submit="updateBatch" />
      <p v-if="!problems.length" class="empty">当前筛选范围内没有待展示的问题。</p>
      <article v-for="p in problemsVisible" :key="p.id" class="problem" :class="p.severity.toLowerCase()" :data-problem-id="p.id">
        <label v-if="canHandle" class="problem-select"><input type="checkbox" :checked="selectedProblems.some(x => x.id === p.id)"
          :disabled="!!busy || !!error || (selectedProblems.length >= 50 && !selectedProblems.some(x => x.id === p.id))"
          :aria-label="'选择问题 ' + p.hostname + ' · ' + p.name" @change="toggleProblem(p, ($event.target as HTMLInputElement).checked)" />选择此问题</label>
        <div class="row"><div class="problem-title"><span class="badge" :class="p.severity.toLowerCase()">{{ p.severity === 'CRITICAL' ? '严重' : '警告' }}</span><h3>{{ p.name }}</h3><span class="ack" v-if="p.handling.acknowledged">已确认</span><span class="muted" v-else>待确认</span></div><span class="muted">{{ age(p.since) }}</span></div>
        <p>{{ p.info || '暂无规则说明' }}</p>
        <p class="muted">{{ p.hostname }} · {{ p.chart }} · {{ p.value === null ? '暂无数值' : p.value.toFixed(2) + ' ' + p.units }}</p>
        <p v-if="p.recovery_hold" class="muted">等待恢复：警告和严重条件已不成立，恢复表达式尚未成立。</p>
        <p v-else-if="p.hold_until && snapshot.now < p.hold_until" class="muted">恢复保持至 {{ formatTime(p.hold_until) }}</p>
        <p v-if="p.delivery" class="muted">通知 {{ p.delivery.channel }} {{ deliveryText(p.delivery) }}。通道结果不代表用户已收件。</p>
        <div class="workflow-state"><span class="owner">责任人：{{ p.handling.assignee || '未分配' }}</span><span class="badge progress-state" :class="p.handling.status">{{ progressName[p.handling.status] }}</span></div>
        <p v-if="p.stale" class="stale-text">历史告警状态，最近观测 {{ formatTime(p.updated) }}；节点恢复更新前不视为当前健康结论。</p>
        <div v-if="canHandle" class="workflow-actions">
          <div>
            <label>责任人 <select :value="owners[p.id] ?? p.handling.assignee" :disabled="busy === p.id" :aria-label="p.name + ' 责任人'" @change="edit(p); owners[p.id] = ($event.target as HTMLSelectElement).value">
              <option value="">未分配</option>
              <option v-if="p.handling.assignee && !snapshot.assignees.some(u => u.name === p.handling.assignee)" :value="p.handling.assignee" disabled>{{ p.handling.assignee }}（不在可指派列表）</option>
              <option v-for="u in snapshot.assignees" :key="u.name" :value="u.name">{{ u.name }}{{ u.name === snapshot.current_user.name ? '（我）' : '' }}</option>
            </select></label>
            <button :disabled="!!busy || (owners[p.id] ?? p.handling.assignee) === p.handling.assignee" @click="assign(p)">保存责任人</button>
            <button v-if="canSelfAssign && p.handling.assignee !== snapshot.current_user.name" :disabled="!!busy" @click="update(p, { action: 'assign', assignee: snapshot.current_user.name })">分配给我</button>
          </div>
          <div>
            <label>处理进度 <select :value="progress[p.id] ?? p.handling.status" :disabled="busy === p.id" :aria-label="p.name + ' 处理进度'" @change="edit(p); progress[p.id] = ($event.target as HTMLSelectElement).value as HandlingStatus">
              <option value="open">待处理</option><option value="investigating">排查中</option><option value="watching">观察中</option>
            </select></label>
            <button :disabled="!!busy || (progress[p.id] ?? p.handling.status) === p.handling.status" @click="update(p, { action: 'progress', status: progress[p.id] ?? p.handling.status })">保存进度</button>
          </div>
        </div>
        <div class="problem-actions"><button @click="emit('drill', p.node, p.chart)">定位图表</button><button @click="emit('solve', p)"><AppIcon name="ai" :size="16" />AI 解决问题</button><template v-if="canHandle"><input v-model="notes[p.id]" @input="edit(p)" :disabled="busy === p.id" :aria-label="p.name + ' 处理备注'" placeholder="处理备注（可选）" maxlength="512" /><button :disabled="!!busy" @click="update(p, { action: p.handling.acknowledged ? 'unacknowledge' : 'acknowledge' })">{{ busy === p.id ? '正在保存…' : p.handling.acknowledged ? '撤销确认' : '确认问题' }}</button><button :disabled="!!busy || !notes[p.id]?.trim()" @click="update(p, { action: 'comment' })">添加备注</button></template></div>
        <details v-if="p.handling.history.length"><summary>处理记录 · 最近 {{ p.handling.history.length }} 条</summary><ol><li v-for="(h,i) in [...p.handling.history].reverse()" :key="i"><span>{{ formatTime(h.at) }} · {{ h.actor }} · {{ describeAction(h) }}</span><p v-if="h.note">{{ h.note }}</p></li></ol></details>
      </article>
      </template>
      <button v-if="(section === 'nodes' && nodeTotal > nodesVisible.length) || (section === 'problems' && problemTotal > problemsVisible.length)" @click="visibleCount = Math.min(visibleCount + 50, 200); refresh()">再显示 50 条</button>
      <MaintenancePlans v-if="section === 'maintenance'" ref="maintenancePanel" />
      <NotificationDiagnostics v-if="section === 'notifications'" />
      <OperationsHistory v-if="section === 'history'" :active-ids="activeIDs" :refresh-key="historyRefreshKey" />
      <p v-if="section === 'problems'" class="footnote">确认、指派和处理进度不改变告警求值或通知；「观察中」仍可能是严重告警。严重级别变化或恢复后再次触发，需要重新确认和分配。责任人列表包括本机配置的管理员、排障人员及当前有权限的登录者；不向外部值班系统发送通知。</p>
      </div>
    </template>
  </div>
</template>

<style scoped>
.problem-select { display:flex; align-items:center; gap:6px; color:#94a3b8; font-size:12px; margin-bottom:10px; }
.operations { max-width: 1600px; margin: 0 auto; color: #e2e8f0; }
.workflow-overview,.workflow-state,.workflow-actions,.workflow-actions>div { display:flex; align-items:center; flex-wrap:wrap; gap:10px; }
.workflow-overview { padding:10px 0; font-size:12px; color:#94a3b8; }.workflow-overview b { color:#e2e8f0; margin-left:4px; }
.workflow-state { font-size:12px; margin:12px 0; }.owner { overflow-wrap:anywhere; }.badge.investigating { background:#1e3a5f; color:#93c5fd; }.badge.watching { background:#3b2c5b; color:#d8b4fe; }
.workflow-actions { border-top:1px solid #27364c; padding-top:12px; margin-top:12px; gap:12px 22px; }.workflow-actions>div { min-width:0; max-width:100%; }.workflow-actions label { display:flex; align-items:center; gap:8px; font-size:12px; min-width:0; max-width:100%; }.workflow-actions select { min-width:0; max-width:220px; flex-shrink:1; }
.hero { display:flex; align-items:center; justify-content:space-between; gap:12px; padding:8px 0 14px; flex-wrap:wrap; }
h1 { font-size:26px; margin:0; letter-spacing:-.03em; }
h2 { font-size:17px; margin:14px 0 8px; } h2 small { font-size:12px; color:#94a3b8; font-weight:400; margin-left:8px; } h3 { font-size:14px; margin:0; overflow-wrap:anywhere; }
p { font-size:13px; line-height:1.65; margin:8px 0; } .muted,.updated,.footnote { color:#94a3b8; } .updated { font-size:11px; margin:6px 0; line-height:1.45; }
.hero-actions,.filters,.saved-views,.problem-actions,.families,.problem-title { display:flex; gap:8px; align-items:center; flex-wrap:wrap; }
.hero-actions button { display:inline-flex; align-items:center; justify-content:center; gap:6px; }
h2 > .app-icon { margin-right:7px; color:#5eead4; }
.summary-label { display:flex; align-items:center; gap:6px; }
.summary-label .app-icon { color:#5eead4; }.summary .attention .summary-label .app-icon { color:#fcd34d; }.summary .critical .summary-label .app-icon { color:#fda4af; }
button,input,select { font:inherit; font-size:12px; border:1px solid #334155; border-radius:7px; background:#111d31; color:#e2e8f0; padding:8px 10px; max-width:100%; } button { cursor:pointer; } button:hover:not(:disabled) { border-color:#5eead4; color:#5eead4; } button:disabled { opacity:.5; cursor:default; } input:focus,select:focus,button:focus-visible { outline:2px solid #5eead4; outline-offset:2px; }
.summary { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:12px; } .summary>div { background:linear-gradient(120deg,#122238,#0f172a); border:1px solid #23334b; border-radius:10px; padding:10px 12px; display:grid; grid-template-columns:1fr auto; align-items:center; gap:4px 8px; } .summary span { font-size:12px; color:#cbd5e1; } .summary strong { font-size:30px; font-weight:600; grid-column:2; grid-row:1 / 3; } .summary small { font-size:11px; color:#94a3b8; } .summary .critical strong { color:#fda4af; } .summary .attention strong { color:#fcd34d; }
.notice { background:#332b18; border:1px solid #625025; color:#fde68a; padding:10px 14px; border-radius:8px; } .notice.error { color:#fecaca; background:#3a1e2a; border-color:#7f1d1d; }
.filters { padding:10px; background:#0c1526; border:1px solid #23334b; border-radius:10px; margin:10px 0 8px; } .filters>input:first-child { flex:1; min-width:min(250px,100%); } .filters label { font-size:12px; white-space:nowrap; } .view-name { width:120px; } .saved-views { font-size:12px; }
.row { display:flex; gap:12px; justify-content:space-between; align-items:center; flex-wrap:wrap; }.badge { font-size:11px; padding:3px 7px; border-radius:5px; background:#26334a; color:#cbd5e1; white-space:nowrap; } .badge.live,.ack { color:#6ee7b7; }.badge.offline,.badge.critical { color:#fda4af; background:#4c1d2a; } .badge.stale,.badge.warning { color:#fde68a; background:#46361c; }
.section-title { gap:8px; margin:10px 0 6px; }.section-title h2 { margin:0; }.sort-hint { margin-left:auto; } .section-title>span { font-size:12px; } .families { margin:0; font-size:12px; }.families>span { padding:5px 9px; background:#1e293b; border-radius:5px; } .families b { margin-left:6px; color:#5eead4; }
.problem { background:#0f1a2d; border:1px solid #27364c; border-left:3px solid #fbbf24; border-radius:8px; padding:16px; margin:12px 0; overflow-wrap:anywhere; } .problem.critical { border-left-color:#fb7185; } .problem-title { flex:1; } .ack { font-size:11px; }.problem-actions { margin-top:14px; }.problem-actions input { flex:1; min-width:min(200px,100%); }.stale-text { color:#fcd34d; }.problem details { margin-top:12px; border-top:1px solid #27364c; padding-top:12px; font-size:12px; } summary { cursor:pointer; color:#94a3b8; } ol { padding-left:20px; color:#94a3b8; } li { margin:10px 0; } li p { color:#e2e8f0; white-space:pre-wrap; } .empty { padding:20px; border:1px dashed #334155; border-radius:8px; color:#94a3b8; } .footnote { margin:24px 0; font-size:12px; }
.activity { border:1px solid #27364c; border-radius:7px; padding:12px; margin:8px 0; font-size:12px; overflow-wrap:anywhere; }
.advanced-filters { display:flex; align-items:center; flex-wrap:wrap; gap:8px; flex-basis:100%; border-top:1px solid #27364c; padding-top:10px; }.advanced-filters .workflow-overview { flex-basis:100%; padding:0; }.filter-count { font-size:12px; color:#99f6e4; }
.export-menu { position:relative; }.export-menu summary { list-style:none; display:flex; gap:6px; align-items:center; padding:8px 10px; border:1px solid #334155; border-radius:7px; background:#111d31; color:#e2e8f0; font-size:12px; }.export-menu summary::-webkit-details-marker { display:none; }.export-menu summary:focus-visible { outline:2px solid #5eead4; outline-offset:2px; }.export-options { position:absolute; z-index:10; top:calc(100% + 5px); right:0; display:grid; gap:6px; padding:8px; min-width:150px; border:1px solid #334155; border-radius:8px; background:#0f1a2d; box-shadow:0 10px 24px #0006; }.export-options button { white-space:nowrap; }
@media(max-width:760px) {
  .hero { flex-wrap:nowrap; padding:2px 0 10px; gap:8px; }.hero-description { display:none; } h1 { font-size:22px; white-space:nowrap; }.hero-actions { flex-wrap:nowrap; gap:5px; }.hero-actions button,.export-menu summary { padding:8px; }
  .summary { grid-template-columns:repeat(2,minmax(0,1fr)); gap:6px; }.summary>div { padding:8px; gap:3px 5px; }.summary strong { font-size:25px; }.summary-label { gap:4px; }.summary span { font-size:11px; }.summary small { font-size:10px; line-height:1.35; }.summary-label .app-icon { width:13px; height:13px; flex-shrink:0; }
  .filters { padding:8px; gap:6px; }.filters>input:first-child { flex-basis:100%; min-width:0; }.advanced-filters select { flex:1; min-width:125px; }.updated { font-size:10px; }.section-title { gap:5px; margin-top:8px; }.sort-hint { display:none; }.problem-actions input { flex-basis:100%; }
}
</style>
