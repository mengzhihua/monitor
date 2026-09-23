<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { api, ApiError } from '../api'
import type { MaintenanceChange, MaintenancePlan, MaintenanceSnapshot, MaintenanceSpec } from '../api'
import { usePolling } from '../polling'

const data = ref<MaintenanceSnapshot | null>(null)
const readError = ref(''), error = ref(''), message = ref('')
const busy = ref(false), loading = ref(false), mustReview = ref(false)
const loadedAt = ref(0), limit = ref(20), state = ref('current')
const draft = ref({ title: '', reason: '', scope: 'alarm', target: '', mode: 'now', start: '', duration: 3600 })
const preview = ref<{ plan: MaintenanceSpec; revision: number } | null>(null)
const cancel = ref<{ plan: MaintenancePlan; revision: number; reason: string } | null>(null)
const names: Record<string, string> = { active: '维护中', scheduled: '待开始', ended: '已到期', canceled: '已取消' }
const time = (t: number) => new Date(t * 1000).toLocaleString()
const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
const describe = (p: MaintenanceSpec) => p.scope === 'all' ? '本机所有告警（包括以后新建的告警）' : `${p.chart} · ${p.alarm}`
const current = computed(() => data.value?.plans.filter(p => ['active', 'scheduled'].includes(p.state)) || [])
const rows = computed(() => (data.value?.plans || []).filter(p => state.value === 'all' || (state.value === 'current' ? ['active', 'scheduled'].includes(p.state) : p.state === state.value)))
const changed = computed(() => !!data.value && ((preview.value && preview.value.revision !== data.value.revision) || (cancel.value && (cancel.value.revision !== data.value.revision || !current.value.some(p => p.id === cancel.value!.plan.id)))))
const canWrite = computed(() => data.value?.can_manage && !readError.value && !busy.value && !mustReview.value)
const target = computed(() => data.value?.targets.find(t => JSON.stringify([t.chart, t.alarm]) === draft.value.target))
const valid = computed(() => draft.value.title.trim() && draft.value.reason.trim() && (draft.value.scope === 'all' || target.value) && (draft.value.mode === 'now' || Number.isFinite(new Date(draft.value.start).getTime())))
watch(state, () => { limit.value = 20 })
let generation = 0, disposed = false
onBeforeUnmount(() => { disposed = true; generation++ })
const refresh = usePolling(async signal => {
  if (busy.value) return
  const epoch = generation
  loading.value = true
  try {
    const next = await api.maintenance(signal)
    if (signal.aborted || disposed || epoch !== generation) return
    data.value = next; loadedAt.value = Math.floor(Date.now() / 1000); readError.value = ''
  } catch (e) {
    if (signal.aborted || disposed || epoch !== generation) return
    if (e instanceof ApiError && [401, 403].includes(e.status)) {
      data.value = null; preview.value = null; cancel.value = null
      readError.value = '当前登录无法读取维护计划，请重新登录。'
    } else readError.value = '维护计划刷新失败；上次结果仅供参考，请刷新后再操作。'
  } finally { if (!signal.aborted && epoch === generation) loading.value = false }
}, 15000)

function review() {
  if (!canWrite.value || !valid.value || !data.value) return
  preview.value = { revision: data.value.revision, plan: {
    title: draft.value.title.trim(), reason: draft.value.reason.trim(), scope: draft.value.scope as 'all' | 'alarm',
    chart: draft.value.scope === 'all' ? '' : target.value!.chart, alarm: draft.value.scope === 'all' ? '' : target.value!.alarm,
    starts_at: draft.value.mode === 'now' ? 0 : Math.floor(new Date(draft.value.start).getTime() / 1000), duration_seconds: Number(draft.value.duration),
  } }
  cancel.value = null; error.value = ''; message.value = ''
}
function reviewCancel(p: MaintenancePlan) {
  if (!canWrite.value || !data.value) return
  cancel.value = { plan: { ...p }, revision: data.value.revision, reason: '' }
  preview.value = null; error.value = ''; message.value = ''
}
async function reread() {
  await refresh()
  if (!disposed && !readError.value && data.value) {
    preview.value = null; cancel.value = null; mustReview.value = false; error.value = ''
    message.value = '已读取最新列表。请先核对现有计划，再重新预览或选择取消；不会自动重试。'
  }
}
async function save(body: MaintenanceChange) {
  if (!canWrite.value || changed.value) return
  generation++; busy.value = true; loading.value = false; error.value = ''; message.value = ''
  try {
    const next = await api.changeMaintenance(body)
    if (disposed) return
    data.value = next; loadedAt.value = Math.floor(Date.now() / 1000)
    preview.value = null; cancel.value = null
    if (body.action === 'create') { draft.value.title = ''; draft.value.reason = ''; state.value = 'current' }
    message.value = body.action === 'create' ? '维护计划已保存。' : '本计划已取消；其他重叠计划或静默仍可能生效。'
  } catch (e) {
    if (disposed) return
    if (e instanceof ApiError && e.status === 409) { mustReview.value = true; error.value = '计划列表已变化或该计划已结束。本次未修改，请读取最新列表并重新核对。' }
    else if (e instanceof ApiError && e.status === 503) error.value = '维护计划保存失败，本次未修改。内容已保留，可重试。'
    else if (e instanceof ApiError && [401, 403].includes(e.status)) { data.value = null; preview.value = null; cancel.value = null; error.value = '没有维护管理权限或登录已失效，请重新登录。' }
    else if (e instanceof ApiError && e.status === 400) error.value = '计划无效，请返回修改：填写标题与原因，指定当前本机告警，开始时间不能过去且应在 90 天内，最长维护 7 天。'
    else if (e instanceof ApiError && e.status === 422) error.value = '维护计划容量已满，请取消不再需要的待执行计划后重新核对。'
    else { mustReview.value = true; error.value = '请求中断，保存结果不确定。请读取最新列表，核对是否已保存后再操作。' }
  } finally { busy.value = false }
}
</script>

<template>
  <section class="maintenance" aria-label="计划维护窗口">
    <div class="row heading"><div><h2>计划维护窗口 <small>本机</small></h2><p class="muted">安排变更期间的通知抑制，采集、告警求值和问题展示继续进行。</p></div><button :disabled="busy || loading" @click="refresh()">刷新维护计划</button></div>
    <p v-if="loading && !data" role="status">正在读取维护计划…</p>
    <p v-if="readError" role="alert" class="notice">{{ readError }}<span v-if="data"> 上次成功：{{ time(loadedAt) }}</span></p>
    <p v-if="error" role="alert" class="notice">{{ error }}</p>
    <p v-if="message" role="status" class="success">{{ message }}</p>
    <template v-if="data">
      <p v-if="!data.available" class="muted">本机健康引擎未启用，无法安排维护计划。</p>
      <template v-else>
        <p class="muted">实例：{{ data.hostname }} · {{ data.persistent ? '计划已启用持久保存，重启后继续生效' : '仅存内存，重启丢失' }}。不向远端 Agent 下发计划。</p>
        <p class="counts">维护中 <b>{{ current.filter(p => p.state === 'active').length }}</b> · 待开始 <b>{{ current.filter(p => p.state === 'scheduled').length }}</b> · 当前与待执行上限 {{ data.pending_limit }} 项</p>
        <p v-if="!data.can_manage" class="muted">只有登录的管理员可以创建或取消维护计划；当前账号可查看。</p>
        <p class="muted">时间按浏览器时区 {{ zone }} 显示。窗口起点包含、终点不包含；到期或取消只解除本计划，不补发已抑制的通知，也不清除其他静默。已经排队的通知仍可能发送。</p>
        <div v-if="mustReview || changed" class="notice"><p>计划列表已变化，预览中的版本保持不变。请读取最新列表并重新核对后再提交。</p><button :disabled="busy || loading" @click="reread">读取最新列表并重新核对</button></div>
        <details v-if="data.can_manage" class="create">
          <summary>新增维护计划</summary>
          <form v-if="!preview" @submit.prevent="review">
            <fieldset :disabled="busy || mustReview">
              <label>标题<input v-model="draft.title" aria-label="维护标题" maxlength="40" required placeholder="例如：数据库例行升级" /></label>
              <label>维护原因<input v-model="draft.reason" aria-label="维护原因" maxlength="512" required placeholder="填写变更内容和原因" /></label>
              <div class="row"><label>范围<select v-model="draft.scope" aria-label="维护范围"><option value="alarm">指定本机告警</option><option value="all">本机所有告警</option></select></label>
                <label v-if="draft.scope === 'alarm'">告警<select v-model="draft.target" aria-label="维护目标告警"><option value="" disabled>选择图表与告警</option><option v-for="t in data.targets" :key="JSON.stringify([t.chart,t.alarm])" :value="JSON.stringify([t.chart,t.alarm])">{{ t.chart }} · {{ t.alarm }}</option></select></label></div>
              <div class="row"><label>开始<select v-model="draft.mode" aria-label="维护开始方式"><option value="now">保存后立即开始</option><option value="scheduled">指定时间</option></select></label><label v-if="draft.mode === 'scheduled'">开始时间<input type="datetime-local" v-model="draft.start" aria-label="维护开始时间" required /></label>
                <label>时长<select v-model="draft.duration" aria-label="维护时长"><option :value="900">15 分钟</option><option :value="3600">1 小时</option><option :value="14400">4 小时</option><option :value="86400">24 小时</option><option :value="604800">7 天</option></select></label></div>
              <button :disabled="!canWrite || !valid" type="submit">预览维护计划</button>
            </fieldset>
          </form>
          <div v-else class="review" role="region" aria-label="维护计划预览"><h3>{{ preview.plan.title }}</h3><p>实例：{{ data.hostname }}</p><p>抑制范围：<b>{{ describe(preview.plan) }}</b></p><p>开始：{{ preview.plan.starts_at ? time(preview.plan.starts_at) : '服务器收到并保存后立即开始' }} · 持续 {{ preview.plan.duration_seconds / 60 }} 分钟</p><p>原因：{{ preview.plan.reason }}</p><div class="row"><button :disabled="!canWrite || !!changed" @click="save({ action: 'create', ...preview })">确认创建维护计划</button><button :disabled="busy" @click="preview = null">返回修改计划</button></div></div>
        </details>
        <div v-if="cancel" class="review" role="region" aria-label="取消维护预览"><h3>取消：{{ cancel.plan.title }}</h3><p>{{ describe(cancel.plan) }} · {{ time(cancel.plan.starts_at) }} — {{ time(cancel.plan.ends_at) }}</p><label>取消原因<input v-model="cancel.reason" aria-label="取消维护原因" maxlength="512" :disabled="busy" /></label><div class="row"><button :disabled="!canWrite || !!changed || !cancel.reason.trim()" @click="save({ action: 'cancel', revision: cancel.revision, id: cancel.plan.id, reason: cancel.reason.trim() })">确认取消该维护计划</button><button :disabled="busy" @click="cancel = null">保留维护计划</button></div></div>
        <div class="row filter"><label>显示<select v-model="state" aria-label="维护计划状态"><option value="current">当前与待执行</option><option value="all">全部记录</option><option value="ended">已到期</option><option value="canceled">已取消</option></select></label><span class="muted">已存 {{ data.plans.length }} / {{ data.history_limit }} 条；新增时只淘汰最早创建的已结束或取消记录。</span></div>
        <p v-if="!rows.length" class="muted">当前筛选下没有维护计划。</p>
        <article v-for="p in rows.slice(0,limit)" :key="p.id" :data-maintenance-id="p.id" class="plan">
          <div class="row heading"><strong>{{ p.title }}</strong><span class="badge" :class="p.state">{{ names[p.state] }}</span></div><p>{{ describe(p) }}</p><p>{{ time(p.starts_at) }} — {{ time(p.ends_at) }}</p><p>原因：{{ p.reason }}</p><p class="muted">{{ p.created_by }} 创建于 {{ time(p.created_at) }}</p><p v-if="p.canceled_at" class="muted">{{ p.canceled_by }} 取消于 {{ time(p.canceled_at) }} · {{ p.cancel_reason }}</p><button v-if="data.can_manage && ['active','scheduled'].includes(p.state)" :disabled="!canWrite" :aria-label="'取消维护 ' + p.title" @click="reviewCancel(p)">取消计划</button>
        </article>
        <button v-if="rows.length > limit" @click="limit += 20">再显示 20 条维护计划</button>
      </template>
    </template>
  </section>
</template>

<style scoped>
.maintenance { border-top:1px solid #334155; margin-top:28px; padding-top:22px; color:#e2e8f0; }
h2 { margin:0; font-size:20px; } h2 small { color:#5eead4; margin-left:8px; } h3 { margin:0 0 10px; font-size:16px; }
.row { display:flex; flex-wrap:wrap; align-items:center; gap:10px; }.heading { justify-content:space-between; } p { line-height:1.6; margin:9px 0; overflow-wrap:anywhere; }.muted,small { font-size:12px; color:#94a3b8; }.counts { font-size:14px; }.counts b { color:#5eead4; padding:0 5px; }
.notice,.review { padding:14px; border:1px solid #6b542e; border-radius:8px; background:#30291e; margin:12px 0; overflow-wrap:anywhere; }.notice { color:#fde68a; font-size:13px; }.review { background:#112a35; border-color:#2b6570; }.success { color:#5eead4; font-size:13px; }
.create,.plan { padding:14px; border:1px solid #334155; border-radius:9px; margin:12px 0; background:#152135; min-width:0; }.plan { font-size:13px; }.plan strong { overflow-wrap:anywhere; } summary { cursor:pointer; }.filter { margin:16px 0; }
fieldset { margin:12px 0 0; padding:0; border:0; min-width:0; } label { display:flex; flex-wrap:wrap; align-items:center; gap:8px; margin:9px 0; max-width:100%; font-size:13px; } form>fieldset>label input { flex:1; } button,input,select { background:#111d31; color:#e2e8f0; font:inherit; padding:8px 11px; border:1px solid #475569; border-radius:6px; min-width:0; max-width:100%; box-sizing:border-box; } input,select { min-height:36px; } input[type=datetime-local] { color-scheme:dark; } button { cursor:pointer; } button:disabled,fieldset:disabled { opacity:.5; cursor:default; }.badge { font-size:12px; background:#334155; border-radius:5px; padding:3px 8px; }.badge.active { color:#fde68a; background:#49341c; }.badge.scheduled { color:#93c5fd; background:#1e3a5f; }
@media(max-width:600px) { .row label { width:100%; } label select,label input { flex:1; } }
</style>
