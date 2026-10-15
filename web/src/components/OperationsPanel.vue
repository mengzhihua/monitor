<script setup lang="ts">
import { defineAsyncComponent, ref, toRef } from 'vue'
import type { Problem } from '../api/operations/types'
import OperationsNodes from '../features/operations/OperationsNodes.vue'
import OperationsNavigation from '../features/operations/OperationsNavigation.vue'
import OperationsProblemCard from '../features/operations/OperationsProblemCard.vue'
import { useOperationsSection, type OperationsSection } from '../features/operations/useOperationsSection'
import { useOperationsFilters } from '../features/operations/useOperationsFilters'
import { useOperationsOverview } from '../features/operations/useOperationsOverview'
import { useProblemHandling } from '../features/operations/useProblemHandling'
import { useOperationsExports } from '../features/operations/useOperationsExports'
import { formatTime } from '../features/operations/problemPresentation'
import OperationsViews from './OperationsViews.vue'
import OperationsBatch from './OperationsBatch.vue'
import AppIcon from './AppIcon.vue'
const OperationsHistory = defineAsyncComponent(() => import('./OperationsHistory.vue'))
const NotificationDiagnostics = defineAsyncComponent(() => import('./NotificationDiagnostics.vue'))
const MaintenancePlans = defineAsyncComponent(() => import('./MaintenancePlans.vue'))

const props = defineProps<{ role: string }>()
const emit = defineEmits<{ drill: [node: string, chart: string]; solve: [problem: Problem] }>()
const { section, selectSection } = useOperationsSection()
const filters = useOperationsFilters(section)
const { query, severity, nodeStatus, pendingOnly, ownerFilter, progressFilter, moreFilters,
  visibleCount, advancedFilterCount, activeFilterCount, clearFilters, applyView } = filters
const { snapshot, error, loading, refresh, invalidateAccess, nodesVisible, problems, problemsVisible,
  nodeTotal, problemTotal, families, activeIDs } = useOperationsOverview(filters)
const role = toRef(props, 'role')
const { actionError, busy, historyRefreshKey, notes, owners, progress, selectedProblems,
  batchError, batchMessage, batchCompleted, batchAction, batchAssignee, batchStatus, batchNote,
  canHandle, canSelfAssign, mineCount, dirty, edit, update, toggleProblem, updateBatch, assign,
} = useProblemHandling({ role, snapshot, error, refresh, filters })
const { exporting, exportingDiagnostic, exportError, canExportDiagnostic, exportSnapshot, exportDiagnostic,
} = useOperationsExports({ role, snapshot, loading, filters, invalidateAccess })
const maintenancePanel = ref<InstanceType<typeof MaintenancePlans> | null>(null)
const viewsPanel = ref<InstanceType<typeof OperationsViews> | null>(null)
const viewDraftName = ref('')
function changeSection(value: OperationsSection) {
  if (section.value === value || viewsPanel.value?.isSaving()) return
  if (section.value === 'maintenance' && maintenancePanel.value?.canLeave() === false) return
  selectSection(value)
}
function canLeave() {
  if (busy.value || maintenancePanel.value?.isBusy() || viewsPanel.value?.isSaving()) return false
  const hasDraft = dirty.value || !!viewDraftName.value.trim() || maintenancePanel.value?.isDirty()
  return !hasDraft || confirm('仍有未提交的问题处理、个人视图或维护草稿，离开运维总览将丢弃这些内容，确定继续？')
}
defineExpose({ canLeave })
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
      <OperationsProblemCard v-for="p in problemsVisible" :key="p.id" :p="p" :now="snapshot.now"
        :assignees="snapshot.assignees" :current-user="snapshot.current_user.name" :can-handle="canHandle" :can-self-assign="!!canSelfAssign"
        :busy="busy" :stale="!!error" :selected="selectedProblems.some(x => x.id === p.id)" :selection-full="selectedProblems.length >= 50"
        :note="notes[p.id]" :owner="owners[p.id]" :status="progress[p.id]"
        @select="checked => toggleProblem(p, checked)" @owner="value => { edit(p); owners[p.id] = value }"
        @status="value => { edit(p); progress[p.id] = value }" @note="value => { edit(p); notes[p.id] = value }"
        @assign="assign(p)" @update="change => update(p, change)" @drill="(node, chart) => emit('drill', node, chart)" @solve="emit('solve', $event)" />
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
.operations { max-width: 1600px; margin: 0 auto; color: #e2e8f0; }
.workflow-overview { display:flex; align-items:center; flex-wrap:wrap; gap:10px; }
.workflow-overview { padding:10px 0; font-size:12px; color:#94a3b8; }.workflow-overview b { color:#e2e8f0; margin-left:4px; }
.hero { display:flex; align-items:center; justify-content:space-between; gap:12px; padding:8px 0 14px; flex-wrap:wrap; }
h1 { font-size:26px; margin:0; letter-spacing:-.03em; }
h2 { font-size:17px; margin:14px 0 8px; } h2 small { font-size:12px; color:#94a3b8; font-weight:400; margin-left:8px; }
p { font-size:13px; line-height:1.65; margin:8px 0; } .muted,.updated,.footnote { color:#94a3b8; } .updated { font-size:11px; margin:6px 0; line-height:1.45; }
.hero-actions,.filters,.families { display:flex; gap:8px; align-items:center; flex-wrap:wrap; }
.hero-actions button { display:inline-flex; align-items:center; justify-content:center; gap:6px; }
h2 > .app-icon { margin-right:7px; color:#5eead4; }
.summary-label { display:flex; align-items:center; gap:6px; }
.summary-label .app-icon { color:#5eead4; }.summary .attention .summary-label .app-icon { color:#fcd34d; }.summary .critical .summary-label .app-icon { color:#fda4af; }
button,input,select { font:inherit; font-size:12px; border:1px solid #334155; border-radius:7px; background:#111d31; color:#e2e8f0; padding:8px 10px; max-width:100%; } button { cursor:pointer; } button:hover:not(:disabled) { border-color:#5eead4; color:#5eead4; } button:disabled { opacity:.5; cursor:default; } input:focus,select:focus,button:focus-visible { outline:2px solid #5eead4; outline-offset:2px; }
.summary { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:12px; } .summary>div { background:linear-gradient(120deg,#122238,#0f172a); border:1px solid #23334b; border-radius:10px; padding:10px 12px; display:grid; grid-template-columns:1fr auto; align-items:center; gap:4px 8px; } .summary span { font-size:12px; color:#cbd5e1; } .summary strong { font-size:30px; font-weight:600; grid-column:2; grid-row:1 / 3; } .summary small { font-size:11px; color:#94a3b8; } .summary .critical strong { color:#fda4af; } .summary .attention strong { color:#fcd34d; }
.notice { background:#332b18; border:1px solid #625025; color:#fde68a; padding:10px 14px; border-radius:8px; } .notice.error { color:#fecaca; background:#3a1e2a; border-color:#7f1d1d; }
.filters { padding:10px; background:#0c1526; border:1px solid #23334b; border-radius:10px; margin:10px 0 8px; } .filters>input:first-child { flex:1; min-width:min(250px,100%); } .filters label { font-size:12px; white-space:nowrap; }
.row { display:flex; gap:12px; justify-content:space-between; align-items:center; flex-wrap:wrap; }
.section-title { gap:8px; margin:10px 0 6px; }.section-title h2 { margin:0; }.sort-hint { margin-left:auto; } .section-title>span { font-size:12px; } .families { margin:0; font-size:12px; }.families>span { padding:5px 9px; background:#1e293b; border-radius:5px; } .families b { margin-left:6px; color:#5eead4; }
.empty { padding:20px; border:1px dashed #334155; border-radius:8px; color:#94a3b8; } .footnote { margin:24px 0; font-size:12px; }
.advanced-filters { display:flex; align-items:center; flex-wrap:wrap; gap:8px; flex-basis:100%; border-top:1px solid #27364c; padding-top:10px; }.advanced-filters .workflow-overview { flex-basis:100%; padding:0; }.filter-count { font-size:12px; color:#99f6e4; }
.export-menu { position:relative; }.export-menu summary { list-style:none; display:flex; gap:6px; align-items:center; padding:8px 10px; border:1px solid #334155; border-radius:7px; background:#111d31; color:#e2e8f0; font-size:12px; }.export-menu summary::-webkit-details-marker { display:none; }.export-menu summary:focus-visible { outline:2px solid #5eead4; outline-offset:2px; }.export-options { position:absolute; z-index:10; top:calc(100% + 5px); right:0; display:grid; gap:6px; padding:8px; min-width:150px; border:1px solid #334155; border-radius:8px; background:#0f1a2d; box-shadow:0 10px 24px #0006; }.export-options button { white-space:nowrap; }
@media(max-width:760px) {
  .hero { flex-wrap:nowrap; padding:2px 0 10px; gap:8px; }.hero-description { display:none; } h1 { font-size:22px; white-space:nowrap; }.hero-actions { flex-wrap:nowrap; gap:5px; }.hero-actions button,.export-menu summary { padding:8px; }
  .summary { grid-template-columns:repeat(2,minmax(0,1fr)); gap:6px; }.summary>div { padding:8px; gap:3px 5px; }.summary strong { font-size:25px; }.summary-label { gap:4px; }.summary span { font-size:11px; }.summary small { font-size:10px; line-height:1.35; }.summary-label .app-icon { width:13px; height:13px; flex-shrink:0; }
  .filters { padding:8px; gap:6px; }.filters>input:first-child { flex-basis:100%; min-width:0; }.advanced-filters select { flex:1; min-width:125px; }.updated { font-size:10px; }.section-title { gap:5px; margin-top:8px; }.sort-hint { display:none; }
}
</style>
