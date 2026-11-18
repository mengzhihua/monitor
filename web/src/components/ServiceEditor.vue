<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ApiError, get, send } from '../api'
interface Service { name: string; alarms?: string[]; children?: string[]; status_rules?: unknown[] }
interface Snapshot { revision: string; services: Service[] }
const emit = defineEmits<{ saved: [] }>()
const base = ref<Snapshot | null>(null), draft = ref<Service[]>([]), preview = ref<Snapshot | null>(null)
const opened = ref(false), busy = ref(false), error = ref(''), message = ref('')
let disposed = false
const pretty = (v: unknown) => JSON.stringify(v, null, 2)
const dirty = computed(() => opened.value && base.value != null && pretty(draft.value) !== pretty(base.value.services))
const removed = computed(() => base.value?.services.filter(s => !draft.value.some(d => d.name === s.name)).map(s => s.name) || [])
watch(draft, () => { preview.value = null; message.value = '' }, { deep: true, flush: 'sync' })
async function load(keep = false) {
  busy.value = true; error.value = ''; preview.value = null
  try {
    const snapshot = await get<Snapshot>('/api/v1/services/config')
    if (disposed) return
    base.value = snapshot
    if (!keep) draft.value = structuredClone(snapshot.services)
    opened.value = true
    message.value = keep ? '已读取最新版本并保留草稿。请核对当前配置与删除范围，再重新预览。' : ''
  } catch (e) { if (!disposed) error.value = String(e) }
  finally { if (!disposed) busy.value = false }
}
async function change(save: boolean) {
  if (!base.value || (save && !preview.value)) return
  busy.value = true; error.value = ''; message.value = ''
  try {
    const result = await send<Snapshot>(save ? 'PUT' : 'POST', '/api/v1/services/config' + (save ? '' : '/preview'), { revision: base.value.revision, services: save ? preview.value!.services : draft.value })
    if (disposed) return
    if (save) { base.value = result; draft.value = structuredClone(result.services); preview.value = null; message.value = '服务配置已保存并生效。'; emit('saved') }
    else preview.value = result
  } catch (e) {
    if (!disposed) { preview.value = null; error.value = e instanceof ApiError && e.status === 409 ? '配置已被其他人修改，草稿已保留。请读取最新版本，核对后重新预览。' : String(e) }
  } finally { if (!disposed) busy.value = false }
}
function canLeave() { return !busy.value && (!dirty.value || window.confirm('服务配置尚未保存，离开将丢弃草稿。是否继续？')) }
function beforeUnload(e: BeforeUnloadEvent) { if (dirty.value || busy.value) { e.preventDefault(); e.returnValue = '' } }
window.addEventListener('beforeunload', beforeUnload)
onBeforeUnmount(() => { disposed = true; window.removeEventListener('beforeunload', beforeUnload) })
defineExpose({ canLeave })
const refs = (value: string) => value.split(/[,，\n]/).map(s => s.trim()).filter(Boolean)
</script>
<template>
  <section class="service-editor" aria-label="服务配置编辑">
    <button v-if="!opened" :disabled="busy" @click="load()">配置服务树</button>
    <template v-else>
      <h2>配置服务树</h2>
      <p>修改本机服务目录，保存后立即生效并在重启后保留。告警名称匹配所有同名图表实例；依赖填写服务名称，以逗号分隔。</p>
      <fieldset :disabled="busy">
        <article v-for="(service, index) in draft" :key="index">
          <label>服务名称<input v-model="service.name" :aria-label="`服务名称 ${index + 1}`" maxlength="128" /></label>
          <label>关联告警<input :value="(service.alarms || []).join(', ')" :aria-label="`关联告警 ${index + 1}`" @change="service.alarms = refs(($event.target as HTMLInputElement).value)" /></label>
          <label>依赖服务<input :value="(service.children || []).join(', ')" :aria-label="`依赖服务 ${index + 1}`" @change="service.children = refs(($event.target as HTMLInputElement).value)" /></label>
          <details v-if="service.status_rules?.length"><summary>已有状态规则（保留）</summary><pre>{{ pretty(service.status_rules) }}</pre></details>
          <button @click="draft.splice(index, 1)">移除 {{ service.name || '未命名服务' }}</button>
        </article>
        <button :disabled="draft.length >= 64" @click="draft.push({ name: '', alarms: [], children: [] })">添加服务</button>
        <button @click="load(true)">读取最新版本并保留草稿</button>
        <button @click="change(false)">校验并预览</button>
        <button :disabled="!preview || !dirty" @click="change(true)">保存服务配置</button>
        <button @click="() => { if (canLeave()) opened = false }">关闭编辑</button>
      </fieldset>
      <p v-if="removed.length">将移除：{{ removed.join('、') }}</p>
      <details v-if="base"><summary>当前服务器配置（用于核对冲突）</summary><pre>{{ pretty(base.services) }}</pre></details>
      <div v-if="preview" class="preview" role="status"><strong>校验通过，将保存 {{ preview.services.length }} 个服务。</strong><p>请核对名称、关联告警、依赖和移除范围。未匹配的告警在运行时显示未知。</p><pre>{{ pretty(preview.services) }}</pre></div>
    </template>
    <p v-if="error" role="alert">{{ error }}</p><p v-if="message" role="status">{{ message }}</p>
  </section>
</template>
<style scoped>
.service-editor { margin:16px 0; padding:16px; border:1px solid #475569; border-radius:12px; }
fieldset { border:0; padding:0; min-width:0; }
article { display:flex; flex-wrap:wrap; gap:12px; padding:12px 0; border-bottom:1px solid #334155; margin-bottom:12px; }
label { display:flex; flex-direction:column; gap:6px; flex:1 1 200px; min-width:0; }
input, button { font:inherit; color:inherit; background:#0f172a; border:1px solid #475569; border-radius:6px; padding:8px; min-width:0; }
button { margin:4px; cursor:pointer; } button:disabled { opacity:.5; cursor:default; }
pre { white-space:pre-wrap; overflow-wrap:anywhere; max-height:300px; overflow:auto; }
.preview { border-left:3px solid #5eead4; padding:12px; margin-top:12px; }
</style>
