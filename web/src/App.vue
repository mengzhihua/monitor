<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { Alarm, AlarmLogEntry, Chart, Info } from './api'
import { ApiError, api, auth } from './api'
import { live } from './live'
import MetricChart from './components/MetricChart.vue'
import AlarmsPanel from './components/AlarmsPanel.vue'

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
const healthOn = computed(() => info.value?.alarms != null)
const raised = computed(() => ({
  warning: alarms.value.filter((a) => a.status === 'WARNING').length,
  critical: alarms.value.filter((a) => a.status === 'CRITICAL').length,
}))

const windows = [
  { label: '1 分钟', v: 60 },
  { label: '5 分钟', v: 300 },
  { label: '15 分钟', v: 900 },
  { label: '1 小时', v: 3600 },
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

async function refresh() {
  try {
    const [i, c] = await Promise.all([api.info(), api.charts()])
    info.value = i
    const list = Object.values(c.charts)
    // keep object identity stable so chart components don't remount
    const byId = new Map(charts.value.map((x) => [x.id, x]))
    charts.value = list.map((x) => {
      const old = byId.get(x.id)
      if (old) { old.dimensions = x.dimensions; return old }
      return x
    })
    error.value = ''
    needToken.value = false
    await refreshAlarms()
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) {
      needToken.value = true
      error.value = ''
      return
    }
    error.value = String(e)
  }
}

async function refreshAlarms() {
  if (!healthOn.value) return
  try {
    const [a, l] = await Promise.all([api.alarms(), api.alarmLog()])
    alarms.value = Object.values(a.alarms)
    alarmLog.value = l
  } catch { /* transient; the next refresh retries */ }
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
  live.onState = (up) => (connected.value = up)
  live.onAlarm = onAlarmEvent
  await refresh()
  if (!needToken.value) live.start()
  timer = window.setInterval(refresh, 30000)
})
onBeforeUnmount(() => clearInterval(timer))
</script>

<template>
  <header>
    <div class="brand">
      <span class="logo">◉</span> Monitor
      <span v-if="info" class="host">{{ info.host.hostname }} · {{ info.host.os }}/{{ info.host.arch }}</span>
    </div>
    <div class="meta" v-if="info">
      <span>{{ info.charts_count }} charts</span>
      <span>{{ info.metrics_count }} metrics</span>
      <span>up {{ fmtUptime(info.uptime) }}</span>
      <button v-if="healthOn" class="alarms-btn" :class="{ crit: raised.critical, warn: !raised.critical && raised.warning, open: showAlarms }"
        @click="showAlarms = !showAlarms" title="告警">
        ⚠ <b v-if="raised.critical">{{ raised.critical }}</b><b v-else-if="raised.warning">{{ raised.warning }}</b><span v-else>0</span>
      </button>
      <span :class="['dot', connected ? 'on' : 'off']" :title="connected ? 'live' : 'reconnecting'">●</span>
    </div>
    <div class="controls">
      <input v-model="filter" placeholder="筛选图表…" />
      <select v-model.number="windowSec">
        <option v-for="w in windows" :key="w.v" :value="w.v">{{ w.label }}</option>
      </select>
    </div>
  </header>

  <div class="layout">
    <nav>
      <a v-for="s in sections" :key="s.name" :href="'#' + s.name" :class="{ active: activeSection === s.name }"
        @click="activeSection = s.name">{{ s.name }} <small>{{ s.charts.length }}</small></a>
      <div class="collectors" v-if="info">
        <div class="nav-title">采集器</div>
        <div v-for="c in info.collectors" :key="c.name" class="col" :class="{ bad: !c.enabled || c.error }">
          <span>{{ c.name }}</span>
          <small>{{ c.enabled ? (c.error ? 'error' : c.last_run_ms + 'ms') : 'off' }}</small>
        </div>
      </div>
    </nav>

    <main>
      <div v-if="error" class="banner">{{ error }}</div>
      <AlarmsPanel v-if="showAlarms && healthOn" :alarms="alarms" :log="alarmLog" @close="showAlarms = false" />
      <form v-if="needToken" class="token" @submit.prevent="submitToken">
        <p>此 Agent 已启用访问令牌（web.token），请输入后继续。</p>
        <input v-model="tokenInput" type="password" placeholder="token" autocomplete="off" autofocus />
        <button type="submit">进入</button>
      </form>
      <section v-for="s in sections" :key="s.name" :id="s.name">
        <h2>{{ s.name }}</h2>
        <div class="grid">
          <MetricChart v-for="c in s.charts" :key="c.id" :chart="c" :window="windowSec" />
        </div>
      </section>
      <p v-if="!charts.length && !error && !needToken" class="empty">等待数据…</p>
    </main>
  </div>
</template>

<style scoped>
header { display: flex; flex-wrap: wrap; align-items: center; gap: 12px 24px; padding: 10px 16px; background: #0b1120; border-bottom: 1px solid #1e293b; position: sticky; top: 0; z-index: 10; }
.brand { font-weight: 700; font-size: 16px; display: flex; align-items: center; gap: 6px; }
.logo { color: #22c55e; }
.host { color: #94a3b8; font-weight: 400; font-size: 13px; margin-left: 10px; }
.meta { display: flex; gap: 14px; color: #94a3b8; font-size: 12px; flex: 1; }
.dot.on { color: #22c55e; } .dot.off { color: #ef4444; }
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
@media (max-width: 760px) {
  nav { display: none; }
  header { position: static; }
  .meta { flex-basis: 100%; }
  main { padding: 8px; }
}
</style>
