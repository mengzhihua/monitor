<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import type { Alarm, AlarmLogEntry, SilenceState } from '../api'
import { api } from '../api'
import { usePolling } from '../polling'

const props = defineProps<{ alarms: Alarm[]; log: AlarmLogEntry[]; canManage: boolean }>()
const actionError = ref('')
const actionBusy = ref(false)
const emit = defineEmits<{ close: [] }>()
const silence = ref<SilenceState>({ all: false, alarms: {} })
const now = ref(Math.floor(Date.now() / 1000))
onMounted(() => { void refreshSilence() })
usePolling(async () => { now.value = Math.floor(Date.now() / 1000) }, 1000)

async function refreshSilence() {
  if (!props.canManage) return
  try { silence.value = await api.silenceState() } catch { /* token / role */ }
}

const order: Record<string, number> = { CRITICAL: 0, WARNING: 1, CLEAR: 2, UNDEFINED: 3, UNINITIALIZED: 4, REMOVED: 5 }
const sorted = computed(() =>
  [...props.alarms].sort((a, b) => (order[a.status] ?? 9) - (order[b.status] ?? 9) || a.chart.localeCompare(b.chart) || a.name.localeCompare(b.name)),
)
const recent = computed(() => [...props.log].sort((a, b) => b.unique_id - a.unique_id).slice(0, 50))
const allSilenced = computed(() => silence.value.all && (!silence.value.until || silence.value.until > now.value))

function remain(until?: number) {
  if (!until) return '持续'
  const s = until - now.value
  if (s <= 0) return '到期'
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
}
function alarmUntil(a: Alarm) {
  return silence.value.alarms[`${a.chart}.${a.name}`] ?? silence.value.alarms[a.name]
}
function manuallySilenced(a: Alarm) {
  const until = alarmUntil(a)
  return until !== undefined && (until === 0 || until > now.value)
}
async function reloadAlarmSilence(addressed: Alarm[]) {
  if (props.alarms !== addressed || !props.canManage) return
  const latest = await api.alarms()
  if (props.alarms !== addressed || !props.canManage) return
  const states = Object.values(latest.alarms)
  for (const a of addressed) a.silenced = states.find(n => n.chart === a.chart && n.name === a.name)?.silenced
}

async function silenceAll(on: boolean) {
  if (!props.canManage || actionBusy.value) return
  actionError.value = ''
  actionBusy.value = true
  let saved = false
  const addressedAlarms = props.alarms
  try {
    const result = await api.silence(on ? { all: true, until: -3600 } : { all: false, clear: true })
    saved = true
    if (props.alarms !== addressedAlarms || !props.canManage) return
    silence.value = result
    await reloadAlarmSilence(addressedAlarms)
  } catch (e) { actionError.value = saved ? '静默设置已保存，状态刷新失败，请重新打开告警面板核对。' : `静默操作失败：${String(e)}` }
  finally { actionBusy.value = false }
}

async function silenceOne(a: Alarm, on: boolean) {
  if (!props.canManage || actionBusy.value) return
  actionError.value = ''
  actionBusy.value = true
  let saved = false
  const addressed = props.alarms
  try {
    silence.value = await api.silence({ chart: a.chart, alarm: a.name, clear: !on, until: on ? -3600 : 0 })
    saved = true
    await reloadAlarmSilence(addressed)
  } catch (e) { actionError.value = saved ? '静默设置已保存，状态刷新失败，请重新打开告警面板核对。' : `静默操作失败：${String(e)}` }
  finally { actionBusy.value = false }
}

function fmt(v: number | null) {
  if (v === null || !Number.isFinite(v)) return '—'
  return Math.abs(v) >= 100 ? v.toFixed(0) : Math.abs(v) >= 10 ? v.toFixed(1) : v.toFixed(2)
}
function when(t: number) {
  return new Date(t * 1000).toLocaleString()
}
function ago(t: number) {
  const s = Math.max(0, Math.floor(Date.now() / 1000 - t))
  return s < 60 ? `${s}s` : s < 3600 ? `${Math.floor(s / 60)}m` : s < 86400 ? `${Math.floor(s / 3600)}h` : `${Math.floor(s / 86400)}d`
}
</script>

<template>
  <div class="panel">
    <div class="head">
      <h3>告警 <small>{{ alarms.length }} 条规则</small></h3>
      <div class="actions">
        <button v-if="canManage" class="mute" :disabled="actionBusy" @click="silenceAll(!allSilenced)" :title="allSilenced ? '只解除全局手动静默，单条静默与维护仍可能生效' : '静默全部通知'">
          {{ allSilenced ? '解除全局静默' : '全部静默' }}
        </button>
        <span v-if="silence.all" class="dim">{{ remain(silence.until) }}</span>
        <span v-if="silence.maintenance" class="dim">维护中</span>
        <button class="x" @click="emit('close')" title="关闭">×</button>
      </div>
    </div>
    <p v-if="actionError" role="alert">{{ actionError }}</p>
    <p v-if="!canManage" class="dim">通知静默仅允许管理员在本机执行；跨节点问题请在运维总览中确认和记录。</p>
    <p class="dim">这里仅修改临时手动静默。维护计划请到运维总览查看或取消，解除手动静默不会清除维护抑制。</p>
    <table>
      <thead>
        <tr><th>状态</th><th>告警</th><th>图表</th><th class="num">当前值</th><th>持续</th><th></th></tr>
      </thead>
      <tbody>
        <tr v-for="a in sorted" :key="a.chart + '.' + a.name" :class="a.status.toLowerCase()" :title="a.info">
          <td><span class="badge">{{ a.status }}</span></td>
          <td>{{ a.name }}</td>
          <td class="dim">{{ a.chart }}</td>
          <td class="num">{{ fmt(a.value) }} <span class="dim">{{ a.units }}</span></td>
          <td class="dim">{{ a.last_status_change ? ago(a.last_status_change) : '—' }}</td>
          <td><button v-if="canManage" class="mute tiny" :disabled="actionBusy" @click="silenceOne(a, !manuallySilenced(a))">{{ manuallySilenced(a) ? '解除手动静默' : '静默' }}</button>
            <span v-if="manuallySilenced(a) || allSilenced" class="dim"> {{ remain(manuallySilenced(a) ? alarmUntil(a) : silence.until) }}</span>
            <span v-else-if="a.silenced" class="dim">维护或其他抑制</span>
          </td>
        </tr>
        <tr v-if="!alarms.length"><td colspan="6" class="dim">暂无告警规则</td></tr>
      </tbody>
    </table>

    <h3>最近事件 <small>{{ log.length }}</small></h3>
    <ul class="log">
      <li v-for="e in recent" :key="e.unique_id" :class="e.status.toLowerCase()">
        <span class="dim">{{ when(e.when) }}</span>
        <span class="badge">{{ e.status }}</span>
        <span class="dim">← {{ e.old_status }}</span>
        <span>{{ e.name }}</span>
        <span class="dim">{{ e.chart }}</span>
        <span class="num">{{ fmt(e.value) }} {{ e.units }}</span>
        <span v-if="e.notified" class="dim" title="已发送通知">✉</span>
      </li>
      <li v-if="!log.length" class="dim">暂无事件</li>
    </ul>
  </div>
</template>

<style scoped>
.panel { background: #0f172a; border: 1px solid #334155; border-radius: 8px; padding: 12px 14px; margin-bottom: 14px; font-size: 13px; }
.head { display: flex; justify-content: space-between; align-items: center; }
.actions { display: flex; gap: 8px; align-items: center; }
.mute { background: #1e293b; color: #cbd5e1; border: 1px solid #334155; border-radius: 6px; padding: 2px 8px; font-size: 12px; cursor: pointer; }
.mute.tiny { padding: 0 6px; font-size: 11px; }
h3 { margin: 0 0 8px; font-size: 13px; color: #cbd5e1; text-transform: uppercase; letter-spacing: .05em; }
h3 small { color: #64748b; font-weight: 400; margin-left: 6px; text-transform: none; }
.x { background: none; border: 0; color: #94a3b8; font-size: 18px; cursor: pointer; }
table { width: 100%; border-collapse: collapse; margin-bottom: 16px; }
th { text-align: left; color: #64748b; font-weight: 500; font-size: 11px; padding: 4px 6px; border-bottom: 1px solid #1e293b; }
td { padding: 4px 6px; border-bottom: 1px solid #111827; white-space: nowrap; }
.num { text-align: right; font-variant-numeric: tabular-nums; }
.dim { color: #64748b; }
.badge { display: inline-block; padding: 1px 6px; border-radius: 4px; font-size: 11px; font-weight: 600; background: #1e293b; color: #94a3b8; }
.critical .badge { background: #7f1d1d; color: #fecaca; }
.warning .badge { background: #78350f; color: #fde68a; }
.clear .badge { background: #14532d; color: #bbf7d0; }
.log { list-style: none; padding: 0; margin: 0; max-height: 260px; overflow: auto; }
.log li { display: flex; gap: 10px; padding: 3px 0; border-bottom: 1px solid #111827; flex-wrap: wrap; }
@media (max-width: 760px) { td:nth-child(3), th:nth-child(3), td:nth-child(5), th:nth-child(5) { display: none; } }
</style>
