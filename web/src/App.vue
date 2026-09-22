<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { Alarm, AlarmLogEntry, Chart, FunctionInfo, Info, NodeInfo } from './api'
import { ApiError, api, auth, selection } from './api'
import { live } from './live'
import MetricChart from './components/MetricChart.vue'
import AlarmsPanel from './components/AlarmsPanel.vue'
import FunctionsPanel from './components/FunctionsPanel.vue'
import LogsPanel from './components/LogsPanel.vue'
import WeightsPanel from './components/WeightsPanel.vue'
import HubPanel from './components/HubPanel.vue'
import ContextsPanel from './components/ContextsPanel.vue'

const info = ref<Info | null>(null)
const charts = ref<Chart[]>([])
const error = ref('')
const needToken = ref(false)
const tokenInput = ref('')
const connected = ref(false)
const windowSec = ref(300)
const filter = ref('')
const activeSection = ref('')
const alarms = ref<Alarm[]>([])
const alarmLog = ref<AlarmLogEntry[]>([])
const showAlarms = ref(false)
const functions = ref<FunctionInfo[]>([])
const showFunctions = ref(false)
const showLogs = ref(false)
const showWeights = ref(false)
const showHub = ref(false)
const showContexts = ref(false)
const oidcAvailable = ref(false)
const nodes = ref<NodeInfo[]>([])
const selectedNode = ref('')
const NODE_KEY = 'monitor.node'
const isHub = computed(() => info.value?.mode === 'hub')
const currentNode = computed(() => nodes.value.find((n) => n.id === selectedNode.value) ?? null)
/** Remote nodes have no local health engine; their alarms are mirrored from the agent. */
const healthOn = computed(() => (selectedNode.value ? true : info.value?.alarms != null))
const raised = computed(() => ({
  warning: alarms.value.filter((a) => a.status === 'WARNING').length,
  critical: alarms.value.filter((a) => a.status === 'CRITICAL').length,
}))

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

async function refreshNodes() {
  if (!isHub.value) return
  try {
    nodes.value = (await api.nodes()).nodes
    if (selectedNode.value && !nodes.value.some((n) => n.id === selectedNode.value)) await selectNode('')
  } catch { /* transient */ }
}

/** Switch the whole dashboard (charts, alarms, functions, live socket) to another node. */
async function selectNode(id: string) {
  if (id === selectedNode.value) return
  selectedNode.value = id
  selection.node = id
  if (id) sessionStorage.setItem(NODE_KEY, id)
  else sessionStorage.removeItem(NODE_KEY)
  charts.value = []
  alarms.value = []
  alarmLog.value = []
  functions.value = []
  showFunctions.value = false
  await refresh()
  if (!needToken.value) live.restart()
}

async function refresh() {
  const node = selectedNode.value
  try {
    const [i, c] = await Promise.all([api.info(), api.charts()])
    info.value = i
    await refreshNodes()
    if (selectedNode.value !== node) return // switched while in flight; a newer refresh owns the state
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
    await Promise.all([refreshAlarms(), refreshFunctions()])
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) {
      needToken.value = true
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

async function refreshAlarms() {
  if (!healthOn.value) return
  const node = selectedNode.value
  try {
    const [a, l] = await Promise.all([api.alarms(), api.alarmLog()])
    if (selectedNode.value !== node) return
    alarms.value = Object.values(a.alarms)
    alarmLog.value = l
  } catch { /* transient; the next refresh retries */ }
}

async function refreshFunctions() {
  const node = selectedNode.value
  try {
    const f = await api.functions()
    if (selectedNode.value !== node) return
    functions.value = f
  } catch { /* transient */ }
}

/** Apply a live transition without waiting for the next poll. */
function onAlarmEvent(e: AlarmLogEntry) {
  if (!alarmLog.value.some((x) => x.unique_id === e.unique_id)) alarmLog.value = [...alarmLog.value, e].slice(-1000)
  const a = alarms.value.find((x) => x.id === e.alarm_id || (x.chart === e.chart && x.name === e.name))
  if (a) {
    a.status = e.status; a.value = e.value; a.last_updated = e.when; a.last_status_change = e.when
  } else {
    void refreshAlarms()
  }
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
  auth.token = tokenInput.value.trim()
  tokenInput.value = ''
  await refresh()
  if (!needToken.value) live.restart()
}

function fmtUptime(s: number) {
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60)
  return d ? `${d}d ${h}h` : h ? `${h}h ${m}m` : `${m}m ${s % 60}s`
}

let timer = 0
onMounted(async () => {
  auth.fromURL()
  const saved = new URL(location.href).searchParams.get('node') ?? sessionStorage.getItem(NODE_KEY) ?? ''
  selectedNode.value = saved
  selection.node = saved
  live.onState = (up) => (connected.value = up)
  live.onAlarm = onAlarmEvent
  fetch('/api/v1/auth/oidc/status').then((r) => r.json()).then((s) => { oidcAvailable.value = s.enabled === true }).catch(() => {})
  await refresh()
  if (!needToken.value) live.start()
  timer = window.setInterval(refresh, 30000)
})
onBeforeUnmount(() => { clearInterval(timer); live.stop() })
</script>

<template>
  <header>
    <div class="brand">
      <span class="logo">◉</span> Monitor
      <span v-if="info && !currentNode" class="host">{{ info.host.hostname }} · {{ info.host.os }}/{{ info.host.arch }}</span>
      <span v-else-if="currentNode" class="host">{{ currentNode.hostname }} · {{ currentNode.os }}/{{ currentNode.arch }}
        <i :class="['node-status', currentNode.status]">{{ currentNode.status }}</i></span>
    </div>
    <div class="meta" v-if="info">
      <select v-if="isHub" class="node-select" :value="selectedNode" @change="selectNode(($event.target as HTMLSelectElement).value)"
        title="节点">
        <option v-for="n in nodes" :key="n.id" :value="n.id">
          {{ n.local ? '◆ ' : n.status === 'live' ? '● ' : n.status === 'stale' ? '◐ ' : '○ ' }}{{ n.hostname }}{{ n.local ? ' (hub)' : n.replica ? ' (replica)' : n.peer ? ' (peer)' : '' }}
        </option>
      </select>
      <span v-if="isHub" class="nodes-count" title="在线节点 / 全部节点">{{ nodes.filter((n) => n.status === 'live').length }}/{{ nodes.length }} nodes</span>
      <span v-if="info.stream" :class="['dot', info.stream.connected ? 'on' : 'off']"
        :title="info.stream.connected ? '已上报到 ' + info.stream.destination : ('未连接 Hub' + (info.stream.last_error ? ': ' + info.stream.last_error : ''))">⇡</span>
      <span>{{ currentNode ? currentNode.charts_count : info.charts_count }} charts</span>
      <span v-if="!currentNode">{{ info.metrics_count }} metrics</span>
      <span v-if="!currentNode">up {{ fmtUptime(info.uptime) }}</span>
      <button v-if="healthOn" class="alarms-btn" :class="{ crit: raised.critical, warn: !raised.critical && raised.warning, open: showAlarms }"
        @click="showAlarms = !showAlarms" title="告警">
        ⚠ <b v-if="raised.critical">{{ raised.critical }}</b><b v-else-if="raised.warning">{{ raised.warning }}</b><span v-else>0</span>
      </button>
      <button v-if="functions.length" class="alarms-btn" :class="{ open: showFunctions }" @click="showFunctions = !showFunctions"
        title="Functions（实时进程表等）">ƒ {{ functions.length }}</button>
      <button class="alarms-btn" :class="{ open: showLogs }" @click="showLogs = !showLogs" title="日志">☰</button>
      <button v-if="isHub" class="alarms-btn" :class="{ open: showHub }" @click="showHub = !showHub" title="Hub：Space / Room / claim">Hub</button>
      <button class="alarms-btn" :class="{ open: showWeights }" @click="showWeights = !showWeights" title="异常顾问 / 关联分析">Σ</button>
      <button class="alarms-btn" :class="{ open: showContexts }" @click="showContexts = !showContexts" title="Context 总览">Ctx</button>
      <span :class="['dot', connected ? 'on' : 'off']" :title="connected ? 'live' : 'reconnecting'">●</span>
    </div>
    <div class="controls">
      <input v-model="filter" placeholder="筛选图表…" />
      <select v-model.number="windowSec">
        <option v-for="w in windows" :key="w.v" :value="w.v">{{ w.label }}</option>
      </select>
    </div>
    <button v-if="info && auth.token" @click="logout">退出登录</button>
  </header>

  <div class="layout">
    <nav>
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

    <main>
      <div v-if="isHub" class="node-overview" aria-label="节点健康总览">
        <button v-for="n in nodes" :key="n.id" @click="selectNode(n.id)" :class="['node-card', n.status]">
          <b>{{ n.hostname }}</b><span>{{ n.status === 'live' ? '在线' : n.status === 'stale' ? '数据过期' : '离线' }}</span>
          <small>{{ n.charts_count }} 图表 · {{ n.alarms?.critical || 0 }} 严重告警{{ n.replica ? ' · 副本' : '' }}</small>
          <small v-if="n.last_data">最后数据：{{ new Date(n.last_data * 1000).toLocaleString() }}</small>
        </button>
      </div>
      <div v-if="info?.db?.persistence?.error" class="banner">数据保存失败：{{ info.db.persistence.error }}</div>
      <div v-if="error" class="banner">{{ error }}</div>
      <AlarmsPanel v-if="showAlarms && healthOn" :alarms="alarms" :log="alarmLog" @close="showAlarms = false" />
      <FunctionsPanel v-if="showFunctions && functions.length" :functions="functions" @close="showFunctions = false" />
      <LogsPanel v-if="showLogs" @close="showLogs = false" />
      <WeightsPanel v-if="showWeights" @close="showWeights = false" @pick="(id) => { filter = id; showWeights = false }" />
      <ContextsPanel v-if="showContexts" @close="showContexts = false" @pick="(id) => { filter = id; showContexts = false }" />
      <HubPanel v-if="showHub && isHub" @close="showHub = false" />
      <form v-if="needToken" class="token" @submit.prevent="submitToken">
        <p>此服务需要身份验证，请输入访问令牌或使用 OIDC 登录。</p>
        <input v-model="tokenInput" type="password" placeholder="token" autocomplete="off" autofocus />
        <button type="submit">进入</button>
        <a v-if="oidcAvailable" class="oidc" :href="api.oidcLoginURL()">使用 OIDC 登录</a>
      </form>
      <section v-for="s in sections" :key="s.name" :id="s.name">
        <h2>{{ s.name }}</h2>
        <div class="grid">
          <MetricChart v-for="c in s.charts" :key="c.id" :chart="c" :window="windowSec" />
        </div>
      </section>
      <p v-if="!charts.length && !error && !needToken" class="empty">
        {{ currentNode && currentNode.status === 'offline' ? '节点离线，暂无数据。' : '等待数据…' }}
      </p>
    </main>
  </div>
</template>

<style scoped>
.node-overview { display:flex; flex-wrap:wrap; gap:10px; margin-bottom:16px; }
.node-card { display:flex; flex-direction:column; align-items:flex-start; gap:5px; background:#0f172a; color:#cbd5e1; border:1px solid #334155; border-radius:8px; padding:12px; cursor:pointer; }
.node-card.stale, .node-card.offline { border-color:#f59e0b; }
header { display: flex; flex-wrap: wrap; align-items: center; gap: 12px 24px; padding: 10px 16px; background: #0b1120; border-bottom: 1px solid #1e293b; position: sticky; top: 0; z-index: 10; }
.brand { font-weight: 700; font-size: 16px; display: flex; align-items: center; gap: 6px; }
.logo { color: #22c55e; }
.host { color: #94a3b8; font-weight: 400; font-size: 13px; margin-left: 10px; }
.meta { display: flex; gap: 14px; color: #94a3b8; font-size: 12px; flex: 1; }
.dot.on { color: #22c55e; } .dot.off { color: #ef4444; }
.node-select { max-width: 220px; }
.nodes-count { color: #cbd5e1; }
.node-status { font-style: normal; font-size: 11px; margin-left: 6px; padding: 0 6px; border-radius: 8px; background: #1e293b; }
.node-status.live { color: #22c55e; } .node-status.stale { color: #fbbf24; } .node-status.offline { color: #ef4444; }
.alarms-btn { background: #1e293b; color: #94a3b8; border: 1px solid #334155; border-radius: 12px; padding: 1px 8px; font-size: 12px; cursor: pointer; }
.alarms-btn.warn { background: #78350f; color: #fde68a; border-color: #b45309; }
.alarms-btn.crit { background: #7f1d1d; color: #fecaca; border-color: #b91c1c; }
.alarms-btn.open { outline: 1px solid #94a3b8; }
.controls { display: flex; gap: 8px; margin-left: auto; }
input, select { background: #0f172a; color: #e2e8f0; border: 1px solid #334155; border-radius: 6px; padding: 4px 8px; font-size: 13px; }
.layout { display: flex; }
nav { width: 180px; flex: none; position: sticky; top: 49px; height: calc(100vh - 49px); overflow: auto; padding: 12px 8px; border-right: 1px solid #1e293b; }
nav a { display: flex; justify-content: space-between; padding: 6px 10px; border-radius: 6px; color: #cbd5e1; text-decoration: none; font-size: 13px; }
nav a:hover, nav a.active { background: #1e293b; }
nav small, .col small { color: #64748b; }
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
@media (max-width: 760px) {
  nav { display: none; }
  header { position: static; }
  .meta { flex-basis: 100%; }
  main { padding: 8px; }
}
</style>
