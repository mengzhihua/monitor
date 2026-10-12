<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import type { Alarm, AlarmLogEntry, Chart, FunctionInfo, Info, NodeInfo, Problem } from './api'
import type { AIProblemSelection } from './ai'
import { ApiError, api, auth, selection } from './api'
import { live } from './live'
import { usePolling } from './polling'
import { pageVisible } from './visibility'
import { sharedRequest } from './sharedRequest'
import MetricChart from './components/MetricChart.vue'
import OperationsPanel from './components/OperationsPanel.vue'
import ServicesPanel from './components/ServicesPanel.vue'
import AlarmsPanel from './components/AlarmsPanel.vue'
import AlertRulesPanel from './components/AlertRulesPanel.vue'
import TemplatesPanel from './components/TemplatesPanel.vue'
import FunctionsPanel from './components/FunctionsPanel.vue'
import LogsPanel from './components/LogsPanel.vue'
import WeightsPanel from './components/WeightsPanel.vue'
import HubPanel from './components/HubPanel.vue'
import CloudPanel from './components/CloudPanel.vue'
import ConfigPanel from './components/ConfigPanel.vue'
import ContextsPanel from './components/ContextsPanel.vue'
import DashboardsPanel from './components/DashboardsPanel.vue'
import ChartTimeline from './components/ChartTimeline.vue'
import DashboardTools from './components/DashboardTools.vue'
import AIAnalysisPanel from './components/AIAnalysisPanel.vue'
import AppIcon from './components/AppIcon.vue'
const ShellPanel = defineAsyncComponent(() => import('./components/ShellPanel.vue'))
import { decodeTimeline, encodeTimeline, timelineKey, type ChartTimeline as TimelineState } from './chartTimeline'

const initialView = new URL(location.href).searchParams.get('view')
type Workspace = 'operations' | 'charts' | 'services' | 'alarms'
const workspace = ref<Workspace>(initialView === 'charts' || initialView === 'services' || initialView === 'alarms' ? initialView : 'operations')
const mobileQuery = window.matchMedia('(max-width: 760px)')
const isMobile = ref(mobileQuery.matches)
const toolsMenu = ref<InstanceType<typeof DashboardTools> | null>(null)
const toolsOpen = ref(false)
function syncMobile() { isMobile.value = mobileQuery.matches }
function jumpGroup(event: Event) {
  activeSection.value = (event.target as HTMLSelectElement).value
  document.getElementById(activeSection.value)?.scrollIntoView({ block: 'start' })
}
function scrollToWorkspace() {
  void nextTick(() => document.getElementById('workspace-main')?.scrollIntoView({ block: 'start' }))
}
function navigate(work: Workspace) {
  workspace.value = work
  showAlarms.value = work === 'alarms'
  if (isMobile.value) scrollToWorkspace()
}
function closeAlarms() {
  showAlarms.value = false
  if (workspace.value === 'alarms') workspace.value = 'operations'
}
function toggleAlarms() {
  if (showAlarms.value) closeAlarms()
  else { showAlarms.value = true; if (isMobile.value) scrollToWorkspace() }
}
watch(workspace, value => {
  const url = new URL(location.href)
  url.searchParams.set('view', value)
  url.searchParams.delete('token')
  history.replaceState(history.state, '', url)
})
async function drill(node: string, chart: string) {
  await selectNode(node)
  if (chartEnd.value !== null) {
    chartEnd.value = null
    timelineNotice.value = '已返回实时，以查看当前运维问题对应的指标。' + timelineNotice.value
  }
  filter.value = chart
  workspace.value = 'charts'
  dashboardView.value = 'all'
}
const info = ref<Info | null>(null)
const charts = ref<Chart[]>([])
const error = ref('')
const needToken = ref(false)
const tokenInput = ref('')
const loginError = ref('')
const connected = ref(false)
let initialTimeline: TimelineState = { window: 300, end: null }
let timelineReadError = ''
try { const raw = sessionStorage.getItem(timelineKey); if (raw) initialTimeline = decodeTimeline(raw) }
catch { timelineReadError = '无法恢复查看时间，已使用实时模式；原始配置未覆盖。' }
const windowSec = ref(initialTimeline.window)
const chartEnd = ref<number | null>(initialTimeline.end)
const timelineNotice = ref(timelineReadError)
watch([windowSec, chartEnd], () => {
  try { sessionStorage.setItem(timelineKey, encodeTimeline({ window: windowSec.value, end: chartEnd.value })); timelineNotice.value = '' }
  catch { timelineNotice.value = '查看时间暂存失败，本次仍可使用；刷新后可能无法恢复。' }
}, { flush: 'sync' })
const filter = ref('')
const activeSection = ref('')
const alarms = ref<Alarm[]>([])
const alarmLog = ref<AlarmLogEntry[]>([])
const showAlarms = ref(initialView === 'alarms')
const showAlertRules = ref(false)
const alertRulesPanel = ref<InstanceType<typeof AlertRulesPanel> | null>(null)
function toggleAlertRules() {
  if (showAlertRules.value) alertRulesPanel.value?.requestClose()
  else showAlertRules.value = true
}
const functions = ref<FunctionInfo[]>([])
const showFunctions = ref(false)
const showLogs = ref(false)
const showWeights = ref(false)
const showShell = ref(false)
const showAI = ref(false)
const aiProblem = shallowRef<AIProblemSelection | null>(null)
const showHub = ref(false)
const showTemplates = ref(false)
const showCloud = ref(false)
const showConfig = ref(false)
const configPanel = ref<InstanceType<typeof ConfigPanel> | null>(null)
function toggleConfig() {
  if (showConfig.value) configPanel.value?.requestClose()
  else showConfig.value = true
}
const showContexts = ref(false)
const dashboardView = ref('all')
const oidcAvailable = ref(false)
const nodes = ref<NodeInfo[]>([])
const selectedNode = ref('')
const nodeNotice = ref('')
const NODE_KEY = 'monitor.node'
// Node dropdown: the native <select> popup caps visible rows at the browser's
// discretion (observed 5); a custom menu keeps ~12 rows visible and scrolls.
const nodeMenuOpen = ref(false)
function nodeMark(n: NodeInfo): string {
  return n.local ? '◆ ' : n.status === 'live' ? '● ' : n.status === 'stale' ? '◐ ' : '○ '
}
function nodeName(n: NodeInfo): string {
  return n.hostname + (n.local ? ' (hub)' : n.replica ? ' (replica)' : n.peer ? ' (peer)' : '')
}
function toggleNodeMenu() { nodeMenuOpen.value = !nodeMenuOpen.value }
function closeNodeMenu() { nodeMenuOpen.value = false }
function pickNode(id: string) { closeNodeMenu(); void selectNode(id) }
const isHub = computed(() => info.value?.mode === 'hub')
// The log panel needs the node's `logs` function; the local host always has
// the standalone query fallback, and an empty list just means "still loading".
const logsAvailable = computed(() => !selectedNode.value || !functions.value.length || functions.value.some((f) => f.name === 'logs'))
const currentNode = computed(() => nodes.value.find((n) => n.id === selectedNode.value) ?? null)
/** Remote nodes have no local health engine; their alarms are mirrored from the agent. */
const healthOn = computed(() => (selectedNode.value ? true : info.value?.alarms != null))
const raised = computed(() => ({
  warning: alarms.value.filter((a) => a.status === 'WARNING').length,
  critical: alarms.value.filter((a) => a.status === 'CRITICAL').length,
}))
const tools = computed(() => [
  { id: 'services', label: '服务与拓扑', title: '服务与拓扑', description: '服务状态与可用性', active: workspace.value === 'services', visible: isMobile.value },
  { id: 'rules', label: '告警规则', title: '本机告警规则', description: '查看与管理本机规则', active: showAlertRules.value, visible: true },
  { id: 'functions', label: `ƒ ${functions.value.length}`, title: 'Functions（实时进程表等）', description: '实时进程与诊断', active: showFunctions.value, visible: !!functions.value.length },
  { id: 'logs', label: '日志', title: '日志', description: '查询当前节点日志', active: showLogs.value, visible: logsAvailable.value },
  { id: 'hub', label: 'Hub', title: 'Hub：Space / Room / claim', description: '节点分组与接入', active: showHub.value, visible: isHub.value },
  { id: 'cloud', label: 'Cloud', title: 'Cloud 控制台', description: '云节点控制台', active: showCloud.value, visible: isHub.value },
  { id: 'templates', label: '模板', title: 'Zabbix 模板 / 主机组 / 资产', description: '主机组与资产模板', active: showTemplates.value, visible: isHub.value },
  { id: 'config', label: '配置', title: '配置：本机与节点', description: '本机与节点配置', active: showConfig.value, visible: info.value?.user?.role === 'admin' },
  { id: 'shell', label: 'WebShell', title: 'WebShell 主机终端', description: '本机 Shell 与 SSH 交互终端', active: showShell.value, visible: info.value?.user?.role === 'admin' && !!auth.token },
  { id: 'ai', label: 'AI 分析', title: 'AI 分析', description: '自定义模型分析监控快照', active: showAI.value, visible: true },
  { id: 'weights', label: '异常顾问', title: '异常顾问 / 关联分析', description: '异常与指标关联', active: showWeights.value, visible: true },
  { id: 'contexts', label: 'Context', title: 'Context 总览', description: '按指标类型浏览', active: showContexts.value, visible: true },
  { id: 'logout', label: '退出登录', title: '退出登录', description: '结束当前浏览器登录', active: false, visible: isMobile.value && !!auth.token },
].filter(tool => tool.visible))
function activateTool(id: string) {
  switch (id) {
    case 'services': navigate('services'); break
    case 'rules': toggleAlertRules(); break
    case 'functions': showFunctions.value = !showFunctions.value; break
    case 'logs': showLogs.value = !showLogs.value; break
    case 'hub': showHub.value = !showHub.value; break
    case 'cloud': showCloud.value = !showCloud.value; break
    case 'templates': showTemplates.value = !showTemplates.value; break
    case 'config': toggleConfig(); break
    case 'shell': showShell.value = !showShell.value; break
    case 'ai': aiProblem.value = null; showAI.value = !showAI.value; if (showAI.value) void scrollToAI(); break
    case 'weights': showWeights.value = !showWeights.value; break
    case 'contexts': showContexts.value = !showContexts.value; break
    case 'logout': void logout(); break
  }
  if (isMobile.value && id !== 'ai' && id !== 'shell') scrollToWorkspace()
}

async function openCloudNode(id: string) {
  showCloud.value = false
  const node = nodes.value.find(n => n.id === id)
  await drill(node?.local ? '' : id, '')
  if (isMobile.value) scrollToWorkspace()
}

async function scrollToShell() {
 await nextTick()
 const panel = document.querySelector<HTMLElement>('section.shell-panel')
 if (!panel) return
 const header = document.querySelector<HTMLElement>('#app > header')
 const offset = !isMobile.value && header ? header.getBoundingClientRect().height + 12 : 12
 window.scrollTo({top:Math.max(0,window.scrollY + panel.getBoundingClientRect().top-offset),behavior:'auto'})
}
async function scrollToAI() {
  await nextTick()
  const panel = document.querySelector<HTMLElement>('section.ai-panel')
  if (!panel) return
  const header = document.querySelector<HTMLElement>('#app > header')
  const offset = !isMobile.value && header ? header.getBoundingClientRect().height + 12 : 12
  window.scrollTo({ top: Math.max(0, window.scrollY + panel.getBoundingClientRect().top - offset), behavior: 'auto' })
}

const windows = [
  { label: '1 分钟', v: 60 },
  { label: '5 分钟', v: 300 },
  { label: '15 分钟', v: 900 },
  { label: '1 小时', v: 3600 },
  { label: '6 小时', v: 21600 },
  { label: '24 小时', v: 86400 },
  { label: '7 天', v: 604800 },
]

/** Group charts by the first segment of the chart id (system, cpu, mem, disk…). */
const sections = computed(() => {
  const q = filter.value.trim().toLowerCase()
  const groups = new Map<string, Chart[]>()
  for (const c of charts.value) {
    if (q && !(c.id + ' ' + c.title + ' ' + c.family).toLowerCase().includes(q)) continue
    const key = c.id.split('.')[0]!.replace(/_.*/, '')
    if (!groups.has(key)) groups.set(key, [])
    groups.get(key)!.push(c)
  }
  return [...groups.entries()]
    .map(([name, list]) => ({
      name,
      charts: list.sort((a, b) => a.priority - b.priority || a.id.localeCompare(b.id)),
    }))
    .sort((a, b) => a.charts[0]!.priority - b.charts[0]!.priority)
})

async function refreshNodes(signal: AbortSignal) {
  if (!isHub.value) return
  try {
    const result = await api.nodes(signal)
    if (signal.aborted) return
    nodes.value = result.nodes
    if (selectedNode.value && !nodes.value.some((n) => n.id === selectedNode.value)) await selectNode('')
  } catch { /* transient */ }
}

/** Forget an offline node on the hub: metadata and history are dropped server-side. */
async function forgetNode(n: NodeInfo) {
  if (!confirm(`确定删除离线节点「${n.hostname}」吗？该节点的图表与历史数据将一并删除，且不可恢复。`)) return
  try {
    await api.forgetNode(n.id)
    error.value = ''
    await refreshNodes(new AbortController().signal)
  } catch (e) {
    error.value = `删除节点失败：${e instanceof Error ? e.message : String(e)}`
  }
}

/** Switch the whole dashboard (charts, alarms, functions, live socket) to another node. */
async function selectNode(id: string) {
  if (id === selectedNode.value) return
  selectedNode.value = id
  aiProblem.value = null
  selection.node = id
  try {
    if (id) sessionStorage.setItem(NODE_KEY, id)
    else sessionStorage.removeItem(NODE_KEY)
    nodeNotice.value = ''
  } catch { nodeNotice.value = '浏览器无法记住所选节点，本次切换仍然生效；刷新后可能恢复到先前节点。' }
  charts.value = []
  alarms.value = []
  alarmLog.value = []
  functions.value = []
  showFunctions.value = false
  await refresh()
  if (!needToken.value) live.restart()
}

async function solveProblem(p: Problem) {
  const node = p.hostname === info.value?.host.hostname && p.node === info.value?.host.id ? '' : p.node
  await selectNode(node)
  if (needToken.value || selectedNode.value !== node) return
  aiProblem.value = { id: p.id, name: p.name, chart: p.chart }
  showAI.value = true
  await scrollToAI()
}

async function load(signal: AbortSignal) {
  const node = selectedNode.value
  try {
    const [i, c] = await Promise.all([api.info(signal), api.charts(signal)])
    if (signal.aborted) return
    info.value = i
    await refreshNodes(signal)
    if (signal.aborted || selectedNode.value !== node) return // switched while in flight; a newer refresh owns the state
    const list = Object.values(c.charts)
    // keep object identity stable so chart components don't remount
    const byId = new Map(charts.value.map((x) => [x.id, x]))
    charts.value = list.map((x) => {
      const old = byId.get(x.id)
      if (old) { Object.assign(old, x); return old }
      return x
    })
    error.value = ''
    needToken.value = false
    if (workspace.value === 'alarms') showAlarms.value = true
    await Promise.all([refreshAlarms(signal), refreshAlarmLog(signal), refreshFunctions(signal)])
  } catch (e) {
    if (signal.aborted) return
    if (e instanceof ApiError && e.status === 401) {
      needToken.value = true
      toolsMenu.value?.close()
      live.stop()
      connected.value = false
      info.value = null
      charts.value = []
      nodes.value = []
      alarms.value = []
      alarmLog.value = []
      functions.value = []
      showAI.value = showFunctions.value = showLogs.value = showWeights.value = showHub.value = showCloud.value = showContexts.value = showAlarms.value = showConfig.value = showAlertRules.value = showTemplates.value = false
      error.value = ''
      return
    }
    if (e instanceof ApiError && e.status === 404 && selectedNode.value) {
      // node was forgotten (or never existed on this hub): fall back to the hub itself
      await selectNode('')
      return
    }
    error.value = String(e)
  }
}

const alarmSnapshotRead = sharedRequest(api.alarms, result => {
  alarms.value = Object.values(result.alarms)
})
const alarmLogRead = sharedRequest(async signal => {
  const known = new Set(alarmLog.value.map(event => event.unique_id))
  return { entries: await api.alarmLog(0, signal), known }
}, ({ entries, known }) => {
  // Preserve live events received while history was loading, without duplicates.
  // The server snapshot remains authoritative for retained/updated history.
  const arrived = alarmLog.value.filter(event => !known.has(event.unique_id))
  const byID = new Map([...arrived, ...entries].map(event => [event.unique_id, event]))
  alarmLog.value = [...byID.values()].sort((a, b) => a.unique_id - b.unique_id).slice(-1000)
})

function refreshAlarms(signal?: AbortSignal): Promise<void> {
  if (!healthOn.value || !pageVisible.value || needToken.value) return Promise.resolve()
  return alarmSnapshotRead.run(signal)
}

function refreshAlarmLog(signal?: AbortSignal): Promise<void> {
  if (!showAlarms.value || !healthOn.value || !pageVisible.value || needToken.value) return Promise.resolve()
  return alarmLogRead.run(signal)
}

function cancelAlarmReads() {
  alarmSnapshotRead.cancel()
  alarmLogRead.cancel()
}

watch([selectedNode, pageVisible, needToken], cancelAlarmReads, { flush: 'sync' })
watch(showAlarms, open => { if (open) void refreshAlarmLog(); else alarmLogRead.cancel() })

async function refreshFunctions(signal?: AbortSignal) {
  const node = selectedNode.value
  try {
    const f = await api.functions(signal)
    if (signal?.aborted || selectedNode.value !== node) return
    functions.value = f
  } catch { /* transient */ }
}

/** Apply a live transition without waiting for the next poll. */
function onAlarmEvent(e: AlarmLogEntry) {
  if (!alarmLog.value.some((x) => x.unique_id === e.unique_id)) alarmLog.value = [...alarmLog.value, e].slice(-1000)
  const a = alarms.value.find((x) => x.id === e.alarm_id || (x.chart === e.chart && x.name === e.name))
  if (!a) {
    void refreshAlarms()
    return
  }
  a.status = e.status; a.value = e.value; a.last_updated = e.when; a.last_status_change = e.when
  if (e.repeat) return
  const raised = e.status === 'WARNING' || e.status === 'CRITICAL'
  if (!raised) {
    if (e.status !== 'CLEAR' || e.old_status === 'WARNING' || e.old_status === 'CRITICAL') clearAlarmSustain(a)
    a.recovery_hold = undefined
    return
  }
  const level = e.status === 'CRITICAL' ? 4 : 3
  const pending = a.pending_status === 'CRITICAL' ? 4 : a.pending_status === 'WARNING' ? 3 : 0
  if (pending > 0 && pending <= level) {
    a.pending_status = undefined; a.pending_since = undefined; a.pending_until = undefined
  }
  a.hold_until = undefined
  a.recovery_hold = undefined
}

function clearAlarmSustain(a: Alarm) {
  a.pending_status = undefined; a.pending_since = undefined; a.pending_until = undefined; a.hold_until = undefined
}

async function logout() {
  try {
    const response = await fetch('/api/v1/auth/oidc/logout', { method: 'POST', headers: { Authorization: `Bearer ${auth.token}` } })
    if (!response.ok && response.status !== 401) throw new Error('退出失败，请重试')
    auth.token = ''
    location.reload()
  } catch (e) { error.value = String(e) }
}

async function submitToken() {
  loginError.value = ''
  auth.token = tokenInput.value.trim()
  tokenInput.value = ''
  await refresh()
  if (!needToken.value) live.restart()
  else {
    auth.token = ''
    loginError.value = '密码或访问令牌不正确，请重试。'
  }
}

function fmtUptime(s: number) {
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60)
  return d ? `${d}d ${h}h` : h ? `${h}h ${m}m` : `${m}m ${s % 60}s`
}

onMounted(() => {
  mobileQuery.addEventListener('change', syncMobile)
  auth.fromURL()
  const saved = new URL(location.href).searchParams.get('node') ?? sessionStorage.getItem(NODE_KEY) ?? ''
  selectedNode.value = saved
  selection.node = saved
  live.onState = (up) => (connected.value = up)
  live.onAlarm = onAlarmEvent
  document.addEventListener('click', closeNodeMenu)
  document.addEventListener('keydown', onDocKeydown)
  fetch('/api/v1/auth/oidc/status').then((r) => r.json()).then((s) => { oidcAvailable.value = s.enabled === true }).catch(() => {})
})
const refresh = usePolling(async (signal) => {
  await load(signal)
  if (!signal.aborted && !needToken.value) live.start()
}, 30000)
function onDocKeydown(e: KeyboardEvent) { if (e.key === 'Escape') closeNodeMenu() }
onBeforeUnmount(() => {
  mobileQuery.removeEventListener('change', syncMobile)
  cancelAlarmReads()
  live.stop()
  document.removeEventListener('click', closeNodeMenu)
  document.removeEventListener('keydown', onDocKeydown)
})
</script>

<template>
  <header>
    <div class="brand">
      <span class="logo">◉</span> Monitor
      <span v-if="info && !currentNode" class="host" :title="info.host.hostname">{{ info.host.hostname }} · {{ info.host.os }}/{{ info.host.arch }}</span>
      <span v-else-if="currentNode" class="host" :title="currentNode.hostname">{{ currentNode.hostname }} · {{ currentNode.os }}/{{ currentNode.arch }}
        <i :class="['node-status', currentNode.status]">{{ currentNode.status }}</i></span>
    </div>
    <div class="meta" v-if="info">
      <div v-if="isHub" class="node-select" @click.stop>
        <button type="button" class="node-select-btn" title="节点" @click="toggleNodeMenu">
          {{ currentNode ? nodeMark(currentNode) + nodeName(currentNode) : (info?.host.hostname ?? '节点') }} ▾
        </button>
        <div v-if="nodeMenuOpen" class="node-menu" role="listbox" aria-label="节点">
          <button v-for="n in nodes" :key="n.id" type="button" role="option" class="node-item"
            :class="{ sel: n.id === selectedNode, live: n.status === 'live', stale: n.status === 'stale', offline: n.status === 'offline' }"
            @click="pickNode(n.id)">
            {{ nodeMark(n) }}{{ nodeName(n) }}
          </button>
        </div>
      </div>
      <span v-if="isHub" class="nodes-count" title="在线节点 / 全部节点">{{ nodes.filter((n) => n.status === 'live').length }}/{{ nodes.length }} nodes</span>
      <span v-if="info.stream" :class="['dot', info.stream.connected ? 'on' : 'off']"
        :title="info.stream.connected ? '已上报到 ' + info.stream.destination : ('未连接 Hub' + (info.stream.last_error ? ': ' + info.stream.last_error : ''))">⇡</span>
      <span class="chart-count">{{ currentNode ? currentNode.charts_count : info.charts_count }} charts</span>
      <span v-if="!currentNode" class="desktop-detail">{{ info.metrics_count }} metrics</span>
      <span v-if="!currentNode" class="desktop-detail">up {{ fmtUptime(info.uptime) }}</span>
      <button v-if="healthOn" class="alarms-btn" :class="{ crit: raised.critical, warn: !raised.critical && raised.warning, open: showAlarms }"
        @click="toggleAlarms" title="告警">
        ⚠ <b v-if="raised.critical">{{ raised.critical }}</b><b v-else-if="raised.warning">{{ raised.warning }}</b><span v-else>0</span>
      </button>
      <DashboardTools ref="toolsMenu" :mobile="isMobile" :tools="tools" @select="activateTool" @visibility="value => toolsOpen = value" />
      <span :class="['dot', connected ? 'on' : 'off']" :title="connected ? 'live' : 'reconnecting'">●</span>
    </div>
    <div class="controls" v-if="workspace === 'charts'">
      <input v-model="filter" placeholder="筛选图表…" aria-label="筛选图表" type="search" />
      <select v-model.number="windowSec" aria-label="查看时长">
        <option v-for="w in windows" :key="w.v" :value="w.v">{{ w.label }}</option>
      </select>
    </div>
    <button v-if="info && auth.token && !isMobile" class="logout" @click="logout">退出登录</button>
  </header>

  <div v-if="info" class="workspace-tabs" aria-label="工作区">
    <button :class="{ selected: workspace === 'operations' }" @click="navigate('operations')"><AppIcon name="operations" :size="16" />运维总览</button>
    <button :class="{ selected: workspace === 'services' }" @click="navigate('services')"><AppIcon name="services" :size="16" />服务与拓扑</button>
    <button :class="{ selected: workspace === 'charts' }" @click="navigate('charts')"><AppIcon name="charts" :size="16" />指标图表</button>
  </div>
  <div class="layout">
    <nav class="chart-nav" v-if="workspace === 'charts' && dashboardView === 'all'">
      <a v-for="s in sections" :key="s.name" :href="'#' + s.name" :class="{ active: activeSection === s.name }"
        @click="activeSection = s.name">{{ s.name }} <small>{{ s.charts.length }}</small></a>
      <div class="collectors" v-if="info && !currentNode">
        <div class="nav-title">采集器</div>
        <div v-for="c in info.collectors" :key="c.name" class="col" :class="{ bad: !c.enabled || c.error }" :title="c.error || (c.enabled ? '采集正常' : '已禁用或等待依赖恢复')">
          <span>{{ c.name }}</span>
          <small>{{ c.enabled ? (c.error ? 'error' : c.last_run_ms + 'ms') : 'off' }}</small>
        </div>
        <template v-if="info.plugins && info.plugins.length">
          <div class="nav-title">插件</div>
          <div v-for="p in info.plugins" :key="p.name" class="col"
            :class="{ bad: p.state !== 'running' && p.state !== 'starting' }" :title="p.error || p.command">
            <span>{{ p.name }}</span>
            <small>{{ p.state === 'running' ? p.stats.charts + ' charts' : p.state }}</small>
          </div>
        </template>
      </div>
    </nav>

    <main id="workspace-main">
      <div v-if="isHub && workspace === 'charts'" class="node-overview" aria-label="节点健康总览">
        <div v-for="n in nodes" :key="n.id" :class="['node-card', n.status]" role="button" tabindex="0"
          @click="selectNode(n.id)" @keydown.enter.prevent="selectNode(n.id)" @keydown.space.prevent="selectNode(n.id)">
          <b>{{ n.hostname }}</b><span>{{ n.status === 'live' ? '在线' : n.status === 'stale' ? '数据过期' : '离线' }}</span>
          <small>{{ n.charts_count }} 图表 · {{ n.alarms?.critical || 0 }} 严重告警{{ n.replica ? ' · 副本' : '' }}</small>
          <small v-if="n.last_data">最后数据：{{ new Date(n.last_data * 1000).toLocaleString() }}</small>
          <button v-if="n.status === 'offline' && !n.local" class="node-del" @click.stop="forgetNode(n)">删除节点</button>
        </div>
      </div>
      <div v-if="info?.db?.persistence?.error" class="banner">数据保存失败：{{ info.db.persistence.error }}</div>
      <div v-if="error" class="banner">{{ error }}</div>
      <p v-if="nodeNotice" class="node-notice" role="status">{{ nodeNotice }}</p>
      <h1 v-if="workspace === 'alarms' && info" class="mobile-page-title">告警 <small>{{ currentNode?.hostname || info.host.hostname }}</small></h1>
      <p v-if="workspace === 'alarms' && info && !healthOn" class="empty">当前节点未启用告警。</p>
      <AlarmsPanel :key="selectedNode" v-if="showAlarms && healthOn" :alarms="alarms" :log="alarmLog" :refresh="refreshAlarms" :can-manage="info?.user?.role === 'admin' && !selectedNode" :can-close="['admin', 'troubleshooter'].includes(info?.user?.role || '') && !selectedNode" @close="closeAlarms" />
      <AlertRulesPanel ref="alertRulesPanel" v-if="showAlertRules && info" :role="info.user?.role || 'viewer'" @close="showAlertRules = false" @changed="refresh()" />
      <FunctionsPanel v-if="showFunctions && functions.length" :functions="functions" @close="showFunctions = false" />
      <LogsPanel :key="'logs:' + selectedNode" v-if="showLogs" @close="showLogs = false" />
      <ShellPanel v-if="showShell && info && !needToken && info.user?.role === 'admin'" :node="selectedNode" @ready="scrollToShell" @close="showShell = false" />
      <AIAnalysisPanel v-if="showAI && info" :node="selectedNode" :charts="charts" :problem="aiProblem" :role="info.user?.role || 'viewer'" @close="showAI = false; aiProblem = null" />
      <WeightsPanel :key="'weights:' + selectedNode" v-if="showWeights" @close="showWeights = false" @pick="(id) => { workspace = 'charts'; dashboardView = 'all'; filter = id; showWeights = false }" />
      <ContextsPanel :key="'contexts:' + selectedNode" v-if="showContexts" @close="showContexts = false" @pick="(id) => { workspace = 'charts'; dashboardView = 'all'; filter = id; showContexts = false }" />
      <HubPanel v-if="showHub && isHub" @close="showHub = false" />
      <CloudPanel v-if="showCloud && isHub" @close="showCloud = false" @pick="openCloudNode" />
      <TemplatesPanel v-if="showTemplates && isHub" :role="info?.user?.role || 'viewer'" :nodes="nodes" @close="showTemplates = false" />
      <ConfigPanel ref="configPanel" v-if="showConfig && info?.user?.role === 'admin'" :can-manage="info?.user?.role === 'admin'" :is-hub="isHub" @close="showConfig = false" />
      <form v-if="needToken" class="token" @submit.prevent="submitToken">
        <h1>登录 Monitor</h1>
        <p>请输入登录密码或访问令牌。</p>
        <p>首次部署的密码保存在服务器数据目录的 web-password 文件中，请联系管理员获取。</p>
        <input v-model="tokenInput" type="password" placeholder="登录密码或访问令牌" aria-label="登录密码或访问令牌" autocomplete="current-password" required autofocus />
        <p v-if="loginError" role="alert">{{ loginError }}</p>
        <button type="submit">进入</button>
        <a v-if="oidcAvailable" class="oidc" :href="api.oidcLoginURL()">使用 OIDC 登录</a>
      </form>
      <OperationsPanel v-if="info && !needToken && workspace === 'operations'" :role="info.user?.role || 'viewer'" @drill="drill" @solve="solveProblem" />
      <ServicesPanel v-if="info && !needToken && workspace === 'services'" />
      <template v-if="workspace === 'charts'">
      <ChartTimeline v-if="info && !needToken" :end="chartEnd" :window="windowSec" :notice="timelineNotice" @update:end="value => chartEnd = value" />
      <div v-if="info && !needToken" class="view-switch" aria-label="看板视图">
        <button :aria-pressed="dashboardView === 'all'" @click="dashboardView = 'all'">全部指标</button>
        <button :aria-pressed="dashboardView === 'presets'" @click="dashboardView = 'presets'">常用聚合看板</button>
      </div>
      <label v-if="info && !needToken && dashboardView === 'all'" class="mobile-groups">图表分组<select aria-label="跳转图表分组" :value="activeSection" @change="jumpGroup"><option value="" disabled>选择分组</option><option v-for="group in sections" :key="group.name" :value="group.name">{{ group.name }} · {{ group.charts.length }} 图表</option></select></label>
      <DashboardsPanel v-if="info && !needToken && dashboardView === 'presets'" :charts="charts" :window="windowSec" :end="chartEnd" :filter="filter" :node="selectedNode" />
      <template v-if="dashboardView === 'all'">
      <section v-for="s in sections" :key="s.name" :id="s.name">
        <h2>{{ s.name }}</h2>
        <div class="grid">
          <MetricChart v-for="c in s.charts" :key="selectedNode + ':' + c.id" :chart="c" :window="windowSec" :end="chartEnd" />
        </div>
      </section>
      </template>
      <p v-if="!charts.length && !error && !needToken" class="empty">
        {{ currentNode && currentNode.status === 'offline' ? '节点离线，暂无数据。' : '等待数据…' }}
      </p>
      </template>
    </main>
  </div>
  <nav v-if="info && !needToken" class="mobile-nav" aria-label="手机导航">
    <button :aria-current="workspace === 'operations' && !toolsOpen ? 'page' : undefined" @click="navigate('operations')"><AppIcon name="operations" :size="21" /><span>总览</span></button>
    <button :aria-current="workspace === 'charts' && !toolsOpen ? 'page' : undefined" @click="navigate('charts')"><AppIcon name="charts" :size="21" /><span>图表</span></button>
    <button :aria-current="workspace === 'alarms' && !toolsOpen ? 'page' : undefined" @click="navigate('alarms')"><AppIcon name="alarms" :size="21" /><span>告警 <b v-if="raised.critical + raised.warning">{{ raised.critical + raised.warning }}</b></span></button>
    <button :aria-expanded="toolsOpen" :aria-current="toolsOpen ? 'page' : undefined" aria-haspopup="dialog" @click="toolsMenu?.open($event.currentTarget)"><AppIcon name="more" :size="21" /><span>更多</span></button>
  </nav>
</template>

<style scoped>
.workspace-tabs { display:flex; gap:6px; padding:12px 16px 0; }
.workspace-tabs button { display:inline-flex; align-items:center; gap:6px; padding:8px 18px; background:#0f172a; border:1px solid #334155; border-radius:7px; color:#94a3b8; cursor:pointer; }
.workspace-tabs button.selected { color:#5eead4; border-color:#0d9488; background:#102d30; }
.view-switch { display:flex; gap:8px; margin-bottom:18px; }
.view-switch button { background:#0f172a; color:#cbd5e1; border:1px solid #334155; border-radius:8px; padding:8px 16px; cursor:pointer; }
.view-switch button[aria-pressed=true] { background:#134e4a; border-color:#2dd4bf; color:#ccfbf1; }
.node-overview { display:flex; flex-wrap:wrap; gap:10px; margin-bottom:16px; }
.node-card { display:flex; flex-direction:column; align-items:flex-start; gap:5px; background:#0f172a; color:#cbd5e1; border:1px solid #334155; border-radius:8px; padding:12px; cursor:pointer; }
.node-card.stale, .node-card.offline { border-color:#f59e0b; }
.node-card:focus-visible { outline: 2px solid #2dd4bf; }
.node-del { background:#7f1d1d; color:#fecaca; border:1px solid #b91c1c; border-radius:6px; padding:4px 10px; font-size:12px; cursor:pointer; }
.node-del:hover { background:#991b1b; }
header { display: flex; flex-wrap: wrap; align-items: center; gap: 12px 24px; padding: 10px 16px; background: #0b1120; border-bottom: 1px solid #1e293b; position: sticky; top: 0; z-index: 10; }
.brand { font-weight: 700; font-size: 16px; display: flex; align-items: center; gap: 6px; }
.logo { color: #22c55e; }
.host { color: #94a3b8; font-weight: 400; font-size: 13px; margin-left: 10px; }
.meta { display: flex; flex-wrap: wrap; gap: 14px; color: #94a3b8; font-size: 12px; flex: 1; }
.dot.on { color: #22c55e; } .dot.off { color: #ef4444; }
.node-select { position: relative; }
.node-select-btn { background: #0f172a; color: #e2e8f0; border: 1px solid #334155; border-radius: 6px; padding: 4px 8px; font-size: 13px; cursor: pointer; max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.node-menu { position: absolute; top: calc(100% + 4px); left: 0; z-index: 50; background: #0f172a; border: 1px solid #334155; border-radius: 6px; box-shadow: 0 8px 24px rgba(0,0,0,.5); max-height: 340px; overflow-y: auto; min-width: 220px; }
.node-item { display: block; width: 100%; text-align: left; background: none; border: 0; padding: 5px 10px; font-size: 13px; color: #cbd5e1; cursor: pointer; white-space: nowrap; }
.node-item:hover { background: #1e293b; }
.node-item.sel { background: #1e293b; color: #e2e8f0; }
.nodes-count { color: #cbd5e1; }
.node-notice { color: #fcd34d; font-size: 13px; overflow-wrap: anywhere; }
.node-status { font-style: normal; font-size: 11px; margin-left: 6px; padding: 0 6px; border-radius: 8px; background: #1e293b; }
.node-status.live { color: #22c55e; } .node-status.stale { color: #fbbf24; } .node-status.offline { color: #ef4444; }
.alarms-btn { background: #1e293b; color: #94a3b8; border: 1px solid #334155; border-radius: 12px; padding: 1px 8px; font-size: 12px; cursor: pointer; }
.alarms-btn.warn { background: #78350f; color: #fde68a; border-color: #b45309; }
.alarms-btn.crit { background: #7f1d1d; color: #fecaca; border-color: #b91c1c; }
.alarms-btn.open { outline: 1px solid #94a3b8; }
.controls { display: flex; gap: 8px; margin-left: auto; }
input, select { background: #0f172a; color: #e2e8f0; border: 1px solid #334155; border-radius: 6px; padding: 4px 8px; font-size: 13px; }
.layout { display: flex; }
.chart-nav { width: 180px; flex: none; position: sticky; top: 49px; height: calc(100vh - 49px); overflow: auto; padding: 12px 8px; border-right: 1px solid #1e293b; }
.chart-nav a { display: flex; justify-content: space-between; padding: 6px 10px; border-radius: 6px; color: #cbd5e1; text-decoration: none; font-size: 13px; }
.chart-nav a:hover, .chart-nav a.active { background: #1e293b; }
.chart-nav small, .col small { color: #64748b; }
.collectors { margin-top: 20px; }
.nav-title { font-size: 11px; text-transform: uppercase; color: #64748b; padding: 0 10px 6px; }
.col { display: flex; justify-content: space-between; padding: 3px 10px; font-size: 12px; color: #94a3b8; }
.col.bad { color: #f87171; }
main { flex: 1; padding: 12px 16px; min-width: 0; }
h2 { font-size: 13px; text-transform: uppercase; letter-spacing: .06em; color: #64748b; margin: 18px 0 8px; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(420px, 100%), 1fr)); gap: 12px; }
.banner { background: #7f1d1d; color: #fecaca; padding: 8px 12px; border-radius: 6px; font-size: 13px; }
.empty { color: #64748b; }
.token { max-width: 420px; margin: 40px auto; padding: 20px; background: #0f172a; border: 1px solid #334155; border-radius: 8px; display: flex; flex-direction: column; gap: 10px; }
.token p { margin: 0; color: #cbd5e1; font-size: 13px; }
.token button { background: #22c55e; color: #052e16; border: 0; border-radius: 6px; padding: 6px 12px; font-weight: 600; cursor: pointer; }
.oidc { color: #93c5fd; font-size: 13px; text-align: center; }
.mobile-nav, .mobile-groups { display:none; }
.mobile-page-title { font-size:22px; margin:14px 0; }.mobile-page-title small { display:block; font-size:12px; font-weight:400; color:#94a3b8; overflow-wrap:anywhere; margin-top:5px; }
.logout { background:#172337; color:#cbd5e1; border:1px solid #334155; border-radius:8px; padding:7px 10px; cursor:pointer; }
.token h1 { font-size:22px; margin:0 0 4px; }
@media (max-width: 760px) {
  .chart-nav, .workspace-tabs, .desktop-detail { display: none; }
  header { position:static; gap:8px; padding:max(10px,env(safe-area-inset-top)) max(12px,env(safe-area-inset-right)) 10px max(12px,env(safe-area-inset-left)); }
  .brand { flex:1; min-width:0; display:block; font-size:17px; order:0; }
  .host { display:block; margin:2px 0 0; max-width:100%; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font-size:11px; }
  .logout { order:1; font-size:12px; min-height:44px; }
  .meta { order:2; flex-basis:100%; align-items:center; flex-wrap:wrap; gap:6px 10px; min-width:0; }
  .meta .dot { margin-left:auto; }
  .node-select { max-width:calc(100% - 60px); min-width:0; }
  .node-select-btn { width:100%; max-width:100%; min-height:44px; }
  .node-menu { width:min(290px,calc(100vw - 32px)); min-width:0; max-height:min(340px,55dvh); }
  .node-item { white-space:normal; overflow-wrap:anywhere; min-height:44px; }
  .nodes-count { display:none; }
  .alarms-btn { min-height:44px; min-width:44px; }
  .controls { order:3; width:100%; min-width:0; display:grid; grid-template-columns:minmax(0,1fr) 112px; }
  .controls input, .controls select { width:100%; min-width:0; min-height:44px; }
  .layout { padding-bottom:calc(76px + env(safe-area-inset-bottom)); }
  main { padding:10px max(12px,env(safe-area-inset-right)) 16px max(12px,env(safe-area-inset-left)); }
  .grid { gap:12px; }
  .node-overview { flex-wrap:nowrap; overflow-x:auto; padding-bottom:6px; }
  .node-card { min-width:220px; max-width:280px; flex-shrink:0; overflow-wrap:anywhere; }
  .view-switch button { flex:1; padding:8px; min-height:44px; }
  .mobile-groups { display:flex; align-items:center; gap:12px; color:#94a3b8; font-size:12px; margin-bottom:12px; }
  .mobile-groups select { flex:1; min-width:0; min-height:44px; }
  .token { margin:24px auto; padding:20px 16px; }.token input,.token button,.oidc { min-height:44px; }.token input { width:100%; min-width:0; }
  .mobile-nav { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); position:fixed; inset:auto 0 0; width:100%; height:auto; padding:6px max(8px,env(safe-area-inset-right)) max(6px,env(safe-area-inset-bottom)) max(8px,env(safe-area-inset-left)); background:#0b1526f5; border-top:1px solid #2b3e53; z-index:20; }
  .mobile-nav button { display:flex; flex-direction:column; align-items:center; justify-content:center; gap:3px; min-height:52px; background:none; border:0; border-radius:10px; color:#94a3b8; font:inherit; font-size:11px; cursor:pointer; }
  .mobile-nav button[aria-current=page] { color:#5eead4; background:#123333; }
  .mobile-nav svg { width:21px; height:21px; stroke:currentColor; fill:none; stroke-width:1.7; stroke-linejoin:round; stroke-linecap:round; }
  .mobile-nav b { display:inline-block; min-width:16px; padding:0 4px; border-radius:10px; background:#7f1d1d; color:#fecaca; font-size:10px; }
  .mobile-nav button:focus-visible { outline:2px solid #5eead4; outline-offset:-2px; }
}
</style>
