<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, toRaw, watch } from 'vue'
import type { AgentVisual, ManageConfig, NodeConfigFull, NodeInfo } from '../api'
import { ApiError, api } from '../api'
import AgentConfigForm from './AgentConfigForm.vue'
import { usePolling } from '../polling'

const props = defineProps<{ canManage: boolean; isHub: boolean }>()
const emit = defineEmits<{ close: [] }>()

const tab = ref<'local' | 'nodes'>('local')
const busy = ref(false)
const localLoading = ref(false)
const localReloading = ref(false)
const nodeReloading = ref(false)
const nodeLoading = ref(false)
const loading = computed(() => tab.value === 'local' ? localLoading.value : nodeLoading.value)
const error = ref('')
const message = ref('')
let disposed = false
let localRequest: AbortController | undefined
let nodeRequest: AbortController | undefined
let nodesRequest: AbortController | undefined
const time = (t?: number) => (t ? new Date(t * 1000).toLocaleString() : '—')

/** 提取 ApiError 携带的后端说明文字（JSON 的 error/message 字段或纯文本 body）。 */
function detail(e: unknown): string {
  const raw = e instanceof Error ? e.message : String(e)
  if (!(e instanceof ApiError)) return raw
  const body = raw.replace(/^\S+: \d+ /, '')
  try {
    const parsed = JSON.parse(body) as { error?: unknown; message?: unknown }
    const text = parsed?.error ?? parsed?.message
    if (typeof text === 'string' && text) return text
  } catch { /* 纯文本响应 */ }
  return body || raw
}

function authError(e: unknown): boolean {
  return e instanceof ApiError && [401, 403].includes(e.status)
}

/* ---------- 本机配置 ---------- */
const local = shallowRef<ManageConfig | null>(null)
const localBaseline = shallowRef<ManageConfig | null>(null)
const localDraft = ref('')
const localEditor = ref<'yaml' | 'form'>('yaml')
const localForm = ref<AgentVisual | null>(null)
const formError = ref('')
let localBase = ''
let localUpdated = 0
let formSnapshot = ''
let preferForm = true

function formDirty() {
  if (!localForm.value) return false
  return JSON.stringify(localForm.value) !== formSnapshot
}

function localDirty() { return localDraft.value !== localBase || formDirty() }
const localConflict = computed(() => !!local.value && !!localBaseline.value && (local.value.updated !== localBaseline.value.updated || local.value.yaml !== localBaseline.value.yaml))

function applyLocal(cfg: ManageConfig, force = false) {
  local.value = cfg
  if (!force && localDirty()) return // Keep the revision paired with the draft.
  localBaseline.value = cfg
  localDraft.value = localBase = cfg.yaml
  localUpdated = cfg.updated
  formError.value = cfg.form_error || ''
  const incoming = cfg.form ? structuredClone(toRaw(cfg.form)) : null
  localForm.value = incoming
  formSnapshot = incoming ? JSON.stringify(incoming) : ''

  if (preferForm && incoming && !formError.value) {
    localEditor.value = 'form'
    preferForm = false
  }
}

async function loadLocal(force = false) {
  localRequest?.abort()
  const request = localRequest = new AbortController()
  localLoading.value = true
  localReloading.value = force
  try {
    const cfg = await api.manageConfig(request.signal)
    if (disposed || request.signal.aborted || localRequest !== request) return
    applyLocal(cfg, force)
  } catch (e) {
    if (disposed || request.signal.aborted || localRequest !== request) return
    error.value = authError(e) ? '没有管理权限或登录已失效，请重新登录。' : `读取本机配置失败：${detail(e)}`
  } finally { if (localRequest === request) { localRequest = undefined; localLoading.value = false; localReloading.value = false } }
}

function switchEditor(editor: 'yaml' | 'form') {
  if (editor === localEditor.value || busy.value || localReloading.value) return
  if (localDirty()) {
    if (!confirm('切换编辑方式将放弃未保存修改。请先保存或复制草稿，确定切换？')) return
    if (local.value) applyLocal(local.value, true)
  }
  localEditor.value = editor
}

async function reloadLocal() {
  if (localDirty() && !confirm('载入最新配置将放弃当前草稿。请先复制需要保留的内容，确定继续？')) return
  error.value = ''; message.value = ''
  await loadLocal(true)
}

async function saveLocal() {
  if (!props.canManage || busy.value || localConflict.value || !local.value || local.value.writable === false) return
  busy.value = true; error.value = ''; message.value = ''
  try {
    const next = localEditor.value === 'form' && localForm.value
      ? await api.putManageForm(localForm.value, localUpdated)
      : await api.putManageConfig(localDraft.value, localUpdated)
    applyLocal(next, true)
    message.value = '配置已保存并校验通过。重启服务后生效。'
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) {
      await loadLocal()
      error.value = '配置已在别处被修改，草稿已保留。请核对最新配置后重新编辑。'
    } else if (e instanceof ApiError && e.status === 400) error.value = `配置无效，未保存：${detail(e)}`
    else if (authError(e)) error.value = '没有管理权限或登录已失效，请重新登录。'
    else error.value = String(e)
  } finally { busy.value = false }
}

async function rollback() {
  if (!props.canManage || busy.value || localReloading.value || !local.value?.backup) return
  if (!confirm('恢复上一份已保存的配置？当前文件会变成新的备份。')) return
  busy.value = true; error.value = ''; message.value = ''
  try {
    applyLocal(await api.rollbackManageConfig(), true)
    message.value = '已恢复上一份配置。重启服务后生效。'
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) error.value = '没有可恢复的备份。'
    else if (authError(e)) error.value = '没有管理权限或登录已失效，请重新登录。'
    else error.value = String(e)
  } finally { busy.value = false }
}

async function restart() {
  if (!props.canManage || busy.value) return
  if (!confirm('重启将短暂中断本机监控与 Web 服务（数秒），服务将自动重新启动。确定继续？')) return
  busy.value = true; error.value = ''; message.value = ''
  try {
    await api.restartService()
    message.value = '重启指令已发出，页面将在服务恢复后自动重连。'
  } catch (e) {
    error.value = authError(e) ? '没有管理权限或登录已失效，请重新登录。' : String(e)
  } finally { busy.value = false }
}

/* ---------- 节点配置（仅 hub） ---------- */
const nodes = ref<NodeInfo[]>([])
const nodesLoaded = ref(false)
const nodeID = ref('')
const nodeCfg = shallowRef<NodeConfigFull | null>(null)
const nodeBaseline = shallowRef<NodeConfigFull | null>(null)
const nodeDraft = ref('')
let nodeBase = ''
let nodeUpdated = 0
function nodeText(cfg: NodeConfigFull) { return cfg.yaml || cfg.reported || '' }
function nodeDirty() { return nodeDraft.value !== nodeBase }
const nodeConflict = computed(() => !!nodeCfg.value && !!nodeBaseline.value && ((nodeCfg.value.updated ?? 0) !== (nodeBaseline.value.updated ?? 0) || nodeText(nodeCfg.value) !== nodeText(nodeBaseline.value)))

async function loadNodes() {
  nodesRequest?.abort()
  const request = nodesRequest = new AbortController()
  try {
    const r = await api.nodes(request.signal)
    if (disposed || request.signal.aborted || nodesRequest !== request) return
    nodes.value = r.nodes.filter((n) => !n.local)
    if (nodeID.value && !nodes.value.some((n) => n.id === nodeID.value)) nodeID.value = ''
    nodesLoaded.value = true
  } catch (e) {
    if (disposed || request.signal.aborted || nodesRequest !== request) return
    error.value = authError(e) ? '没有管理权限或登录已失效，请重新登录。' : `读取节点列表失败：${detail(e)}`
  } finally { if (nodesRequest === request) nodesRequest = undefined }
}

function applyNode(cfg: NodeConfigFull, force = false) {
  nodeCfg.value = cfg
  if (!force && nodeDirty()) return
  nodeBaseline.value = cfg
  nodeDraft.value = nodeBase = nodeText(cfg)
  nodeUpdated = cfg.updated ?? 0
}

async function loadNode(force = false, signal?: AbortSignal) {
  nodeRequest?.abort()
  const id = nodeID.value
  if (!id || tab.value !== 'nodes') { nodeRequest = undefined; nodeLoading.value = false; return }
  const request = nodeRequest = new AbortController()
  const abort = () => request.abort()
  signal?.addEventListener('abort', abort, { once: true })
  if (signal?.aborted) abort()
  nodeLoading.value = true
  nodeReloading.value = force
  try {
    const cfg = await api.nodeConfig(id, request.signal)
    if (disposed || request.signal.aborted || nodeRequest !== request || nodeID.value !== id) return
    applyNode(cfg, force)
  } catch (e) {
    if (disposed || request.signal.aborted || nodeRequest !== request || nodeID.value !== id) return
    error.value = authError(e) ? '没有管理权限或登录已失效，请重新登录。' : `读取节点配置失败：${detail(e)}`
  } finally {
    signal?.removeEventListener('abort', abort)
    if (nodeRequest === request) { nodeRequest = undefined; nodeLoading.value = false; nodeReloading.value = false }
  }
}

async function reloadNode() {
  if (nodeDirty() && !confirm('载入最新配置将放弃当前草稿。请先复制需要保留的内容，确定继续？')) return
  error.value = ''; message.value = ''
  await loadNode(true)
}

function selectNode(event: Event) {
  const select = event.target as HTMLSelectElement
  if (nodeDirty() && !confirm('切换节点将放弃当前节点的未保存修改，确定继续？')) { select.value = nodeID.value; return }
  nodeID.value = select.value
}

watch(nodeID, () => {
  nodeRequest?.abort()
  nodeCfg.value = nodeBaseline.value = null
  nodeDraft.value = nodeBase = ''
  nodeUpdated = 0
  error.value = ''; message.value = ''
  void loadNode()
})

async function saveNode() {
  if (!props.canManage || busy.value || nodeConflict.value || !nodeID.value || !nodeCfg.value) return
  busy.value = true; error.value = ''; message.value = ''
  nodeRequest?.abort()
  nodeRequest = undefined
  nodeLoading.value = false
  nodeReloading.value = false
  try {
    const res = await api.putNodeConfig({ node_id: nodeID.value, yaml: nodeDraft.value, if_updated: nodeUpdated })
    applyNode(res, true)
    message.value = res.pushed && res.online
      ? '已校验并推送给节点，等待 agent 应用（会自动重启重连）。'
      : '已保存，节点上线后自动下发。'
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) {
      await loadNode()
      error.value = '配置已在别处被修改，草稿已保留。请核对最新配置后重新编辑。'
    } else if (e instanceof ApiError && e.status === 400) error.value = `配置无效，未保存：${detail(e)}`
    else if (authError(e)) error.value = '没有管理权限或登录已失效，请重新登录。'
    else error.value = String(e)
  } finally { busy.value = false }
}

watch(tab, (t) => {
  error.value = ''; message.value = ''
  nodeRequest?.abort(); nodesRequest?.abort()
  nodeRequest = nodesRequest = undefined
  nodeLoading.value = false
  nodeReloading.value = false
  if (t === 'nodes' && props.isHub) {
    if (!nodesLoaded.value) void loadNodes()
    if (nodeID.value) void loadNode()
  }
})

usePolling(async (signal) => {
  if (busy.value || nodeReloading.value || tab.value !== 'nodes' || !nodeID.value) return
  await loadNode(false, signal)
}, 10000)

function close() {
  if (busy.value) return
  if ((localDirty() || nodeDirty()) && !confirm('仍有未保存的配置，关闭将丢弃草稿，确定继续？')) return
  emit('close')
}
defineExpose({ requestClose: close })

function beforeUnload(event: BeforeUnloadEvent) {
  if (localDirty() || nodeDirty()) { event.preventDefault(); event.returnValue = '' }
}
onMounted(() => { void loadLocal(); window.addEventListener('beforeunload', beforeUnload) })
onBeforeUnmount(() => {
  disposed = true
  localRequest?.abort(); nodeRequest?.abort(); nodesRequest?.abort()
  window.removeEventListener('beforeunload', beforeUnload)
})
</script>

<template>
  <section class="config-panel" aria-label="配置管理">
    <div class="head">
      <h3>配置管理 <small>本机与节点</small></h3>
      <button class="x" :disabled="busy" title="关闭" aria-label="关闭配置管理" @click="close">×</button>
    </div>
    <p v-if="error" role="alert" class="err">{{ error }}</p>
    <p v-if="message" role="status" class="ok">{{ message }}</p>

    <div class="tabs" aria-label="配置范围">
      <button type="button" :class="{ sel: tab === 'local' }" :aria-pressed="tab === 'local'" :disabled="busy" @click="tab = 'local'">本机配置</button>
      <button v-if="isHub" type="button" :class="{ sel: tab === 'nodes' }" :aria-pressed="tab === 'nodes'" :disabled="busy" @click="tab = 'nodes'">节点配置</button>
    </div>

    <div v-if="tab === 'local'">
      <p v-if="loading && !local" role="status">正在读取本机配置…</p>
      <template v-if="local">
        <p class="meta">文件：<code>{{ local.path }}</code> 修改于 {{ time(local.updated / 1000000) }}</p>
        <p v-if="formError" class="dim">{{ formError }}</p>
        <div class="tabs" aria-label="本机编辑方式">
          <button v-if="localForm" type="button" :class="{ sel: localEditor === 'form' }" :aria-pressed="localEditor === 'form'" :disabled="busy || localReloading" @click="switchEditor('form')">表单</button>
          <button type="button" :class="{ sel: localEditor === 'yaml' }" :aria-pressed="localEditor === 'yaml'" :disabled="busy || localReloading" @click="switchEditor('yaml')">YAML</button>
        </div>
        <AgentConfigForm v-if="localEditor === 'form' && localForm" :form="localForm" :disabled="!canManage || local.writable === false || busy || localReloading" />
        <textarea v-else v-model="localDraft" aria-label="本机配置内容" rows="18" spellcheck="false" :readonly="!canManage || local.writable === false || busy || localReloading" class="mono"></textarea>
        <p v-if="!canManage" class="dim">只有管理员可以编辑配置。</p>
        <div v-if="localConflict" class="conflict">
          <p role="status">服务器配置已更新，当前草稿已保留，保存已暂停。可先复制草稿再载入最新配置。</p>
          <details><summary>查看最新已保存配置</summary><pre>{{ local.yaml }}</pre></details>
        </div>
        <div class="row">
          <button :disabled="busy || loading" @click="reloadLocal">载入最新本机配置</button>
          <button :disabled="!canManage || local.writable === false || busy || loading || localConflict" @click="saveLocal">保存本机配置</button>
          <button v-if="local.backup" :disabled="!canManage || busy || localReloading" @click="rollback">恢复上一份</button>
          <button class="danger" :disabled="!canManage || busy" @click="restart">重启服务</button>
        </div>
      </template>
    </div>

    <div v-else>
      <div class="row">
        <label>节点
          <select :value="nodeID" @change="selectNode" aria-label="选择节点" :disabled="busy || nodeReloading">
            <option value="" disabled>选择节点</option>
            <option v-for="n in nodes" :key="n.id" :value="n.id">{{ n.hostname }}</option>
          </select>
        </label>
        <span v-if="nodesLoaded && !nodes.length" class="dim">暂无远端节点。</span>
      </div>
      <p v-if="nodeID && loading && !nodeCfg" role="status">正在读取节点配置…</p>
      <template v-if="nodeCfg">
        <p class="meta">
          {{ nodeCfg.online ? '在线' : '离线' }} · 最后上报 {{ time(nodeCfg.report_at) }}<template v-if="nodeCfg.pending"> · 待应用</template>
        </p>
        <p v-if="nodeCfg.apply?.state === 'rejected'" role="alert" class="err">节点拒绝应用：{{ nodeCfg.apply.error || '未知原因' }}</p>
        <p v-else-if="nodeCfg.apply?.state === 'deferred'" role="status" class="dim">节点已延迟应用（防抖窗口，约 5 分钟内自动重试）。</p>
        <p v-else-if="nodeCfg.apply?.state === 'applied'" role="status" class="ok">节点已应用新配置。</p>
        <p class="dim">编辑 Hub 已保存的配置；尚未下发时使用节点最近上报的配置。</p>
        <textarea v-model="nodeDraft" aria-label="节点配置内容" rows="18" spellcheck="false" :readonly="!canManage || busy || nodeReloading" class="mono"></textarea>
        <p v-if="!canManage" class="dim">只有管理员可以编辑配置。</p>
        <div v-if="nodeConflict" class="conflict">
          <p role="status">服务器配置已更新，当前草稿已保留，保存已暂停。可先复制草稿再载入最新配置。</p>
          <details><summary>查看最新已保存配置</summary><pre>{{ nodeText(nodeCfg) }}</pre></details>
        </div>
        <div class="row">
          <button :disabled="busy || loading" @click="reloadNode">载入最新节点配置</button>
          <button :disabled="!canManage || busy || loading || nodeConflict" @click="saveNode">保存并下发节点配置</button>
        </div>
      </template>
    </div>
  </section>
</template>

<style scoped>
.conflict { border:1px solid #a16207; padding:8px; margin:8px 0; border-radius:6px; }
.conflict pre { max-height:240px; overflow:auto; white-space:pre-wrap; overflow-wrap:anywhere; }
.config-panel { background:#0f172a; border:1px solid #334155; border-radius:8px; padding:12px 14px; margin-bottom:14px; color:#cbd5e1; font-size:13px; }
.head { display:flex; justify-content:space-between; align-items:center; }
h3 { margin:0; font-size:13px; color:#cbd5e1; text-transform:uppercase; letter-spacing:.05em; }
h3 small { color:#64748b; font-weight:400; margin-left:6px; text-transform:none; }
.x { background:none; border:0; color:#94a3b8; font-size:18px; cursor:pointer; }
.err { color:#fecaca; background:#7f1d1d; padding:6px 8px; border-radius:6px; margin:8px 0; overflow-wrap:anywhere; }
.ok { color:#5eead4; margin:8px 0; }
.tabs { display:flex; gap:6px; margin:10px 0; }
.tabs button { background:#0f172a; color:#cbd5e1; border:1px solid #334155; border-radius:7px; padding:6px 14px; cursor:pointer; }
.tabs button[aria-pressed=true] { color:#5eead4; border-color:#0d9488; background:#102d30; }
.meta { color:#94a3b8; margin:8px 0; }
.dim { color:#64748b; margin:8px 0; }
.row { display:flex; flex-wrap:wrap; gap:8px; align-items:center; margin:10px 0; }
label { color:#94a3b8; display:flex; gap:6px; align-items:center; }
textarea, select, button { background:#111d31; color:#e2e8f0; border:1px solid #475569; border-radius:6px; padding:8px 11px; font:inherit; }
textarea.mono { font-family:ui-monospace, monospace; width:100%; box-sizing:border-box; resize:vertical; tab-size:2; }
select { min-height:36px; min-width:180px; }
button { cursor:pointer; background:#1e293b; }
button:disabled { opacity:.5; cursor:not-allowed; }
.danger { color:#fecaca; border-color:#7f1d1d; }
code { font-size:12px; }
</style>
