<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ApiError, api, type AgentConfigFile, type AgentVisual } from '../api'
import AgentConfigForm from './AgentConfigForm.vue'

const emit = defineEmits<{ close: [] }>()
const path = ref('')
const yaml = ref('')
const writable = ref(false)
const restartRequired = ref(false)
const backup = ref(false)
const editor = ref<'form' | 'yaml'>('form')
const form = ref<AgentVisual | null>(null)
const formError = ref('')
const error = ref('')
const notice = ref('')
const busy = ref(false)

function apply(cfg: AgentConfigFile) {
  path.value = cfg.path
  yaml.value = cfg.yaml
  writable.value = cfg.writable
  restartRequired.value = cfg.restart_required
  backup.value = cfg.backup
  form.value = cfg.form ? structuredClone(cfg.form) : null
  formError.value = cfg.form_error || ''
}

async function load() {
  error.value = ''
  try {
    apply(await api.agentConfig())
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : '读取配置失败'
  }
}

async function save() {
  if (!writable.value || busy.value) return
  busy.value = true
  error.value = ''
  notice.value = ''
  try {
    const cfg = editor.value === 'form' && form.value ? await api.saveAgentForm(form.value) : await api.saveAgentConfig(yaml.value)
    apply(cfg)
    notice.value = cfg.restart_required ? '已写入配置文件。重启本机服务后才会生效。' : '配置文件与当前进程一致。'
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : '保存失败，配置文件未替换'
  } finally {
    busy.value = false
  }
}

async function rollback() {
  if (!backup.value || busy.value) return
  if (!window.confirm('用上一份配置覆盖当前文件？已经写坏、无法加载的文件也可以这样找回。')) return
  busy.value = true
  error.value = ''
  notice.value = ''
  try {
    apply(await api.rollbackAgentConfig())
    notice.value = '已恢复上一份配置。与当前进程不一致时仍需重启。'
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : '恢复失败，当前文件未替换'
  } finally {
    busy.value = false
  }
}

async function restart() {
  if (busy.value) return
  if (!window.confirm('重启会中断当前连接，并让已保存的配置生效。继续重启本机服务？')) return
  busy.value = true
  error.value = ''
  notice.value = ''
  try {
    await api.restartAgent()
    notice.value = '服务正在重启。请稍等几秒后刷新页面。'
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : '重启请求失败'
    busy.value = false
  }
}

onMounted(() => { void load() })
</script>

<template>
  <div class="panel">
    <div class="head">
      <h3>本机配置 <small>{{ path || '未绑定配置文件' }}</small></h3>
      <button class="x" type="button" @click="emit('close')" title="关闭">×</button>
    </div>
    <p class="hint">这里修改的是当前登录的这台 monitord 的配置文件。查看远端节点时不会改那台 Agent。保存后需要重启才会加载。写入前会保留上一份文件；若磁盘上的配置无法加载，启动时会自动退回那一份。</p>
    <p v-if="restartRequired" class="pending" role="status">磁盘上的配置与当前进程不一致，重启后才会生效。</p>
    <div v-if="error" class="err" role="alert">{{ error }}</div>
    <p v-if="notice" class="ok" role="status">{{ notice }}</p>
    <div class="tabs" role="tablist" aria-label="配置编辑方式">
      <button type="button" :class="{ on: editor === 'form' }" @click="editor = 'form'">表单</button>
      <button type="button" :class="{ on: editor === 'yaml' }" @click="editor = 'yaml'">YAML</button>
    </div>
    <p v-if="editor === 'form' && formError" class="err" role="alert">{{ formError }}</p>
    <AgentConfigForm v-else-if="editor === 'form' && form" :form="form" :disabled="!writable" />
    <textarea v-else v-model="yaml" :readonly="!writable" aria-label="本机配置 YAML" spellcheck="false" />
    <div class="row">
      <button type="button" :disabled="!writable || busy || (editor === 'form' && !form)" @click="save">保存配置</button>
      <button type="button" :disabled="!backup || busy" @click="rollback">恢复上一份</button>
      <button type="button" class="danger" :disabled="busy" @click="restart">重启服务</button>
    </div>
  </div>
</template>

<style scoped>
.panel { background: #0f172a; border: 1px solid #334155; border-radius: 8px; padding: 12px 14px; margin-bottom: 14px; font-size: 13px; }
.head { display: flex; justify-content: space-between; align-items: center; gap: 8px; }
h3 { margin: 0 0 8px; font-size: 13px; color: #cbd5e1; text-transform: uppercase; letter-spacing: .05em; }
h3 small { color: #64748b; font-weight: 400; margin-left: 6px; text-transform: none; letter-spacing: 0; }
.x { background: none; border: 0; color: #94a3b8; font-size: 18px; cursor: pointer; }
.hint, .ok { color: #64748b; margin: 0 0 8px; font-size: 12px; }
.ok, .pending { color: #86efac; }
.pending { color: #fcd34d; }
.err { color: #fecaca; background: #7f1d1d; padding: 6px 8px; border-radius: 6px; margin-bottom: 8px; overflow-wrap: anywhere; }
textarea { width: 100%; min-height: 240px; box-sizing: border-box; background: #020617; color: #e2e8f0; border: 1px solid #334155; border-radius: 6px; padding: 8px; font: 12px/1.45 ui-monospace, monospace; }
.tabs { display: flex; gap: 6px; margin-bottom: 8px; }
.tabs button.on { color: #5eead4; border-color: #0d9488; }
.row { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 8px; }
button { background: #1e293b; color: #e2e8f0; border: 1px solid #334155; border-radius: 6px; padding: 4px 8px; font-size: 13px; cursor: pointer; }
button:disabled { opacity: .5; cursor: not-allowed; }
.danger { color: #fecaca; border-color: #7f1d1d; }
</style>
