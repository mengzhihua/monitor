<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AgentConfigForm from './AgentConfigForm.vue'
import { useConfigurationFeedback } from '../features/configuration/useConfigurationFeedback'
import { useLocalConfiguration } from '../features/configuration/useLocalConfiguration'
import { nodeConfigurationText as nodeText, useNodeConfiguration } from '../features/configuration/useNodeConfiguration'

const props = defineProps<{ canManage: boolean; isHub: boolean }>()
const emit = defineEmits<{ close: [] }>()
const tab = ref<'local' | 'nodes'>('local')
const feedback = useConfigurationFeedback()
const { busy, error, message } = feedback
const {
  local, draft: localDraft, editor: localEditor, form: localForm, formError,
  conflict: localConflict, dirty: localDirty, loading: localLoading, reloading: localReloading,
  switchEditor, reload: reloadLocal, save: saveLocal, rollback, restart,
} = useLocalConfiguration(() => props.canManage, feedback)
const {
  nodes, nodesLoaded, nodeID, config: nodeCfg, draft: nodeDraft, conflict: nodeConflict,
  dirty: nodeDirty, loading: nodeLoading, reloading: nodeReloading,
  selectNode, reload: reloadNode, save: saveNode,
} = useNodeConfiguration(() => tab.value === 'nodes' && props.isHub, () => props.canManage, feedback)
const loading = computed(() => tab.value === 'local' ? localLoading.value : nodeLoading.value)
const time = (value?: number) => value ? new Date(value * 1000).toLocaleString() : '—'

watch(tab, feedback.clear)
function canLeave() {
  return !busy.value && (!(localDirty() || nodeDirty()) || confirm('仍有未保存的配置，关闭将丢弃草稿，确定继续？'))
}
function close() { if (canLeave()) emit('close') }
defineExpose({ requestClose: close, canLeave })

function beforeUnload(event: BeforeUnloadEvent) {
  if (busy.value || localDirty() || nodeDirty()) { event.preventDefault(); event.returnValue = '' }
}
onMounted(() => window.addEventListener('beforeunload', beforeUnload))
onBeforeUnmount(() => window.removeEventListener('beforeunload', beforeUnload))
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
.config-panel { background:#0f172a; border:1px solid #334155; border-radius:8px; padding:12px 14px; margin-bottom:14px; color:#cbd5e1; font-size:13px; min-width:0; overflow-wrap:anywhere; }
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
