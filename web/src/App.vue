<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { auth } from './api/auth'
import { configurationApi } from './api/configuration/client'
import type { Problem } from './api/operations/types'
import type { AIProblemSelection } from './ai'
import { useMonitorSession } from './features/monitoring/useMonitorSession'
import { modules, resolveWorkspace, type WorkspaceId } from './features/workspace/modules'
import WorkspaceHeader from './features/workspace/WorkspaceHeader.vue'
import WorkspaceModuleHost from './features/workspace/WorkspaceModuleHost.vue'
import type { WorkspaceModuleHandle } from './features/workspace/contracts'
import DashboardTools from './components/DashboardTools.vue'
import AppIcon from './components/AppIcon.vue'

const workspace = ref<WorkspaceId>(resolveWorkspace(new URL(location.href).searchParams.get('view')))
const returnWorkspace = ref<WorkspaceId>('operations')
const alarmHistoryVisible = computed(() => workspace.value === 'alarms')
const aiProblem = shallowRef<AIProblemSelection | null>(null)
const toolsMenu = ref<InstanceType<typeof DashboardTools> | null>(null)
const toolsOpen = ref(false)
const moduleView = shallowRef<WorkspaceModuleHandle | null>(null)
const pendingChart = ref<string | null>(null)
// An awaited node refresh must not take over a workspace chosen in the meantime.
let navigationGeneration = 0
const HISTORY_KEY = 'monitor.workspace'
type HistoryEntry = { session: string; index: number }
function readHistoryEntry(state: unknown): HistoryEntry | null {
  const entry = state && typeof state === 'object' ? (state as Record<string, unknown>)[HISTORY_KEY] : null
  if (!entry || typeof entry !== 'object') return null
  const value = entry as Partial<HistoryEntry>
  return typeof value.session === 'string' && Number.isSafeInteger(value.index) && value.index! >= 0 ? value as HistoryEntry : null
}
function freshHistoryEntry(): HistoryEntry { return { session: `${Date.now()}-${Math.random().toString(36).slice(2)}`, index: 0 } }
let historyEntry = readHistoryEntry(history.state) ?? freshHistoryEntry()
let restoringHistory: HistoryEntry | null = null
let currentURL = location.href
function historyState() { return { ...(history.state && typeof history.state === 'object' ? history.state : {}), [HISTORY_KEY]: historyEntry } }
const media = window.matchMedia('(max-width: 760px)')
const isMobile = ref(media.matches)
const syncMobile = () => { isMobile.value = media.matches }
const {
  info, charts, error, needToken, tokenInput, loginError, connected, oidcAvailable,
  alarms, alarmLog, functions, nodes, selectedNode, nodeNotice, isHub, currentNode,
  healthOn, raised, logsAvailable, refresh, selectNode, forgetNode, refreshAlarms,
  submitToken: submitSessionToken, logout: logoutSession,
} = useMonitorSession({
  alarmHistoryVisible,
  onUnauthorized: () => { navigationGeneration++; toolsMenu.value?.close(); aiProblem.value = null; pendingChart.value = null },
  onNodeChange: () => { aiProblem.value = null },
})
const context = computed(() => ({ hub: isHub.value, admin: info.value?.user?.role === 'admin', personal: !!info.value?.user?.personal, authenticated: !!auth.token, functions: functions.value.length, logs: logsAvailable.value }))
const availableModules = computed(() => modules.filter(module => !module.available || module.available(context.value)))
const activeModule = computed(() => availableModules.value.find(module => module.id === workspace.value))
const primaryModules = computed(() => availableModules.value.filter(module => module.group === '监控'))
const tools = computed(() => [
  ...availableModules.value.filter(module => module.group !== '监控' || isMobile.value && module.id === 'services')
    .map(module => ({ ...module, active: workspace.value === module.id })),
  ...(isMobile.value && auth.token ? [{ id: 'logout', label: '退出登录', title: '退出登录', description: '结束当前浏览器登录', active: false, group: '账户' }] : []),
])

function writeURL(push = false) {
  const url = new URL(location.href)
  url.searchParams.set('view', workspace.value)
  url.searchParams.delete('token')
  if (push && url.href !== location.href) {
    historyEntry = { ...historyEntry, index: historyEntry.index + 1 }
    history.pushState(historyState(), '', url)
  } else history.replaceState(historyState(), '', url)
  currentURL = url.href
}
function canLeave() { return moduleView.value?.canLeave?.() !== false }
function focusWorkspace() {
  void nextTick(() => {
    const main = document.getElementById('workspace-main')
    main?.focus({ preventScroll: true })
    if (!isMobile.value) main?.scrollIntoView({ block: 'start' })
  })
}
function navigate(value: WorkspaceId, guarded = true, push = true) {
  navigationGeneration++
  if (restoringHistory) return false
  if (value === workspace.value) return true
  if (guarded && !canLeave()) return false
  if (['operations', 'charts', 'services', 'alarms'].includes(workspace.value)) returnWorkspace.value = workspace.value
  workspace.value = value
  writeURL(push)
  focusWorkspace()
  return true
}
function closeModule() { navigate(returnWorkspace.value === workspace.value ? 'operations' : returnWorkspace.value, false) }
function activateTool(id: string) {
  if (id === 'logout') { if (canLeave()) void logout(); return }
  const next = resolveWorkspace(id)
  if (next === workspace.value) {
    if (canLeave()) closeModule()
  } else if (navigate(next) && next === 'ai') aiProblem.value = null
}
function changeNode(id: string) { navigationGeneration++; if (restoringHistory || id === selectedNode.value || !canLeave()) return; void selectNode(id); currentURL = location.href }
function submitToken() { navigationGeneration++; return submitSessionToken() }
function logout() { navigationGeneration++; return logoutSession() }
async function drill(node: string, chart: string) {
  const navigation = ++navigationGeneration
  if (!canLeave()) return
  await selectNode(node)
  if (navigation !== navigationGeneration || needToken.value || selectedNode.value !== node) return
  pendingChart.value = chart
  navigate('charts', false)
}
async function solveProblem(problem: Problem) {
  const navigation = ++navigationGeneration
  if (!canLeave()) return
  const node = problem.hostname === info.value?.host.hostname && problem.node === info.value?.host.id ? '' : problem.node
  await selectNode(node)
  if (navigation !== navigationGeneration || needToken.value || selectedNode.value !== node) return
  aiProblem.value = { id: problem.id, name: problem.name, chart: problem.chart }
  navigate('ai', false)
}
watch([workspace, moduleView, pendingChart], () => {
  if (workspace.value !== 'charts') { pendingChart.value = null; return }
  if (workspace.value === 'charts' && moduleView.value?.showChart && pendingChart.value !== null) {
    moduleView.value.showChart(pendingChart.value)
    pendingChart.value = null
  }
}, { flush: 'post' })
const moduleProps = computed(() => {
  const role = info.value?.user?.role || 'viewer'
  switch (workspace.value) {
    case 'operations': return { role }
    case 'charts': return { charts: charts.value, node: selectedNode.value, info: info.value, currentNode: currentNode.value }
    case 'alarms': return { alarms: alarms.value, log: alarmLog.value, refresh: refreshAlarms, canManage: role === 'admin' && !selectedNode.value, canClose: ['admin', 'troubleshooter'].includes(role) && !selectedNode.value }
    case 'rules': return { role }
    case 'functions': return { functions: functions.value }
    case 'shell': return { node: selectedNode.value }
    case 'ai': return { node: selectedNode.value, charts: charts.value, problem: aiProblem.value, role }
    case 'templates': return { role, nodes: nodes.value }
    case 'nodes': return { nodes: nodes.value, canManage: role === 'admin' }
    case 'config': return { canManage: role === 'admin', isHub: isHub.value }
    default: return {}
  }
})
const moduleEvents = computed(() => ({
  close: closeModule,
  ...(workspace.value === 'operations' ? { drill, solve: solveProblem } : {}),
  ...(workspace.value === 'rules' ? { changed: refresh } : {}),
  ...(['weights', 'contexts'].includes(workspace.value) ? { pick: (id: string) => drill(selectedNode.value, id) } : {}),
  ...(['cloud', 'nodes'].includes(workspace.value) ? { pick: (id: string) => drill(nodes.value.find(node => node.id === id)?.local ? '' : id, '') } : {}),
  ...(workspace.value === 'nodes' ? { forget: forgetNode } : {}),
}))
// Node-scoped modules unmount to cancel their requests; global config/operations stay stable.
const moduleKey = computed(() => `${workspace.value}:${['alarms', 'logs', 'weights', 'contexts', 'functions'].includes(workspace.value) ? selectedNode.value : ''}`)
function popstate(event: PopStateEvent) {
  const entry = readHistoryEntry(event.state)
  if (restoringHistory && entry?.session === restoringHistory.session && entry.index === restoringHistory.index) {
    restoringHistory = null
    currentURL = location.href
    return
  }
  restoringHistory = null
  navigationGeneration++
  const next = resolveWorkspace(new URL(location.href).searchParams.get('view'))
  const node = new URL(location.href).searchParams.get('node') ?? ''
  if ((next !== workspace.value || node !== selectedNode.value) && !canLeave()) {
    if (entry?.session === historyEntry.session && entry.index !== historyEntry.index) {
      restoringHistory = historyEntry
      history.go(historyEntry.index - entry.index)
    } else {
      // An entry from another document has no reliable relative offset. Restore
      // this workspace as a new entry, preserving the unknown destination.
      const url = new URL(currentURL)
      url.searchParams.set('view', workspace.value)
      url.searchParams.delete('token')
      if (selectedNode.value) url.searchParams.set('node', selectedNode.value)
      else url.searchParams.delete('node')
      history.pushState(historyState(), '', url)
      currentURL = url.href
    }
    return
  }
  historyEntry = entry?.session === historyEntry.session ? entry : freshHistoryEntry()
  navigate(next, false, false)
  if (node !== selectedNode.value) void selectNode(node)
  writeURL()
}
onMounted(() => { history.replaceState(historyState(), '', location.href); currentURL = location.href; media.addEventListener('change', syncMobile); window.addEventListener('popstate', popstate) })
onBeforeUnmount(() => { media.removeEventListener('change', syncMobile); window.removeEventListener('popstate', popstate) })
</script>

<template>
  <WorkspaceHeader :info="info" :current-node="currentNode" :nodes="nodes" :node="selectedNode" :connected="connected" :warning="raised.warning" :critical="raised.critical" :authenticated="!!auth.token" :mobile="isMobile" @node="changeNode" @alarms="navigate('alarms')" @logout="() => { if (canLeave()) void logout() }" />
  <div class="app-layout" :class="{ authenticated: info && !needToken }">
    <nav v-if="info && !needToken && !isMobile" class="workspace-nav" aria-label="功能模块">
      <p class="nav-group">监控</p>
      <div class="workspace-tabs">
        <button v-for="module in primaryModules" :key="module.id" :class="{ selected: workspace === module.id }" :aria-current="workspace === module.id ? 'page' : undefined" @click="navigate(module.id)"><AppIcon :name="module.id" :size="18" />{{ module.label }}</button>
      </div>
      <DashboardTools ref="toolsMenu" :mobile="false" :tools="tools" @select="activateTool" @visibility="value => toolsOpen = value" />
    </nav>
    <DashboardTools v-if="info && !needToken && isMobile" ref="toolsMenu" :mobile="true" :tools="tools" @select="activateTool" @visibility="value => toolsOpen = value" />
    <main id="workspace-main" tabindex="-1">
      <div v-if="info?.db?.persistence?.error" class="banner" role="alert">数据保存失败：{{ info.db.persistence.error }}</div>
      <div v-if="error" class="banner" role="alert">{{ error }}</div>
      <p v-if="nodeNotice" class="node-notice" role="status">{{ nodeNotice }}</p>
      <form v-if="needToken" class="token" @submit.prevent="submitToken">
        <h1>登录 Monitor</h1><p>请输入登录密码或访问令牌。</p><p>首次部署的密码保存在服务器数据目录的 web-password 文件中，请联系管理员获取。</p>
        <input v-model="tokenInput" type="password" placeholder="登录密码或访问令牌" aria-label="登录密码或访问令牌" autocomplete="current-password" required autofocus />
        <p v-if="loginError" role="alert">{{ loginError }}</p><button type="submit">进入</button>
        <a v-if="oidcAvailable" class="oidc" :href="configurationApi.oidcLoginURL()">使用 OIDC 登录</a>
      </form>
      <template v-else-if="info">
        <div class="workspace-context"><span>{{ activeModule?.group || '工作区' }} / {{ activeModule?.label || workspace }}</span><span>{{ workspace === 'operations' || workspace === 'services' ? '全局视图' : currentNode?.hostname || info.host.hostname }}</span></div>
        <p v-if="!activeModule" class="empty">当前节点或账号暂不可使用此模块。<button @click="navigate('operations', false)">返回运维总览</button></p>
        <p v-else-if="workspace === 'alarms' && !healthOn" class="empty">当前节点未启用告警。</p>
        <WorkspaceModuleHost v-else :component="activeModule.component" :key="moduleKey" ref="moduleView" v-bind="moduleProps" v-on="moduleEvents" />
      </template>
      <p v-else-if="!error" class="empty" role="status">正在连接监控服务…</p>
    </main>
  </div>
  <Teleport to="body">
  <nav v-if="info && !needToken" class="mobile-nav" aria-label="手机导航">
    <button :aria-current="workspace === 'operations' && !toolsOpen ? 'page' : undefined" @click="navigate('operations')"><AppIcon name="operations" :size="21" /><span>总览</span></button>
    <button :aria-current="workspace === 'charts' && !toolsOpen ? 'page' : undefined" @click="navigate('charts')"><AppIcon name="charts" :size="21" /><span>图表</span></button>
    <button :aria-current="workspace === 'alarms' && !toolsOpen ? 'page' : undefined" @click="navigate('alarms')"><AppIcon name="alarms" :size="21" /><span>告警 <b v-if="raised.critical + raised.warning">{{ raised.critical + raised.warning }}</b></span></button>
    <button :aria-expanded="toolsOpen" :aria-current="toolsOpen ? 'page' : undefined" aria-haspopup="dialog" @click="toolsMenu?.open($event.currentTarget)"><AppIcon name="more" :size="21" /><span>更多</span></button>
  </nav>
  </Teleport>
</template>

<style scoped>
.app-layout { display:grid; grid-template-columns:minmax(0,1fr); }.app-layout.authenticated { grid-template-columns:204px minmax(0,1fr); }
.workspace-nav { position:sticky; top:64px; height:calc(100dvh - 64px); overflow:auto; padding:18px 12px; border-right:1px solid #1e293b; background:#0b1120; }.nav-group { color:#64748b; font-size:11px; margin:0 10px 8px; letter-spacing:.08em; }
.workspace-tabs { display:grid; gap:3px; margin-bottom:24px; }.workspace-tabs button { display:flex; align-items:center; gap:10px; padding:10px; background:transparent; color:#94a3b8; border:1px solid transparent; border-radius:8px; text-align:left; font:inherit; font-size:13px; cursor:pointer; }.workspace-tabs button:hover { background:#172337; }.workspace-tabs button.selected { color:#5eead4; border-color:#1e5451; background:#123333; }
main { min-width:0; padding:22px 28px 40px; scroll-margin-top:80px; outline:none; }.workspace-context { display:flex; justify-content:space-between; gap:12px; font-size:11px; color:#64748b; margin-bottom:20px; overflow-wrap:anywhere; }
.banner { background:#3a1e2a; border:1px solid #7f1d1d; color:#fecaca; padding:12px; border-radius:8px; margin-bottom:12px; font-size:13px; overflow-wrap:anywhere; }.node-notice { color:#fcd34d; }.empty { color:#94a3b8; padding:24px; }
.token { max-width:420px; margin:40px auto; padding:24px; background:#0f172a; border:1px solid #334155; border-radius:12px; display:flex; flex-direction:column; gap:12px; }.token h1 { font-size:24px; margin:0; }.token p { margin:0; color:#94a3b8; font-size:13px; }.token input { background:#0b1120; border:1px solid #475569; border-radius:7px; padding:12px; color:#e2e8f0; font:inherit; }.token button { background:#2dd4bf; color:#042f2e; padding:10px; font:inherit; font-weight:600; border:0; border-radius:7px; cursor:pointer; }.oidc { color:#93c5fd; text-align:center; }
.mobile-nav { display:none; }button:focus-visible { outline:2px solid #5eead4; outline-offset:2px; }
@media(max-width:760px) { .app-layout.authenticated { grid-template-columns:minmax(0,1fr); }.app-layout { position:relative; z-index:1; padding-bottom:calc(78px + env(safe-area-inset-bottom)); }main { padding:14px max(12px,env(safe-area-inset-right)) 20px max(12px,env(safe-area-inset-left)); scroll-margin-top:12px; position:relative; z-index:0; }.workspace-context { margin-bottom:16px; }.token { margin:12px auto; padding:20px 16px; }.token input { min-width:0; width:100%; }.mobile-nav { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); position:fixed; inset:auto 0 0; padding:6px max(8px,env(safe-area-inset-right)) max(6px,env(safe-area-inset-bottom)) max(8px,env(safe-area-inset-left)); background:#0b1526f2; border-top:1px solid #2b3e53; z-index:40; pointer-events:auto; }.mobile-nav button { display:flex; flex-direction:column; align-items:center; justify-content:center; gap:3px; min-height:52px; background:none; border:0; border-radius:10px; color:#94a3b8; font:inherit; font-size:11px; cursor:pointer; pointer-events:auto; }.mobile-nav button[aria-current=page] { color:#5eead4; background:#123333; }.mobile-nav b { padding:0 4px; border-radius:10px; background:#7f1d1d; color:#fecaca; font-size:10px; } }
</style>
