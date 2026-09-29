<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api, ApiError } from '../api'
import type { AlertRuleChange, AlertRuleConfig, AlertRulePreview, AlertRuleSpec, AlertRulesSnapshot } from '../api'

const props = defineProps<{ role: string }>()
const emit = defineEmits<{ close: []; changed: [] }>()
const data = ref<AlertRulesSnapshot | null>(null)
const query = ref(''), selected = ref(''), draft = ref(''), originalDraft = ref(''), revision = ref('')
const creating = ref(false), loading = ref(false), previewing = ref(false), busy = ref(false), mustReview = ref(false)
const error = ref(''), readError = ref(''), message = ref('')
const action = ref<AlertRuleChange['action']>('update')
const originalAction = ref<AlertRuleChange['action']>('update')
const preview = ref<AlertRulePreview | null>(null)
let previewBody: AlertRuleChange | null = null
let listRequest: AbortController | undefined, previewRequest: AbortController | undefined
let disposed = false
const pretty = (value: unknown) => JSON.stringify(value, null, 2)
const originName = { base: '基础规则', override: '已覆盖基础', custom: '自定义规则', deleted: '基础规则已删除', invalid: '失效（宏缺失）' }
const actionName = { create: '新建规则', update: '更新规则', delete: '删除规则', reset: '恢复配置规则' }
const target = computed(() => data.value?.configs.find(rule => rule.name === selected.value))
const orphanedDeletion = computed(() => !!target.value?.deleted && !target.value.has_base)
const resetLabel = computed(() => orphanedDeletion.value ? '清除失效删除标记' : '恢复配置规则')
const canManage = computed(() => props.role === 'admin' && !!data.value?.can_manage && !readError.value)
const editing = computed(() => creating.value || !!selected.value)
const dirty = computed(() => editing.value && (creating.value || draft.value !== originalDraft.value || !!preview.value || action.value !== originalAction.value))
const changed = computed(() => editing.value && !!data.value && revision.value !== data.value.revision)
const availableAction = computed(() => creating.value ? action.value === 'create'
  : !!target.value && (action.value === 'update' ? !target.value.deleted
    : action.value === 'delete' ? !target.value.deleted
      : action.value === 'reset' && (target.value.deleted || (target.value.has_base && target.value.origin !== 'base'))))
const canPreview = computed(() => canManage.value && editing.value && availableAction.value && !busy.value && !loading.value && !previewing.value && !mustReview.value && !changed.value)
const canSave = computed(() => canPreview.value && !!preview.value?.valid && !!previewBody && preview.value.revision === revision.value)
const rows = computed(() => {
  const q = query.value.trim().toLowerCase()
  return (data.value?.configs || []).filter(rule => !q || `${rule.name} ${rule.on} ${rule.config.info || ''} ${originName[rule.origin]}`.toLowerCase().includes(q))
})

function invalidatePreview() {
  previewRequest?.abort(); previewRequest = undefined
  preview.value = null; previewBody = null; previewing.value = false
}
watch([draft, action], () => { invalidatePreview(); error.value = ''; message.value = '' }, { flush: 'sync' })
function detail(e: unknown) {
  return (e instanceof Error ? e.message.replace(/^.*?: \d{3}\s*/, '') : String(e)).slice(0, 1200)
}
function authenticated(e: unknown) {
  if (!(e instanceof ApiError) || ![401, 403].includes(e.status)) return false
  data.value = null; invalidatePreview()
  readError.value = '当前登录无法管理告警规则，请重新登录有效账号。'
  return true
}
function parseDraft(): AlertRuleSpec {
  const value: unknown = JSON.parse(draft.value)
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('请输入单个规则 JSON 对象。')
  const rule = value as Record<string, unknown>
  const strings = ['name', 'on', 'class', 'type', 'component', 'lookup', 'calc', 'every', 'units', 'warn', 'crit', 'recovery', 'for', 'keep_firing_for', 'delay', 'repeat', 'info', 'to']
  for (const [key, item] of Object.entries(rule)) {
    if (strings.includes(key)) { if (typeof item !== 'string') throw new Error(`${key} 必须是字符串。`) }
    else if (key === 'disabled') { if (typeof item !== 'boolean') throw new Error('disabled 必须是布尔值。') }
    else if (key === 'chart_labels') {
      if (!item || typeof item !== 'object' || Array.isArray(item) || Object.values(item).some(v => typeof v !== 'string')) throw new Error('chart_labels 必须是字符串键值对象。')
    } else throw new Error(`不支持字段 ${key}。请使用下方列出的规则字段。`)
  }
  if (typeof rule.name !== 'string' || !rule.name.trim() || typeof rule.on !== 'string' || !rule.on.trim()) throw new Error('name 和 on 不能为空。')
  if (!creating.value && rule.name !== selected.value) throw new Error('现有规则名称不能直接修改；需要另一个名称时请新建规则。')
  return rule as unknown as AlertRuleSpec
}
function mayDiscard() {
  return !busy.value && (!dirty.value || confirm('仍有未保存的规则草稿或预览，继续将丢弃它们，确定继续？'))
}
function openRule(rule?: AlertRuleConfig) {
  if (!mayDiscard()) return
  invalidatePreview(); mustReview.value = false; error.value = ''; message.value = ''
  selected.value = rule?.name || ''; creating.value = !rule
  action.value = rule ? (rule.deleted ? 'reset' : 'update') : 'create'
  originalAction.value = action.value
  draft.value = pretty(rule?.config || { name: 'custom_cpu_usage', on: 'system.cpu', lookup: 'average -1m of user,system',
    every: '10s', units: '%', warn: '$this > 80', crit: '$this > 90', info: '过去一分钟的 CPU 使用率' })
  originalDraft.value = draft.value
  revision.value = data.value?.revision || ''
}
async function load(review = false) {
  if (busy.value) return
  listRequest?.abort()
  const request = listRequest = new AbortController()
  loading.value = true
  try {
    const next = await api.alertRules(request.signal)
    if (disposed || request.signal.aborted || listRequest !== request) return
    data.value = next; readError.value = ''
    if (review && editing.value) {
      invalidatePreview()
      revision.value = next.revision
      mustReview.value = !creating.value && !next.configs.some(rule => rule.name === selected.value)
      error.value = mustReview.value ? '原规则已不存在。JSON 草稿已保留，请复制需要的内容后选择其他规则或新建。' : ''
      message.value = mustReview.value ? '' : '已读取最新列表并保留草稿。请核对服务器当前定义，再重新校验和预览；不会自动提交。'
    }
  } catch (e) {
    if (disposed || request.signal.aborted || listRequest !== request) return
    if (!authenticated(e)) readError.value = e instanceof ApiError && e.status === 404
      ? '本机健康引擎未启用，告警规则暂时不可用。' : '告警规则读取失败，上次列表仅供参考。请恢复连接后重新读取。'
  } finally {
    if (listRequest === request) { listRequest = undefined; loading.value = false }
  }
}
function toggleDisabled() {
  if (!canManage.value || busy.value || action.value !== 'update') return
  try { const rule = parseDraft(); rule.disabled = !rule.disabled; draft.value = pretty(rule) }
  catch (e) { error.value = `JSON 无效：${detail(e)}` }
}
async function validate() {
  if (!canPreview.value) return
  error.value = ''; message.value = ''; invalidatePreview()
  let body: AlertRuleChange
  try {
    body = action.value === 'create' || action.value === 'update'
      ? { revision: revision.value, action: action.value, config: parseDraft() }
      : { revision: revision.value, action: action.value, name: selected.value }
  } catch (e) { error.value = `JSON 无效：${detail(e)}`; return }
  const request = previewRequest = new AbortController()
  previewing.value = true
  try {
    const next = await api.previewAlertRule(body, request.signal)
    if (disposed || request.signal.aborted || previewRequest !== request) return
    preview.value = next
    previewBody = body
  } catch (e) {
    if (disposed || request.signal.aborted || previewRequest !== request) return
    if (e instanceof ApiError && e.status === 409) {
      mustReview.value = true; error.value = '规则已在别处修改，草稿已保留。请读取最新列表并重新核对。'
    } else if (!authenticated(e)) error.value = `预览未通过：${detail(e)}`
  } finally { if (previewRequest === request) { previewRequest = undefined; previewing.value = false } }
}
async function save() {
  if (!canSave.value || !previewBody) return
  const body = previewBody
  const label = body.action === 'reset' ? resetLabel.value : actionName[body.action]
  busy.value = true; error.value = ''; message.value = ''
  try {
    const next = await api.changeAlertRule(body)
    if (disposed) return
    data.value = next; readError.value = ''; invalidatePreview(); mustReview.value = false
    const name = 'config' in body ? body.config.name : body.name
    const saved = next.configs.find(rule => rule.name === name)
    selected.value = saved?.name || ''; creating.value = false
    action.value = saved?.deleted ? 'reset' : 'update'
    originalAction.value = action.value
    draft.value = saved ? pretty(saved.config) : ''; originalDraft.value = draft.value; revision.value = next.revision
    message.value = `${label}已保存并生效。${next.persistent ? '规则保存在服务器，重启后继续生效。' : '当前仅保存在内存，服务重启后会丢失。'}`
    emit('changed')
  } catch (e) {
    if (disposed) return
    if (e instanceof ApiError && [409, 404].includes(e.status)) {
      mustReview.value = true; invalidatePreview()
      error.value = '规则或版本已变化，本次未保存，草稿已保留。请读取最新列表并重新核对。'
    } else if (authenticated(e)) return
    else if (e instanceof ApiError && [400, 413, 422, 503].includes(e.status)) error.value = `保存失败，本次未修改，草稿已保留：${detail(e)}`
    else {
      mustReview.value = true; invalidatePreview()
      error.value = '请求中断，保存结果不确定。草稿已保留，请读取最新列表并核对，不要直接重复提交。'
    }
  } finally { if (!disposed) busy.value = false }
}
function close() {
  if (!mayDiscard()) return false
  emit('close'); return true
}
function beforeUnload(event: BeforeUnloadEvent) {
  if (dirty.value || busy.value) { event.preventDefault(); event.returnValue = '' }
}
defineExpose({ requestClose: close })
onMounted(() => { void load(); window.addEventListener('beforeunload', beforeUnload) })
onBeforeUnmount(() => {
  disposed = true; listRequest?.abort(); previewRequest?.abort()
  window.removeEventListener('beforeunload', beforeUnload)
})
</script>

<template>
  <section class="rules-panel" aria-label="本机告警规则">
    <div class="heading"><div><h2>本机告警规则</h2><p class="muted">实例：{{ data?.hostname || '正在读取本机信息' }}。当前选择远端节点时，此处仍管理本机规则。</p></div><button :disabled="busy" @click="close" aria-label="关闭告警规则">关闭</button></div>
    <p class="muted">规则决定告警如何求值。通知静默、维护计划与问题处理仍使用原有入口；此处不会执行通知测试。</p>
    <div class="toolbar"><input v-model="query" placeholder="搜索规则名称、图表或说明…" aria-label="搜索告警规则" /><button :disabled="loading || busy" @click="load()">{{ loading ? '读取中…' : '刷新规则列表' }}</button><button v-if="canManage" :disabled="busy || loading" @click="openRule()">新建规则</button></div>
    <p v-if="readError" role="alert" class="notice">{{ readError }}</p>
    <p v-if="error" role="alert" class="notice">{{ error }}</p>
    <p v-if="message" role="status" class="success">{{ message }}</p>
    <p v-if="data && !canManage && !readError" class="muted">当前账号只读。只有已登录的管理员可以校验、预览和修改规则。</p>
    <template v-if="data">
      <p class="muted">{{ rows.length }} / {{ data.count }} 条规则 · {{ data.persistent ? '保存后重启继续生效' : '当前未启用规则持久化' }}</p>
      <div class="workspace">
        <div class="rule-list" aria-label="告警规则列表">
          <button v-for="rule in rows" :key="rule.name" :data-rule-name="rule.name" :aria-pressed="selected === rule.name && !creating" :disabled="busy" @click="openRule(rule)">
            <strong>{{ rule.name }}</strong><span>{{ rule.on }}</span><small>{{ originName[rule.origin] }} · {{ rule.deleted ? '已删除' : rule.config.disabled ? '已禁用' : '已启用' }}</small>
          </button>
          <p v-if="!rows.length" class="muted">没有匹配的规则。</p>
        </div>
        <div class="editor">
          <template v-if="editing">
            <h3>{{ creating ? '新建自定义规则' : selected }}</h3>
            <p v-if="target" class="muted">来源：{{ target.source }} · 求值间隔 {{ target.every }} 秒</p>
            <label v-if="canManage && !creating">规则操作<select v-model="action" :disabled="busy" aria-label="规则操作"><option v-if="target && !target.deleted" value="update">编辑规则</option><option v-if="target && !target.deleted" value="delete">删除规则</option><option v-if="target?.deleted || (target?.has_base && target.origin !== 'base')" value="reset">{{ resetLabel }}</option></select></label>
            <p v-if="action === 'delete'" class="notice">{{ target?.has_base ? '将停用这条配置规则并保留删除标记，之后可恢复配置规则。' : '将删除这条自定义规则。' }}JSON 草稿不会作为删除操作提交。</p>
            <p v-if="action === 'reset'" class="notice">{{ orphanedDeletion ? '基础配置中已不存在这条规则。此操作只清除失效的删除标记，不会新建规则。' : '将移除对这条规则的覆盖或删除记录，恢复服务器加载的基础定义。' }}</p>
            <label>规则 JSON<textarea v-model="draft" :readonly="!canManage || busy || ['delete','reset'].includes(action)" spellcheck="false" aria-label="规则 JSON" /></label>
            <details class="guide"><summary>规则字段与示例说明</summary><p>name 是规则名称，on 是图表 ID 或 context；lookup 或 calc 至少填写一个。warn / crit 使用 $this 判断警告与严重级别，every 为求值间隔。</p><p>支持 name、on、class、type、component、lookup、calc、every、units、warn、crit、recovery、for、keep_firing_for、delay、repeat、info、to、chart_labels、disabled。JSON 不支持注释。</p><p>for 是状态升到警告或严重之前条件必须连续成立的时间；keep_firing_for 是条件恢复后仍保持已升高状态的时间。recovery 在警告和严重表达式都不成立后，还要成立才回到正常。delay 只推迟通知，不改变状态何时切换。disabled 为 true 时停止该规则求值；需要临时抑制通知时，请使用静默或维护计划。</p></details>
            <div v-if="canManage" class="toolbar editor-actions"><button v-if="action === 'update'" :disabled="busy" @click="toggleDisabled">切换启用 / 禁用草稿</button><button :disabled="!canPreview" @click="validate">{{ previewing ? '正在校验…' : '校验并预览' }}</button></div>
            <p v-if="changed || mustReview" class="notice">服务器版本已变化或需要重新核对。当前 JSON 草稿和原保存基线已保留。</p>
            <button v-if="canManage && (changed || mustReview)" :disabled="loading || busy" @click="load(true)">读取最新并重新核对</button>
            <details v-if="canManage && target && draft !== pretty(target.config)" class="latest"><summary>查看服务器当前定义，与草稿核对</summary><pre>{{ pretty(target.config) }}</pre></details>
            <section v-if="preview" aria-label="告警规则预览" class="preview">
              <h3>{{ preview.action === 'reset' ? resetLabel : actionName[preview.action] }}：{{ preview.name }}</h3>
              <p>{{ preview.action === 'delete' ? '保存后该规则将不再求值。' : orphanedDeletion && preview.action === 'reset' ? '清除标记后不会新建规则。' : preview.disabled ? '规则将保持禁用，不进行求值。' : '规则未禁用。' }} 当前匹配 {{ preview.matched }} 个图表。</p>
              <p>此次预览只校验配置并展示匹配范围，不执行告警表达式，也不发送通知。</p>
              <p v-if="preview.notice" class="muted">{{ preview.notice }}</p>
              <p v-if="!preview.matched" class="notice">当前没有匹配图表。请核对 on 与 chart_labels，或确认对应采集器之后会提供图表。</p>
              <ul><li v-for="chart in preview.charts" :key="chart.id"><code>{{ chart.id }}</code> · {{ chart.title }}<small>{{ chart.context }} · {{ chart.family }}</small></li></ul>
              <p v-if="preview.truncated" class="muted">匹配范围较大，仅展示前 {{ preview.limit }} 个图表。</p>
              <button v-if="canManage" :disabled="!canSave" @click="save">{{ busy ? '保存中…' : '确认保存规则变更' }}</button>
            </section>
          </template>
          <p v-else class="muted">选择一条规则查看定义，或新建自定义规则。</p>
        </div>
      </div>
    </template>
  </section>
</template>

<style scoped>
.rules-panel { padding:18px; margin-bottom:18px; border:1px solid #334155; border-radius:12px; background:#0f172a; color:#e2e8f0; }
.heading,.toolbar { display:flex; align-items:center; justify-content:space-between; gap:10px; flex-wrap:wrap; }.heading { align-items:flex-start; }.toolbar { justify-content:flex-start; margin:14px 0; }
h2 { margin:0 0 8px; font-size:20px; }h3 { font-size:15px; margin:0 0 12px; overflow-wrap:anywhere; }p { font-size:12px; line-height:1.7; margin:8px 0; }.muted,small { color:#94a3b8; }.toolbar input { flex:1; min-width:min(260px,100%); }
button,input,select,textarea { font:inherit; font-size:12px; color:inherit; background:#17243a; border:1px solid #475569; border-radius:7px; padding:8px 10px; box-sizing:border-box; max-width:100%; }button { cursor:pointer; }button:disabled { opacity:.5; cursor:default; }button:hover:not(:disabled) { border-color:#5eead4; }button:focus-visible,input:focus,select:focus,textarea:focus { outline:2px solid #5eead4; outline-offset:2px; }
.workspace { display:grid; grid-template-columns:minmax(180px,280px) minmax(0,1fr); gap:18px; align-items:start; }.rule-list { display:flex; flex-direction:column; gap:7px; max-height:620px; overflow:auto; }.rule-list button { text-align:left; overflow-wrap:anywhere; }.rule-list button[aria-pressed="true"] { border-color:#2dd4bf; background:#123b3b; }.rule-list strong,.rule-list span,.rule-list small { display:block; line-height:1.7; }.rule-list strong { font-size:12px; }.rule-list span { color:#cbd5e1; }.rule-list small { font-size:11px; }
.editor { min-width:0; }.editor label { display:block; font-size:12px; margin:12px 0; }.editor select { margin-left:10px; }textarea { display:block; width:100%; min-height:290px; resize:vertical; margin-top:8px; font-family:ui-monospace,SFMono-Regular,Consolas,monospace; line-height:1.65; tab-size:2; }textarea[readonly] { color:#a8b5c8; }.notice,.success { padding:10px 12px; border:1px solid #63502d; border-radius:7px; background:#302919; color:#fde68a; overflow-wrap:anywhere; }.success { background:#0b3430; border-color:#23685d; color:#99f6e4; }.guide,.latest { margin:14px 0; font-size:12px; }.guide p,.latest pre { overflow-wrap:anywhere; }.latest pre { white-space:pre-wrap; background:#111d31; padding:12px; }summary { cursor:pointer; color:#94a3b8; }.preview { margin-top:18px; padding:14px; background:#102a2b; border:1px solid #23685d; border-radius:9px; font-size:12px; }.preview ul { padding-left:20px; max-height:230px; overflow:auto; }.preview li { margin:8px 0; overflow-wrap:anywhere; }.preview small { display:block; }
@media(max-width:760px) { .rules-panel { padding:12px; }.workspace { grid-template-columns:minmax(0,1fr); }.rule-list { max-height:230px; }.toolbar input { flex-basis:100%; }.editor select { display:block; margin:8px 0 0; width:100%; }.heading>div { min-width:0; } }
</style>
