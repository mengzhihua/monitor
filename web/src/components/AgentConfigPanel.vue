<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ApiError, api } from '../api'

const emit = defineEmits<{ close: [] }>()
const path = ref('')
const yaml = ref('')
const writable = ref(false)
const error = ref('')
const notice = ref('')
const busy = ref(false)

async function load() {
  error.value = ''
  try {
    const cfg = await api.agentConfig()
    path.value = cfg.path
    yaml.value = cfg.yaml
    writable.value = cfg.writable
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
    const cfg = await api.saveAgentConfig(yaml.value)
    yaml.value = cfg.yaml
    notice.value = '已写入配置文件。重启本机服务后才会生效。'
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : '保存失败，配置文件未替换'
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
    <p class="hint">这里修改的是当前登录的这台 monitord 的配置文件。查看远端节点时不会改那台 Agent。保存后需要重启才会加载。</p>
    <div v-if="error" class="err" role="alert">{{ error }}</div>
    <p v-if="notice" class="ok" role="status">{{ notice }}</p>
    <textarea v-model="yaml" :readonly="!writable" aria-label="本机配置 YAML" spellcheck="false" />
    <div class="row">
      <button type="button" :disabled="!writable || busy" @click="save">保存配置</button>
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
.ok { color: #86efac; }
.err { color: #fecaca; background: #7f1d1d; padding: 6px 8px; border-radius: 6px; margin-bottom: 8px; overflow-wrap: anywhere; }
textarea { width: 100%; min-height: 240px; box-sizing: border-box; background: #020617; color: #e2e8f0; border: 1px solid #334155; border-radius: 6px; padding: 8px; font: 12px/1.45 ui-monospace, monospace; }
.row { display: flex; gap: 8px; margin-top: 8px; }
button { background: #1e293b; color: #e2e8f0; border: 1px solid #334155; border-radius: 6px; padding: 4px 8px; font-size: 13px; cursor: pointer; }
button:disabled { opacity: .5; cursor: not-allowed; }
.danger { color: #fecaca; border-color: #7f1d1d; }
</style>
