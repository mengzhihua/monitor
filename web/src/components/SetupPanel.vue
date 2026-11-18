<script setup lang="ts">
import { computed, ref } from 'vue'
import { get } from '../api'
import type { Info, ChartsResponse, AlarmsResponse, NotificationSnapshot, ServiceReport } from '../api'
import { usePolling } from '../polling'
import { readiness, type Evidence, type Check } from '../features/onboarding/model/readiness'
import NotificationDiagnostics from './NotificationDiagnostics.vue'
const props = defineProps<{ role: string }>()
const emit = defineEmits<{ go: [target: 'charts' | 'config' | 'rules' | 'services'] }>()
const evidence = ref<Evidence>({}), errors = ref<string[]>([]), loading = ref(false), checkedAt = ref(0), showNotifications = ref(false)
const labels = { ready: '已有运行证据', attention: '需要检查', unknown: '尚未确认' }
const checks = computed(() => readiness(evidence.value, checkedAt.value))
const refresh = usePolling(async signal => {
  loading.value = true
  const result = await Promise.allSettled([
    get<Info>('/api/v1/info', signal), get<ChartsResponse>('/api/v1/charts', signal),
    get<AlarmsResponse>('/api/v1/alarms?all=true', signal), get<NotificationSnapshot>('/api/v1/operations/notifications', signal),
    get<ServiceReport>('/api/v1/services', signal),
  ])
  if (signal.aborted) return
  const [info, charts, alarms, notifications, services] = result
  // Clear failed evidence rather than presenting an earlier success as current.
  evidence.value = {
    info: info.status === 'fulfilled' ? info.value : undefined,
    charts: charts.status === 'fulfilled' ? Object.values(charts.value.charts) : undefined,
    alarms: alarms.status === 'fulfilled' ? alarms.value : undefined,
    notifications: notifications.status === 'fulfilled' ? notifications.value : undefined,
    services: services.status === 'fulfilled' ? services.value : undefined,
  }
  const names = ['实例信息', '指标目录', '告警', '通知', '服务目录']
  errors.value = result.flatMap((r, i) => r.status === 'rejected' ? [names[i]!] : [])
  checkedAt.value = Date.now() / 1000
  loading.value = false
}, 15000)
function act(check: Check) {
  if (check.action === 'notifications') showNotifications.value = !showNotifications.value
  else emit('go', check.action)
}
const actionLabel = { charts: '查看本机图表', config: '检查保存配置', rules: '查看本机告警规则', notifications: '打开通知诊断与测试', services: '配置业务服务' }
</script>
<template>
  <section class="setup-panel" aria-label="接入与运行检查">
    <div class="heading"><div><h1>接入与运行检查</h1><p>按运行证据检查本机部署，逐步完成采集、保存、告警和通知验证。</p></div><button :disabled="loading" @click="refresh">{{ loading ? '检查中…' : '重新检查' }}</button></div>
    <p class="scope">当前检查：{{ evidence.info?.host.hostname || '本机（实例信息尚未确认）' }}。此页不跟随顶部远端节点选择；前往图表时切回本机。</p>
    <p v-if="checkedAt" role="status">检查时间：{{ new Date(checkedAt * 1000).toLocaleString() }} · 每 15 秒刷新。绿色表示已有对应运行证据，不是整套部署验收完成。</p>
    <p v-if="errors.length" role="alert">{{ errors.join('、') }}读取失败，相关项目已标为尚未确认。请重新检查。</p>
    <div class="steps">
      <article v-for="check in checks" :key="check.id" :data-check="check.id" :data-state="check.state">
        <div class="heading"><h2>{{ check.title }}</h2><span class="badge">{{ labels[check.state] }}</span></div>
        <p>{{ check.detail }}</p>
        <button v-if="check.action !== 'config' || props.role === 'admin'" @click="act(check)">{{ actionLabel[check.action] }}</button>
        <p v-else class="muted">请联系管理员检查保存配置；当前账号仅可查看状态。</p>
      </article>
    </div>
    <NotificationDiagnostics v-if="showNotifications" />
    <p class="muted">此页不会自动修改配置、发送测试消息或标记人工验收完成。通知测试需在下方明确确认；长期运行、备份恢复和实际收件仍需按现场环境验证。</p>
  </section>
</template>
<style scoped>
.setup-panel { max-width:1100px; margin:auto; padding:24px; color:#e2e8f0; overflow-wrap:anywhere; }
.heading { display:flex; justify-content:space-between; align-items:center; gap:12px; flex-wrap:wrap; }
h1 { margin:0; font-size:26px; } h2 { margin:0; font-size:18px; } p { line-height:1.65; }
.steps { display:grid; grid-template-columns:repeat(auto-fit,minmax(min(100%,360px),1fr)); gap:16px; margin:20px 0; }
article { padding:18px; background:#0f172a; border:1px solid #334155; border-radius:12px; min-width:0; }
.badge { font-size:12px; padding:4px 8px; border-radius:6px; background:#334155; }
[data-state="ready"] .badge { background:#134e4a; color:#99f6e4; } [data-state="attention"] .badge { background:#422f19; color:#fde68a; }
.scope { border-left:3px solid #5eead4; padding-left:12px; }.muted, [role="status"] { color:#94a3b8; font-size:13px; }
[role="alert"] { color:#fda4af; } button { font:inherit; color:#e2e8f0; background:#1e293b; border:1px solid #475569; padding:9px 12px; border-radius:6px; cursor:pointer; } button:disabled { opacity:.5; }
@media(max-width:760px) { .setup-panel { padding:16px 12px; } h1 { font-size:22px; } }
</style>
