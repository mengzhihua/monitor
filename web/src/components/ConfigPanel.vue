<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import type { ManageConfig, NodeConfigFull, NodeInfo } from '../api'
import { ApiError, api } from '../api'
import { usePolling } from '../polling'

const props = defineProps<{ canManage: boolean; isHub: boolean }>()
const emit = defineEmits<{ close: [] }>()

const tab = ref<'local' | 'nodes'>('local')
const busy = ref(false)
const loading = ref(false)
const error = ref('')
const message = ref('')
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
const local = ref<ManageConfig | null>(null)
const localDraft = ref('')
let localBase = ''

async function loadLocal(force = false) {
  loading.value = true
  try {
    const cfg = await api.manageConfig()
    local.value = cfg
    // 仅在用户未编辑（草稿仍是上次加载内容）或强制重读（409）时同步编辑器
    if (force || localDraft.value === localBase) localDraft.value = cfg.yaml
    localBase = cfg.yaml
  } catch (e) {
    error.value = authError(e) ? '没有管理权限或登录已失效，请重新登录。' : `读取本机配置失败：${detail(e)}`
  } finally { loading.value = false }
}

async function saveLocal() {
  if (!props.canManage || busy.value || !local.value || local.value.writable === false) return
  busy.value = true; error.value = ''; message.value = ''
  try {
    const next = await api.putManageConfig(localDraft.value, local.value.updated)
    local.value = next
    localBase = localDraft.value
    message.value = '配置已保存并校验通过。重启服务后生效。'
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) {
      await loadLocal(true)
      error.value = '配置已在别处被修改，已重新读取，请核对后再保存。'
    } else if (e instanceof ApiError && e.status === 400) error.value = `配置无效，未保存：${detail(e)}`
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
const nodeCfg = ref<NodeConfigFull | null>(null)
const nodeDraft = ref('')
let nodeBase = ''

async function loadNodes() {
  try {
    const r = await api.nodes()
    nodes.value = r.nodes.filter((n) => !n.local)
    if (nodeID.value && !nodes.value.some((n) => n.id === nodeID.value)) nodeID.value = ''
    nodesLoaded.value = true
    error.value = ''
  } catch (e) {
    error.value = authError(e) ? '没有管理权限或登录已失效，请重新登录。' : `读取节点列表失败：${detail(e)}`
  }
}

async function fetchNode(): Promise<NodeConfigFull | null> {
  const id = nodeID.value
  if (!id) return null
  const cfg = await api.nodeConfig(id)
  if (nodeID.value !== id) return null // 已切换节点，丢弃过期响应
  return cfg
}

/**
 * 应用节点配置快照：auto 保护用户编辑（草稿未被改动时才跟随服务器）；
 * force 用于 409 后强制重读；keep 用于保存成功后仅刷新状态、不动草稿。
 */
function applyNode(cfg: NodeConfigFull, mode: 'auto' | 'force' | 'keep' = 'auto') {
  nodeCfg.value = cfg
  if (mode === 'keep') return
  const base = cfg.reported || cfg.yaml || ''
  if (mode === 'force' || nodeDraft.value === nodeBase) nodeDraft.value = base
  nodeBase = base
}

async function loadNode(mode: 'auto' | 'force' | 'keep' = 'auto') {
  if (!nodeID.value) { nodeCfg.value = null; return }
  loading.value = true
  try {
    const cfg = await fetchNode()
    if (cfg) applyNode(cfg, mode)
  } catch (e) {
    error.value = authError(e) ? '没有管理权限或登录已失效，请重新登录。' : `读取节点配置失败：${detail(e)}`
  } finally { loading.value = false }
}

watch(nodeID, () => {
  nodeCfg.value = null
  nodeDraft.value = ''
  nodeBase = ''
  error.value = ''; message.value = ''
  void loadNode()
})

async function saveNode() {
  if (!props.canManage || busy.value || !nodeID.value || !nodeCfg.value) return
  busy.value = true; error.value = ''; message.value = ''
  try {
    const res = await api.putNodeConfig({ node_id: nodeID.value, yaml: nodeDraft.value, if_updated: nodeCfg.value.updated })
    nodeCfg.value = res
    nodeBase = nodeDraft.value
    message.value = res.pushed && res.online
      ? '已校验并推送给节点，等待 agent 应用（会自动重启重连）。'
      : '已保存，节点上线后自动下发。'
    await loadNode('keep')
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) {
      await loadNode('force')
      error.value = '配置已在别处被修改，已重新读取，请核对后再保存。'
    } else if (e instanceof ApiError && e.status === 400) error.value = `配置无效，未保存：${detail(e)}`
    else if (authError(e)) error.value = '没有管理权限或登录已失效，请重新登录。'
    else error.value = String(e)
  } finally { busy.value = false }
}

watch(tab, (t) => {
  error.value = ''; message.value = ''
  if (t === 'nodes' && props.isHub && !nodesLoaded.value) void loadNodes()
})

// 节点状态 10s 轮询：仅在节点 tab 且已选中节点时刷新，不覆盖编辑中的草稿。
usePolling(async () => {
  if (busy.value || tab.value !== 'nodes' || !nodeID.value) return
  try { const cfg = await fetchNode(); if (cfg) applyNode(cfg) } catch { /* 瞬时失败，下轮重试 */ }
}, 10000)

onMounted(() => { void loadLocal() })
</script>

<template>
  <section class="config-panel" aria-label="配置管理">
    <div class="head">
      <h3>配置管理 <small>本机与节点</small></h3>
      <button class="x" :disabled="busy" title="关闭" @click="emit('close')">×</button>
    </div>
    <p v-if="error" role="alert" class="err">{{ error }}</p>
    <p v-if="message" role="status" class="ok">{{ message }}</p>

    <div class="tabs" aria-label="配置范围">
      <button type="button" :class="{ sel: tab === 'local' }" :aria-pressed="tab === 'local'" @click="tab = 'local'">本机配置</button>
      <button v-if="isHub" type="button" :class="{ sel: tab === 'nodes' }" :aria-pressed="tab === 'nodes'" @click="tab = 'nodes'">节点配置</button>
    </div>

    <div v-if="tab === 'local'">
      <p v-if="loading && !local" role="status">正在读取本机配置…</p>
      <template v-if="local">
        <p class="meta">文件：<code>{{ local.path }}</code> 修改于 {{ time(local.updated / 1000000) }}</p>
        <textarea v-model="localDraft" aria-label="本机配置内容" rows="18" spellcheck="false" :readonly="!canManage || local.writable === false" class="mono"></textarea>
        <p v-if="!canManage" class="dim">只有管理员可以编辑配置。</p>
        <div class="row">
          <button :disabled="!canManage || local.writable === false || busy || loading" @click="saveLocal">保存本机配置</button>
          <button class="danger" :disabled="!canManage || busy" @click="restart">重启服务</button>
        </div>
      </template>
    </div>

    <div v-else>
      <div class="row">
        <label>节点
          <select v-model="nodeID" aria-label="选择节点" :disabled="busy">
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
        <textarea v-model="nodeDraft" aria-label="节点配置内容" rows="18" spellcheck="false" :readonly="!canManage" class="mono"></textarea>
        <p v-if="!canManage" class="dim">只有管理员可以编辑配置。</p>
        <div class="row">
          <button :disabled="!canManage || busy || loading" @click="saveNode">保存并下发节点配置</button>
        </div>
      </template>
    </div>
  </section>
</template>

<style scoped>
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
