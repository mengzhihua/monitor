<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { Info } from '../../api/monitoring/types'
import type { NodeInfo } from '../../api/hub/types'
import AppIcon from '../../components/AppIcon.vue'
const props = defineProps<{ info: Info | null; currentNode: NodeInfo | null; nodes: NodeInfo[]; node: string; connected: boolean; warning: number; critical: number; authenticated: boolean; mobile: boolean }>()
const emit = defineEmits<{ node: [id: string]; alarms: []; logout: [] }>()
// Preserve per-node build visibility when moving the old header into its module.
function shortVersion(version: string) {
  if (!version) return '—'
  const plus = version.indexOf('+')
  return plus > 0 ? version.slice(0, plus + 8) : version
}
const hubVersion = computed(() => props.nodes.find(node => node.local)?.version ?? '')
function versionMismatch(node: NodeInfo) {
  return !!hubVersion.value && !node.local && !node.replica && !node.peer && !!node.version && node.version !== hubVersion.value
}
const selectedVersion = computed(() => props.currentNode?.version || props.info?.version || '')
const menuOpen = ref(false)
const menu = ref<HTMLElement>()
const trigger = ref<HTMLButtonElement>()
const buildLabel = computed(() => props.info ? `${props.info.version}${props.info.commit ? ' · ' + props.info.commit.slice(0, 12) + (props.info.dirty ? '+' : '') : ''}` : '')
const buildTitle = computed(() => {
  const i = props.info
  return i ? [`版本 ${i.version}`, i.commit && `提交 ${i.commit}`, i.build_time && `构建 ${i.build_time}`, i.dirty ? '构建时工作区有未提交修改' : '构建时工作区干净'].filter(Boolean).join('\n') : ''
})
function close(event: Event) { if (!menu.value?.contains(event.target as Node)) menuOpen.value = false }
function escape(event: KeyboardEvent) { if (event.key === 'Escape' && menuOpen.value) { menuOpen.value = false; trigger.value?.focus() } }
function pick(node: NodeInfo) { menuOpen.value = false; emit('node', node.local ? '' : node.id); trigger.value?.focus() }
onMounted(() => { document.addEventListener('click', close); document.addEventListener('keydown', escape) })
onBeforeUnmount(() => { document.removeEventListener('click', close); document.removeEventListener('keydown', escape) })
</script>

<template>
  <header class="workspace-header">
    <a href="#workspace-main" class="skip-link">跳转到工作区</a>
    <div class="brand"><span class="logo">◉</span> Monitor <span class="edition">监控工作台</span></div>
    <div v-if="info" class="identity">
      <div v-if="info.mode === 'hub'" ref="menu" class="node-select">
        <button ref="trigger" class="node-select-btn" title="节点" aria-haspopup="listbox" :aria-expanded="menuOpen" @click="menuOpen = !menuOpen"><AppIcon name="server" :size="16" />{{ currentNode?.hostname || info.host.hostname }} <span class="node-version" :title="selectedVersion">{{ shortVersion(selectedVersion) }}</span><span>⌄</span></button>
        <div v-if="menuOpen && mobile" class="node-backdrop" aria-hidden="true" @click="menuOpen = false" />
        <div v-if="menuOpen" class="node-menu" role="listbox" aria-label="节点">
          <button v-for="item in nodes" :key="item.id" role="option" :aria-selected="item.local ? !node : item.id === node" class="node-item" @click="pick(item)">{{ item.local ? '◆' : item.status === 'live' ? '●' : '○' }} {{ item.hostname }}{{ item.local ? ' (hub)' : item.replica ? ' (replica)' : item.peer ? ' (peer)' : '' }}<span :class="['node-version', { mismatch: versionMismatch(item) }]" :title="versionMismatch(item) ? `与 hub 版本不一致（hub: ${hubVersion}）；${item.version}` : item.version">{{ shortVersion(item.version) }}</span></button>
        </div>
      </div>
      <span v-else class="host" :title="info.host.hostname + ' · ' + info.version"><AppIcon name="server" :size="16" />{{ info.host.hostname }} <span class="node-version">{{ shortVersion(info.version) }}</span></span>
      <span class="platform">{{ currentNode?.os || info.host.os }}/{{ currentNode?.arch || info.host.arch }}</span>
      <span v-if="currentNode && currentNode.status !== 'live'" class="stale">{{ currentNode.status === 'stale' ? '数据过期' : '节点离线' }}</span>
    </div>
    <div v-if="info" class="header-status meta">
      <span class="build desktop-detail" :title="buildTitle">{{ buildLabel }}</span>
      <span v-if="info.stream" :class="['dot', info.stream.connected ? 'on' : 'off']" :title="info.stream.connected ? '已上报到 ' + info.stream.destination : '未连接 Hub' + (info.stream.last_error ? ': ' + info.stream.last_error : '')">⇡</span>
      <span :class="['dot', connected ? 'on' : 'off']" :title="connected ? 'live' : 'reconnecting'">● <span>{{ connected ? '实时连接' : '正在重连' }}</span></span>
      <button class="alarms-btn" :class="{ crit: critical, warn: !critical && warning }" title="告警" @click="emit('alarms')"><AppIcon name="alarms" :size="17" /><b>{{ critical + warning }}</b></button>
      <button v-if="authenticated && !mobile" class="logout" @click="emit('logout')">退出登录</button>
    </div>
  </header>
</template>

<style scoped>
.workspace-header { display:flex; align-items:center; gap:20px; padding:12px 20px; min-height:64px; border-bottom:1px solid #1e293b; background:#0b1120; position:sticky; top:0; z-index:30; }
.brand { font-size:18px; font-weight:700; display:flex; align-items:center; gap:8px; flex-shrink:0; }.logo { color:#2dd4bf; }.edition { font-size:11px; color:#64748b; font-weight:400; margin-left:4px; }
.identity,.header-status,.host { display:flex; align-items:center; gap:10px; min-width:0; }.identity { flex:1; }.host { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; color:#cbd5e1; font-size:13px; }.platform,.build { font-size:11px; color:#64748b; }.header-status { flex-shrink:0; font-size:12px; }
.dot { white-space:nowrap; }.dot.on { color:#5eead4; }.dot.off,.stale { color:#fbbf24; }.dot span { color:#94a3b8; }
button { display:inline-flex; align-items:center; gap:8px; cursor:pointer; background:#111e31; border:1px solid #334155; border-radius:8px; color:#cbd5e1; font:inherit; padding:7px 10px; }button:focus-visible { outline:2px solid #5eead4; }
.alarms-btn.crit { color:#fda4af; border-color:#9f3751; }.alarms-btn.warn { color:#fde68a; border-color:#956f24; }.logout { font-size:12px; }
.node-select { position:relative; min-width:0; }.node-select-btn { max-width:280px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font-size:13px; }.node-backdrop { display:none; }
.node-menu { position:absolute; top:calc(100% + 6px); left:0; z-index:50; background:#0f172a; border:1px solid #334155; border-radius:10px; padding:6px; width:300px; max-width:calc(100vw - 30px); max-height:340px; overflow:auto; box-shadow:0 12px 32px #0008; }.node-item { width:100%; text-align:left; border:0; background:transparent; overflow-wrap:anywhere; }.node-item:hover,.node-item[aria-selected=true] { color:#5eead4; background:#123333; }
.node-version { color:#94a3b8; font-size:11px; flex-shrink:0; }.node-version.mismatch { color:#fbbf24; }.node-select-btn { gap:6px; }.node-item { flex-wrap:wrap; }
.skip-link { position:fixed; top:-100px; left:20px; z-index:100; padding:10px; color:#fff; background:#134e4a; }.skip-link:focus { top:8px; }
@media(max-width:1100px) { .edition,.platform,.build { display:none; } }
@media(max-width:760px) { .workspace-header { position:relative; z-index:50; flex-wrap:wrap; gap:8px 12px; padding:12px; padding-top:max(12px,env(safe-area-inset-top)); }.brand { flex-basis:100%; font-size:17px; }.identity { flex:1 1 120px; font-size:12px; }.host { max-width:100%; }.header-status { gap:8px; }.dot span { display:none; }.node-select-btn { max-width:min(250px,60vw); min-height:44px; }.node-backdrop { display:block; position:fixed; inset:0; z-index:54; background:#02061780; }.node-select .node-menu { position:fixed; left:max(12px,env(safe-area-inset-left)); right:max(12px,env(safe-area-inset-right)); top:auto; bottom:calc(78px + env(safe-area-inset-bottom) + 8px); width:auto; max-height:min(340px,45dvh); z-index:55; }.stale { display:none; }.node-item { min-height:44px; } }
</style>
