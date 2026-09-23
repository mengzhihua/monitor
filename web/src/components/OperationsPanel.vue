<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { api, ApiError } from '../api'
import type { OperationsSnapshot, Problem, ResourceMetric } from '../api'
import { usePolling } from '../polling'

const props = defineProps<{ role: string }>()
const emit = defineEmits<{ drill: [node: string, chart: string] }>()
const snapshot = ref<OperationsSnapshot | null>(null)
const error = ref('')
const actionError = ref('')
const busy = ref('')
const loading = ref(true)
const query = ref('')
const severity = ref('all')
const nodeStatus = ref('all')
const pendingOnly = ref(false)
const notes = ref<Record<string, string>>({})
const visibleCount = ref(50)
const viewName = ref('')
const viewMessage = ref('')
const viewsKey = 'monitor.operations.views.v1'
type SavedView = { name: string; query: string; severity: string; nodeStatus: string; pendingOnly: boolean }
const savedViews = ref<SavedView[]>([])
try {
  const v = JSON.parse(localStorage.getItem(viewsKey) || '[]')
  if (Array.isArray(v)) savedViews.value = v.filter(v => typeof v.name === 'string' && typeof v.query === 'string'
    && ['all', 'CRITICAL', 'WARNING'].includes(v.severity) && ['all', 'live', 'stale', 'offline'].includes(v.nodeStatus)
    && typeof v.pendingOnly === 'boolean').slice(0, 10)
} catch { /* storage may be unavailable; monitoring still works */ }
let disposed = false
onBeforeUnmount(() => { disposed = true })
const canHandle = computed(() => ['admin', 'troubleshooter'].includes(props.role))
const refresh = usePolling(async signal => {
  try {
    const next = await api.operations(signal)
    if (signal.aborted) return
    snapshot.value = next
    error.value = ''
  } catch (e) {
    if (signal.aborted) return
    if (e instanceof ApiError && [401, 403].includes(e.status)) snapshot.value = null
    error.value = e instanceof ApiError && e.status === 401 ? '登录已失效，请重新登录。' : '刷新失败，正在展示上次成功的数据；请检查连接并重试。'
  } finally { if (!signal.aborted) loading.value = false }
}, 15000)

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
    && (!q || `${p.hostname} ${p.node} ${p.name} ${p.chart} ${p.family} ${p.info}`.toLowerCase().includes(q)))
})
const families = computed(() => {
  const counts = new Map<string, number>()
  for (const p of problems.value) counts.set(p.family || '其他', (counts.get(p.family || '其他') || 0) + 1)
  return [...counts].sort((a,b) => b[1] - a[1]).slice(0, 8)
})
const nodesVisible = computed(() => filteredNodes.value.slice(0, visibleCount.value))
const problemsVisible = computed(() => problems.value.slice(0, visibleCount.value))
const activeIDs = computed(() => new Set(snapshot.value?.problems.map(p => p.id) || []))
const activity = computed(() => {
  const q = query.value.trim().toLowerCase()
  return (snapshot.value?.activity || []).filter(r => !q || `${r.problem.hostname} ${r.problem.name} ${r.problem.chart} ${r.history.map(h => h.note).join(' ')}`.toLowerCase().includes(q))
})
const statusName: Record<string, string> = { live: '在线', stale: '数据过期', offline: '离线' }
const actionName: Record<string, string> = { acknowledge: '确认问题', unacknowledge: '撤销确认', comment: '补充备注' }
const formatTime = (t: number) => t > 0 ? new Date(t * 1000).toLocaleString() : '暂无样本'
const formatMetric = (m: ResourceMetric) => m.value === null ? (m.state === 'stale' ? '数据过期' : '暂无数据') : `${m.value.toFixed(1)}%`
const age = (t: number) => {
  if (!t) return '时间未知'
  const s = Math.max(0, (snapshot.value?.now || Date.now() / 1000) - t)
  return s < 60 ? `${Math.floor(s)} 秒` : s < 3600 ? `${Math.floor(s / 60)} 分钟` : s < 86400 ? `${Math.floor(s / 3600)} 小时` : `${Math.floor(s / 86400)} 天`
}
async function update(p: Problem, action: string) {
  busy.value = p.id
  actionError.value = ''
  try {
    await api.acknowledge({ id: p.id, action, note: notes.value[p.id] || '', revision: p.handling.revision })
    if (disposed) return
    delete notes.value[p.id]
    await refresh()
  } catch (e) {
    if (disposed) return
    actionError.value = e instanceof ApiError && e.status === 409
      ? '问题或处理记录已变化，请刷新后重新操作。'
      : e instanceof ApiError && e.status === 403 ? '当前账号没有问题处理权限。' : `保存失败：${String(e)}`
  } finally { if (!disposed) busy.value = '' }
}
function saveView() {
  const name = viewName.value.trim().slice(0, 40)
  if (!name) return
  const next = [{ name, query: query.value, severity: severity.value, nodeStatus: nodeStatus.value, pendingOnly: pendingOnly.value },
    ...savedViews.value.filter(v => v.name !== name)].slice(0, 10)
  try { localStorage.setItem(viewsKey, JSON.stringify(next)); savedViews.value = next; viewName.value = ''; viewMessage.value = '视图已保存在当前浏览器。' }
  catch { viewMessage.value = '浏览器未允许保存视图。' }
}
function applyView(v: SavedView) {
  query.value = v.query; severity.value = v.severity; nodeStatus.value = v.nodeStatus; pendingOnly.value = v.pendingOnly; visibleCount.value = 50
}
function removeView(name: string) {
  const next = savedViews.value.filter(v => v.name !== name)
  try { localStorage.setItem(viewsKey, JSON.stringify(next)); savedViews.value = next }
  catch { viewMessage.value = '删除失败，请检查浏览器存储权限。' }
}
function exportSnapshot() {
  if (!snapshot.value) return
  const data = { captured_at: snapshot.value.now, stale: !!error.value, scope: 'filtered', nodes: filteredNodes.value, problems: problems.value, activity: activity.value }
  const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' }))
  const a = document.createElement('a'); a.href = url; a.download = `monitor-operations-${snapshot.value.now}.json`; a.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
</script>

<template>
  <div class="operations" aria-label="运维总览">
    <div class="hero">
      <div><p class="eyebrow">MONITOR · OPERATIONS</p><h1>运维总览 <span>2.0 开发版</span></h1><p class="muted">先发现问题，再进入主机定位。跨节点状态与处理记录集中在这里。</p></div>
      <div class="hero-actions"><button @click="refresh()">刷新总览</button><button :disabled="!snapshot" @click="exportSnapshot">导出当前快照</button></div>
    </div>
    <p v-if="loading" role="status">正在读取节点和问题…</p>
    <p v-if="error" class="notice error" role="alert">{{ error }}</p>
    <template v-if="snapshot">
      <div class="summary">
        <div><span>受监控节点</span><strong>{{ snapshot.summary.nodes }}</strong><small>{{ snapshot.summary.live }} 在线</small></div>
        <div :class="{ attention: snapshot.summary.offline || snapshot.summary.stale }"><span>需要检查的连接</span><strong>{{ (snapshot.summary.offline || 0) + (snapshot.summary.stale || 0) }}</strong><small>{{ snapshot.summary.offline }} 离线 · {{ snapshot.summary.stale }} 数据过期</small></div>
        <div :class="{ critical: snapshot.summary.critical }"><span>严重问题</span><strong>{{ snapshot.summary.critical }}</strong><small>{{ snapshot.summary.warning }} 条警告</small></div>
        <div :class="{ attention: snapshot.summary.unacknowledged }"><span>待确认问题</span><strong>{{ snapshot.summary.unacknowledged }}</strong><small>确认不影响告警求值与通知</small></div>
      </div>
      <p class="updated">快照时间 {{ formatTime(snapshot.now) }} · 前台每 15 秒刷新 · {{ snapshot.persistent ? '处理记录保存到服务端' : '处理记录仅在内存中，重启会丢失' }}</p>
      <p v-if="snapshot.summary.coverage_unknown" class="notice">{{ snapshot.summary.coverage_unknown }} 个节点尚无告警规则、告警未启用或不在此 Hub 的可见范围内，零问题不代表已完成健康检查。</p>
      <form class="filters" @submit.prevent="saveView">
        <input v-model="query" placeholder="搜索主机、问题、图表或标签…" aria-label="搜索主机或问题" @input="visibleCount = 50" />
        <select v-model="nodeStatus" aria-label="节点状态"><option value="all">全部连接状态</option><option value="live">在线</option><option value="stale">数据过期</option><option value="offline">离线</option></select>
        <select v-model="severity" aria-label="问题级别"><option value="all">全部问题级别</option><option value="CRITICAL">严重</option><option value="WARNING">警告</option></select>
        <label><input type="checkbox" v-model="pendingOnly" />只看待确认</label>
        <input v-model="viewName" placeholder="视图名称" aria-label="视图名称" maxlength="40" class="view-name" />
        <button type="submit" :disabled="!viewName.trim()">保存视图</button>
      </form>
      <div class="saved-views" v-if="savedViews.length"><span class="muted">浏览器视图</span><span v-for="v in savedViews" :key="v.name"><button @click="applyView(v)">{{ v.name }}</button><button :aria-label="'删除视图 ' + v.name" @click="removeView(v.name)">×</button></span></div>
      <p role="status" v-if="viewMessage">{{ viewMessage }}</p>

      <h2>主机资源 <small>{{ filteredNodes.length }} 个节点</small></h2>
      <div class="node-grid">
        <article v-for="n in nodesVisible" :key="n.id" class="node-tile">
          <div class="row"><h3>{{ n.hostname }}</h3><span class="badge" :class="n.status">{{ statusName[n.status] }}</span></div>
          <p class="muted">{{ n.local ? '本机' : n.replica ? '副本' : n.peer ? '其他 Hub' : 'Agent' }} · {{ n.os }}/{{ n.arch }} · {{ n.charts_count }} 图表</p>
          <div class="resources"><div><span>CPU</span><b :class="{ muted: n.cpu.value === null }">{{ formatMetric(n.cpu) }}</b><progress v-if="n.cpu.value !== null" :value="n.cpu.value" max="100" aria-label="CPU 使用率" /></div><div><span>内存</span><b :class="{ muted: n.memory.value === null }">{{ formatMetric(n.memory) }}</b><progress v-if="n.memory.value !== null" :value="n.memory.value" max="100" aria-label="内存使用率" /></div></div>
          <small class="muted">CPU 样本 {{ formatTime(n.cpu.at) }}<br />内存样本 {{ formatTime(n.memory.at) }}</small>
          <div class="row tile-bottom"><span>{{ n.alarms?.critical || 0 }} 严重 · {{ n.alarms?.warning || 0 }} 警告</span><button @click="emit('drill', n.id, '')">查看主机</button></div>
        </article>
      </div>
      <p v-if="!filteredNodes.length" class="empty">没有匹配的节点，可调整搜索条件。</p>

      <div class="row section-title"><h2>问题中心 <small>{{ problems.length }} 条</small></h2><span class="muted">严重优先，待确认优先</span></div>
      <div class="families"><span v-for="[family, count] in families" :key="family">{{ family }} <b>{{ count }}</b></span></div>
      <p v-if="actionError" class="notice error" role="alert">{{ actionError }}</p>
      <p v-if="!canHandle" class="muted">当前账号只读；管理员和排障人员可以确认问题、撤销确认及添加备注。</p>
      <p v-if="!problems.length" class="empty">当前筛选范围内没有待展示的问题。</p>
      <article v-for="p in problemsVisible" :key="p.id" class="problem" :class="p.severity.toLowerCase()" :data-problem-id="p.id">
        <div class="row"><div class="problem-title"><span class="badge" :class="p.severity.toLowerCase()">{{ p.severity === 'CRITICAL' ? '严重' : '警告' }}</span><h3>{{ p.name }}</h3><span class="ack" v-if="p.handling.acknowledged">已确认</span><span class="muted" v-else>待确认</span></div><span class="muted">{{ age(p.since) }}</span></div>
        <p>{{ p.info || '暂无规则说明' }}</p>
        <p class="muted">{{ p.hostname }} · {{ p.chart }} · {{ p.value === null ? '暂无数值' : p.value.toFixed(2) + ' ' + p.units }}</p>
        <p v-if="p.stale" class="stale-text">历史告警状态，最近观测 {{ formatTime(p.updated) }}；节点恢复更新前不视为当前健康结论。</p>
        <div class="problem-actions"><button @click="emit('drill', p.node, p.chart)">定位图表</button><template v-if="canHandle"><input v-model="notes[p.id]" :disabled="busy === p.id" :aria-label="p.name + ' 处理备注'" placeholder="处理备注（可选）" maxlength="512" /><button :disabled="!!busy" @click="update(p, p.handling.acknowledged ? 'unacknowledge' : 'acknowledge')">{{ busy === p.id ? '正在保存…' : p.handling.acknowledged ? '撤销确认' : '确认问题' }}</button><button :disabled="!!busy || !notes[p.id]?.trim()" @click="update(p, 'comment')">添加备注</button></template></div>
        <details v-if="p.handling.history.length"><summary>处理记录 · 最近 {{ p.handling.history.length }} 条</summary><ol><li v-for="(h,i) in [...p.handling.history].reverse()" :key="i"><span>{{ formatTime(h.at) }} · {{ h.actor }} · {{ actionName[h.action] || h.action }}</span><p v-if="h.note">{{ h.note }}</p></li></ol></details>
      </article>
      <button v-if="filteredNodes.length > visibleCount || problems.length > visibleCount" @click="visibleCount += 50">再显示 50 条</button>
      <h2>最近处理记录 <small>服务端最近 100 个已处理的问题阶段，可按搜索词筛选</small></h2>
      <p v-if="!activity.length" class="muted">还没有匹配的处理记录。</p>
      <details v-for="record in activity" :key="record.id" class="activity">
        <summary>{{ record.problem.hostname }} · {{ record.problem.name }} · {{ activeIDs.has(record.id) ? '当前告警阶段' : '不在当前告警快照' }}</summary>
        <p class="muted">{{ record.problem.chart }} · {{ record.problem.severity }} · 起始 {{ formatTime(record.problem.since) }}</p>
        <ol><li v-for="(h,i) in [...record.history].reverse()" :key="i">{{ formatTime(h.at) }} · {{ h.actor }} · {{ actionName[h.action] || h.action }}<p v-if="h.note">{{ h.note }}</p></li></ol>
      </details>
      <p class="footnote">确认表示已有人关注，不会静默通知或将问题标为恢复。严重级别变化或恢复后再次触发，需要重新确认。镜像告警以当前 Hub 已收到的状态为准。</p>
    </template>
  </div>
</template>

<style scoped>
.operations { max-width: 1600px; margin: 0 auto; color: #e2e8f0; }
.hero { display:flex; align-items:center; justify-content:space-between; gap:20px; padding:22px 0; flex-wrap:wrap; }
.eyebrow { color:#5eead4; font-size:11px; letter-spacing:.16em; }
h1 { font-size:28px; margin:8px 0; letter-spacing:-.03em; } h1 span { display:inline-block; font-size:11px; color:#99f6e4; background:#134e4a; border:1px solid #115e59; border-radius:5px; padding:4px 7px; vertical-align:middle; letter-spacing:0; }
h2 { font-size:17px; margin:26px 0 12px; } h2 small { font-size:12px; color:#94a3b8; font-weight:400; margin-left:8px; } h3 { font-size:14px; margin:0; overflow-wrap:anywhere; }
p { font-size:13px; line-height:1.65; margin:8px 0; } .muted,.updated,.footnote { color:#94a3b8; } .updated { font-size:12px; margin:12px 0; }
.hero-actions,.filters,.saved-views,.problem-actions,.families,.problem-title { display:flex; gap:8px; align-items:center; flex-wrap:wrap; }
button,input,select { font:inherit; font-size:12px; border:1px solid #334155; border-radius:7px; background:#111d31; color:#e2e8f0; padding:8px 10px; max-width:100%; } button { cursor:pointer; } button:hover:not(:disabled) { border-color:#5eead4; color:#5eead4; } button:disabled { opacity:.5; cursor:default; } input:focus,select:focus,button:focus-visible { outline:2px solid #5eead4; outline-offset:2px; }
.summary { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:12px; } .summary>div { background:linear-gradient(120deg,#122238,#0f172a); border:1px solid #23334b; border-radius:12px; padding:18px; display:flex; flex-direction:column; gap:8px; } .summary span { font-size:12px; color:#cbd5e1; } .summary strong { font-size:34px; font-weight:600; } .summary small { font-size:11px; color:#94a3b8; } .summary .critical strong { color:#fda4af; } .summary .attention strong { color:#fcd34d; }
.notice { background:#332b18; border:1px solid #625025; color:#fde68a; padding:10px 14px; border-radius:8px; } .notice.error { color:#fecaca; background:#3a1e2a; border-color:#7f1d1d; }
.filters { padding:14px; background:#0c1526; border:1px solid #23334b; border-radius:10px; margin:20px 0 10px; } .filters>input:first-child { flex:1; min-width:min(250px,100%); } .filters label { font-size:12px; white-space:nowrap; } .view-name { width:120px; } .saved-views { font-size:12px; }
.row { display:flex; gap:12px; justify-content:space-between; align-items:center; flex-wrap:wrap; }.node-grid { display:grid; grid-template-columns:repeat(auto-fill,minmax(min(290px,100%),1fr)); gap:12px; } .node-tile { background:#0f1a2d; border:1px solid #27364c; border-radius:10px; padding:16px; min-width:0; } .badge { font-size:11px; padding:3px 7px; border-radius:5px; background:#26334a; color:#cbd5e1; white-space:nowrap; } .badge.live,.ack { color:#6ee7b7; }.badge.offline,.badge.critical { color:#fda4af; background:#4c1d2a; } .badge.stale,.badge.warning { color:#fde68a; background:#46361c; }
.resources { display:grid; grid-template-columns:1fr 1fr; gap:20px; margin:18px 0; } .resources>div { display:flex; flex-direction:column; gap:7px; } .resources span { font-size:11px; color:#94a3b8; } .resources b { font-size:21px; font-weight:500; } .resources progress { height:5px; width:100%; accent-color:#2dd4bf; } .tile-bottom { margin-top:16px; font-size:12px; } .tile-bottom span { color:#cbd5e1; }
.section-title h2 { margin-bottom:12px; } .section-title>span { font-size:12px; } .families { margin:0 0 12px; font-size:12px; }.families>span { padding:5px 9px; background:#1e293b; border-radius:5px; } .families b { margin-left:6px; color:#5eead4; }
.problem { background:#0f1a2d; border:1px solid #27364c; border-left:3px solid #fbbf24; border-radius:8px; padding:16px; margin:12px 0; overflow-wrap:anywhere; } .problem.critical { border-left-color:#fb7185; } .problem-title { flex:1; } .ack { font-size:11px; }.problem-actions { margin-top:14px; }.problem-actions input { flex:1; min-width:min(200px,100%); }.stale-text { color:#fcd34d; }.problem details { margin-top:12px; border-top:1px solid #27364c; padding-top:12px; font-size:12px; } summary { cursor:pointer; color:#94a3b8; } ol { padding-left:20px; color:#94a3b8; } li { margin:10px 0; } li p { color:#e2e8f0; white-space:pre-wrap; } .empty { padding:20px; border:1px dashed #334155; border-radius:8px; color:#94a3b8; } .footnote { margin:24px 0; font-size:12px; }
.activity { border:1px solid #27364c; border-radius:7px; padding:12px; margin:8px 0; font-size:12px; overflow-wrap:anywhere; }
@media(max-width:760px) { .summary { grid-template-columns:repeat(2,minmax(0,1fr)); }.summary>div { padding:12px; }.summary strong { font-size:28px; } .hero { padding:10px 0; } h1 { font-size:24px; }.filters { padding:10px; }.filters>input:first-child { flex-basis:100%; } .hero-actions { width:100%; }.problem-actions input { flex-basis:100%; } }
</style>
