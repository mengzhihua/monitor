<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import AppIcon from './AppIcon.vue'

type Tool = { id: string; label: string; title: string; description: string; active: boolean; group?: string }
const props = defineProps<{ mobile: boolean; tools: Tool[] }>()
const groups = computed(() => [...new Set(props.tools.map(tool => tool.group || '功能'))].map(name => ({ name, tools: props.tools.filter(tool => (tool.group || '功能') === name) })))
const emit = defineEmits<{ select: [id: string]; visibility: [open: boolean] }>()
const sheet = ref<HTMLDialogElement>()
let previousOverflow: string | undefined
let trigger: HTMLElement | null = null
function closed() {
  if (previousOverflow !== undefined) document.body.style.overflow = previousOverflow
  previousOverflow = undefined
  emit('visibility', false)
  trigger?.focus({ preventScroll: true })
  trigger = null
}
function close() { sheet.value?.close(); closed() }
async function open(source?: EventTarget | null) {
  if (!props.mobile) return
  await nextTick()
  if (!sheet.value || sheet.value.open) return
  trigger = source instanceof HTMLElement ? source : document.activeElement instanceof HTMLElement ? document.activeElement : null
  previousOverflow = document.body.style.overflow
  sheet.value.showModal()
  document.body.style.overflow = 'hidden'
  emit('visibility', true)
}
function select(id: string) { close(); emit('select', id) }
watch(() => props.mobile, () => close(), { flush: 'sync' })
onBeforeUnmount(close)
defineExpose({ open, close })
</script>

<template>
  <div v-if="!mobile" class="desktop-tools">
    <div v-for="group in groups" :key="group.name" class="tool-group">
      <p class="group-title">{{ group.name }}</p>
      <button v-for="tool in group.tools" :key="tool.id" class="tool-button" :class="{ active: tool.active }" :aria-current="tool.active ? 'page' : undefined"
        :title="tool.title" @click="emit('select', tool.id)"><AppIcon :name="tool.id" :size="17" />{{ tool.label }}</button>
    </div>
  </div>
  <dialog v-else ref="sheet" class="tools-sheet" aria-label="更多功能" @close="closed" @click="event => { if (event.target === sheet) close() }">
    <div class="sheet-body">
      <div class="sheet-heading"><div><small>MONITOR</small><h2>更多功能</h2></div><button class="close" autofocus @click="close" aria-label="关闭更多功能"><AppIcon name="close" :size="16" />关闭</button></div>
      <section v-for="group in groups" :key="group.name" class="tool-group">
      <h3 class="group-title">{{ group.name }}</h3>
      <div class="tool-grid">
        <button v-for="tool in group.tools" :key="tool.id" :title="tool.title" :aria-label="tool.label" :class="{ active: tool.active }"
          @click="select(tool.id)"><span class="tool-icon" :class="'icon-' + tool.id"><AppIcon :name="tool.id" :size="22" /></span><strong>{{ tool.label }}</strong><span class="tool-description">{{ tool.description }}</span></button>
      </div>
      </section>
      <details class="homescreen"><summary>添加到手机主屏幕</summary><p>iPhone / iPad：在 Safari 的共享菜单选择“添加到主屏幕”。Android：在浏览器菜单选择“添加到主屏幕”或“安装应用”（以浏览器提供的选项为准）。</p><p>从主屏幕重新打开时可能需要再次登录。查看实时数据需要连接服务器。</p></details>
    </div>
  </dialog>
</template>

<style scoped>
.desktop-tools { display:flex; flex-direction:column; gap:18px; }
.group-title { color:#64748b; font-size:11px; font-weight:600; margin:0 0 8px; padding:0 10px; letter-spacing:.08em; }
.tool-button { display:flex; width:100%; align-items:center; gap:10px; background:transparent; color:#94a3b8; border:1px solid transparent; border-radius:8px; padding:9px 10px; font-size:13px; cursor:pointer; text-align:left; }
.tool-button:hover { background:#172337; color:#e2e8f0; }.active { background:#123333; color:#5eead4; border-color:#1e5451; }
.tools-sheet .tool-group + .tool-group { margin-top:20px; }
.tools-sheet { padding:0; color:#e2e8f0; background:#0b1526; border:1px solid #334155; border-radius:20px 20px 0 0; margin:auto auto 0; width:100%; max-width:600px; max-height:calc(100dvh - env(safe-area-inset-top) - 16px); }
.tools-sheet::backdrop { background:#020617ba; }
.sheet-body { padding:20px max(16px, env(safe-area-inset-right)) max(24px, env(safe-area-inset-bottom)) max(16px, env(safe-area-inset-left)); }
.sheet-heading { display:flex; align-items:center; justify-content:space-between; gap:12px; margin-bottom:20px; }
.sheet-heading small { font-size:10px; letter-spacing:.18em; color:#5eead4; }
h2 { margin:4px 0 0; font-size:20px; }
button { font:inherit; cursor:pointer; }
.close { display:inline-flex; align-items:center; justify-content:center; gap:5px; min-width:60px; min-height:44px; border:1px solid #475569; color:#cbd5e1; background:#172337; border-radius:10px; }
.tool-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:10px; }
.tool-grid button { min-height:76px; padding:12px; border:1px solid #283b50; border-radius:12px; background:#112136; color:#e2e8f0; display:flex; flex-direction:column; gap:6px; text-align:left; overflow-wrap:anywhere; }
.tool-grid strong { font-size:14px; }.tool-description { color:#94a3b8; font-size:12px; line-height:1.5; }
.tool-icon { display:flex; align-items:center; justify-content:center; width:36px; height:36px; margin-bottom:3px; border-radius:10px; background:#15374b; color:#67e8f9; }
.icon-rules,.icon-weights { background:#46321d; color:#fcd34d; }.icon-config,.icon-templates,.icon-contexts { background:#302950; color:#c4b5fd; }.icon-ai { background:#173f39; color:#6ee7b7; }.icon-logout { background:#3e2935; color:#fda4af; }
.homescreen { color:#94a3b8; font-size:13px; margin-top:18px; line-height:1.7; }.homescreen summary { min-height:44px; display:flex; align-items:center; cursor:pointer; color:#5eead4; }
button:focus-visible,summary:focus-visible { outline:2px solid #5eead4; outline-offset:3px; }
</style>
