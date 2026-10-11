<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import type { Terminal } from '@xterm/xterm'
import type { FitAddon } from '@xterm/addon-fit'
import { shell, type ShellTargets } from '../shell'
import { ApiError } from '../api'
import AppIcon from './AppIcon.vue'
const props = defineProps<{node: string}>()
const emit = defineEmits<{close: []; ready: []}>()
const config = shallowRef<ShellTargets | null>(null)
const targetID = ref('')
const target = computed(() => config.value?.targets.find(t => t.id === targetID.value))
const password = ref(''); const privateKey = ref(''); const passphrase = ref(''); const method = ref('password'); const consent = ref(false)
const status = ref<'idle'|'connecting'|'connected'>('idle'); const error = ref(''); const notice = ref(''); const loading = ref(false)
const terminalElement = ref<HTMLElement>(); const command = ref('')
let term: Terminal | undefined; let fit: FitAddon | undefined; let ws: WebSocket | undefined; let controller: AbortController | undefined; let observer: ResizeObserver | undefined; let epoch = 0; let fitTimer: number | undefined
const secure = computed(() => location.protocol === 'https:' || ['localhost','127.0.0.1','[::1]'].includes(location.hostname))
const canConnect = computed(() => !loading.value && status.value === 'idle' && config.value?.enabled && config.value?.secure && secure.value && !!target.value && consent.value && (target.value.kind === 'local' || (method.value === 'password' ? !!password.value : !!privateKey.value)))
function clearCredentials() { password.value = ''; privateKey.value = ''; passphrase.value = '' }
function disconnect(message = '会话已断开。') {
 epoch++; controller?.abort(); controller = undefined; loading.value = false
 const old = ws; ws = undefined; if (old?.readyState === WebSocket.OPEN) old.send(JSON.stringify({type:'close'})); old?.close()
 status.value = 'idle'; clearCredentials(); consent.value = false; command.value = ''; notice.value = message
}
function explain(e: unknown) {
 if (e instanceof ApiError) { if (e.status === 401 || e.status === 403) return '当前登录、权限或安全连接不满足要求，请重新登录或核对 HTTPS 配置。'; if (e.status === 429) return '已有终端会话或达到全局并发上限，请先断开已有会话。'; if (e.status === 404) return '目标已移除或当前机房权限不允许访问。'; if (e.status === 503) return 'WebShell 尚未启用，请按下方配置示例启用并重启。' }
 return '终端请求失败，请检查服务器连接。'
}
async function load() {
 disconnect(''); const requestEpoch = epoch; controller = new AbortController(); loading.value = true; error.value = ''; config.value = null
 try { const value = await shell.targets(controller.signal); if (requestEpoch !== epoch) return; config.value = value; targetID.value = value.targets.find(t => t.node === props.node || ((!t.node || t.node === 'local') && !props.node))?.id || value.targets[0]?.id || '' }
 catch(e) { if (requestEpoch === epoch) error.value = explain(e) }
 finally { if (requestEpoch === epoch) {loading.value = false; controller = undefined} }
}
function sendInput(data: string, confirmPaste = true) {
 if (ws?.readyState !== WebSocket.OPEN || status.value !== 'connected') return
 if (new TextEncoder().encode(data).length > 16000) { error.value = '单次输入过长，请减少到 16 KB 以内。'; return }
 if (confirmPaste && data.length > 1 && /[\r\n]/.test(data) && !window.confirm('粘贴含换行的内容可能立即执行命令，是否发送？')) return
 if (ws.bufferedAmount > 64 * 1024) { error.value = '发送队列繁忙，请稍后输入。'; return }
 ws.send(JSON.stringify({type:'input',data}))
}
function resize() { if (!term || !fit || !terminalElement.value?.clientWidth) return; fit.fit(); if (status.value === 'connected' && ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({type:'resize',cols:Math.min(400,Math.max(2,term.cols)),rows:Math.min(200,Math.max(2,term.rows))})) }
async function ensureTerminal() {
 if (term) return
 const [{Terminal},{FitAddon}] = await Promise.all([import('@xterm/xterm'),import('@xterm/addon-fit'),import('@xterm/xterm/css/xterm.css')])
 await nextTick(); if (!terminalElement.value) throw new Error('terminal closed')
 term = new Terminal({fontSize:13,scrollback:1000,cursorBlink:true,convertEol:false,theme:{background:'#07101e',foreground:'#dbeafe'},allowProposedApi:false})
 fit = new FitAddon(); term.loadAddon(fit); term.open(terminalElement.value)
 term.parser.registerOscHandler(52, () => true)
 term.onData(data => sendInput(data)); observer = new ResizeObserver(() => {clearTimeout(fitTimer);fitTimer=window.setTimeout(resize,100)});observer.observe(terminalElement.value);resize()
}
async function connect() {
 if (!canConnect.value || !target.value) return
 error.value = ''; notice.value = ''; status.value = 'connecting'; const thisEpoch = ++epoch; controller = new AbortController()
 try {
  await ensureTerminal(); if (thisEpoch !== epoch) return
  term!.reset();resize()
  const ticket = await shell.ticket(targetID.value,controller.signal); if (thisEpoch !== epoch) return
  const socket = new WebSocket(shell.url(),['monitor-shell',`shell-ticket.${ticket.ticket}`]); ws = socket; socket.binaryType='arraybuffer'
  const timer = window.setTimeout(() => { if (ws === socket && status.value === 'connecting') {disconnect('连接超时，请手动重试。')} },20000)
  socket.onopen = () => {if (thisEpoch !== epoch) {socket.close(); return};socket.send(JSON.stringify({type:'connect',cols:Math.min(400,Math.max(2,term!.cols)),rows:Math.min(200,Math.max(2,term!.rows)),...(target.value?.kind === 'ssh' ? method.value === 'password' ? {password:password.value} : {private_key:privateKey.value,passphrase:passphrase.value} : {})}));clearCredentials()}
  socket.onmessage = event => {
   if (thisEpoch !== epoch) return
   if (event.data instanceof ArrayBuffer) {term?.write(new Uint8Array(event.data));return}
   let m: {type: string; message?: string; code?: number};try{m=JSON.parse(event.data)}catch{disconnect('终端协议异常。');return}
   if (m.type === 'ready') {clearTimeout(timer);status.value='connected';resize();term?.focus()}
   else if (m.type === 'error') {error.value = translate(m.message || '');disconnect('会话已结束。')}
   else if (m.type === 'exit') disconnect(`Shell 已退出（${m.code === 0 ? '正常退出' : '异常退出'}）。`)
  }
  socket.onerror = () => {if (thisEpoch === epoch) error.value='终端连接失败，票据可能过期或权限已变化。请手动重连。'}
  socket.onclose = () => {clearTimeout(timer);if (thisEpoch === epoch) disconnect('连接已关闭；未自动重连。')}
 } catch(e) {if (thisEpoch === epoch) {error.value=explain(e);disconnect('连接未完成。')}}
 finally {if (thisEpoch === epoch) controller=undefined}
}
function translate(value: string) {if (value.includes('host key') || value.includes('authentication')) return 'SSH 认证或主机指纹校验失败，请核对凭据与管理员配置的 SHA256 指纹。'; if(value.includes('unreachable'))return 'SSH 主机不可达，请检查地址、端口和网络。';if(value.includes('permission'))return '登录已过期或机房权限变化，连接已结束。';if(value.includes('idle'))return '五分钟没有输入，连接已结束。';if(value.includes('private key'))return 'SSH 私钥格式或口令不正确。'; return '终端启动失败，请检查目标平台、Shell 路径和 SSH PTY 支持。'}
function sendCommand() {if (command.value.trim()) {sendInput(command.value+'\r', false);command.value='';term?.focus()}}
async function copySelection() {if(term?.hasSelection())try{await navigator.clipboard.writeText(term.getSelection());notice.value='已复制选中内容。'}catch{error.value='复制失败，请使用浏览器复制操作。'}}
function close() {disconnect();emit('close')}
function pageClosed() {disconnect('页面离开或网络中断，连接已关闭。')}
watch(() => props.node, () => void load())
watch(targetID, () => {if(status.value !== 'idle') disconnect('目标已切换，请重新连接。');clearCredentials();consent.value=false;term?.clear()})
watch(method,clearCredentials)
onMounted(() => {emit('ready');void load();window.addEventListener('pagehide',pageClosed);window.addEventListener('offline',pageClosed)})
onBeforeUnmount(() => {disconnect();clearTimeout(fitTimer);observer?.disconnect();term?.dispose();window.removeEventListener('pagehide',pageClosed);window.removeEventListener('offline',pageClosed)})

</script>
<template>
 <section class="shell-panel" aria-label="WebShell 主机终端">
  <div class="heading"><h2><AppIcon name="shell" />WebShell 主机终端</h2><button @click="close" aria-label="关闭 WebShell">关闭</button></div>
  <p>本机 Shell 与 SSH 交互终端。操作以目标系统账号权限执行，请先核对主机。关闭面板、切换节点或离线会断开；不自动重连。</p>
  <p v-if="error" role="alert" class="error">{{error}}</p><p v-if="notice" role="status">{{notice}}</p>
  <p v-if="!secure" class="error">主机终端需要 HTTPS；仅本机回环地址允许 HTTP。</p>
  <p v-if="config && !config.audit_enabled" class="error">当前服务器未启用审计日志，连接事件不会持久保存。</p>
  <details v-if="config && (!config.enabled || !config.targets.length)"><summary>启用 WebShell 的配置示例</summary><p>在服务端 monitor.yaml 中配置目标后重启服务；本机命令必须是绝对路径，SSH 指纹需从可信渠道核对。</p><pre>shell:
  enabled: true
  targets:
    - id: local-shell
      name: 本机终端
      kind: local
      node: local
      argv: [/bin/bash, --noprofile, --norc]
      dir: /tmp
    # SSH 目标配置参见 docs/19-host-shell.md</pre><p>本机 PTY 支持 Unix 和 Windows ConPTY；Windows 请配置 PowerShell 的绝对路径。</p></details>
  <div class="form">
   <label>连接目标<select v-model="targetID" :disabled="status !== 'idle' || loading"><option value="">请选择目标</option><option v-for="t in config?.targets || []" :key="t.id" :value="t.id">{{t.name || t.id}} · {{t.kind === 'local' ? '本机' : `${t.user}@${t.address}`}}</option></select></label>
   <p v-if="target" class="identity">{{target.kind === 'local' ? '本机服务账号 Shell' : `SSH ${target.user}@${target.address}`}} · 节点 {{target.node || 'local'}}<span v-if="target.host_key"> · {{target.host_key}}</span></p>
   <template v-if="target?.kind === 'ssh'"><label>认证方式<select v-model="method" :disabled="status !== 'idle'"><option value="password">密码</option><option value="key">私钥</option></select></label><label v-if="method === 'password'">SSH 密码<input v-model="password" type="password" autocomplete="new-password" :disabled="status !== 'idle'" /></label><template v-else><label>SSH 私钥<textarea v-model="privateKey" rows="4" autocomplete="off" spellcheck="false" :disabled="status !== 'idle'" /></label><label>私钥口令（可选）<input v-model="passphrase" type="password" autocomplete="new-password" :disabled="status !== 'idle'" /></label></template><p>凭据仅用于本次连接，不保存到配置、浏览器存储或审计日志。</p></template>
   <label class="consent"><input v-model="consent" type="checkbox" :disabled="status !== 'idle'" />已核对目标主机，允许以目标账号权限进行交互操作</label>
   <div class="actions"><button class="primary" :disabled="!canConnect" @click="connect">连接终端</button><button v-if="status !== 'idle'" @click="disconnect()">断开连接</button><button :disabled="status !== 'idle' || loading" @click="load">刷新目标</button><strong>{{status === 'connected' ? '已连接' : status === 'connecting' ? '正在连接' : '未连接'}}</strong></div>
  </div>
  <div ref="terminalElement" class="terminal" aria-label="交互终端输出"></div>
  <div class="shortcuts" aria-label="终端快捷键"><button v-for="key in [{label:'Ctrl+C',value:'\x03'},{label:'Ctrl+D',value:'\x04'},{label:'Tab',value:'\t'},{label:'Esc',value:'\x1b'},{label:'↑',value:'\x1b[A'},{label:'↓',value:'\x1b[B'}]" :key="key.label" :disabled="status !== 'connected'" @click="sendInput(key.value);term?.focus()">{{key.label}}</button><button @click="term?.clear()">清屏</button><button @click="copySelection">复制选中内容</button></div>
  <form class="command" @submit.prevent="sendCommand"><label>手机命令输入<input v-model="command" autocomplete="off" spellcheck="false" :disabled="status !== 'connected'" placeholder="输入命令后点击发送" /></label><button :disabled="status !== 'connected' || !command.trim()">发送命令</button></form>
  <p>5 分钟无输入或会话满 30 分钟自动结束。终端会执行你输入的命令；AI 方案不会自动发送到终端。断开不会撤销已执行操作，主动脱离终端的后台任务可能继续运行。</p>
 </section>
</template>
<style scoped>
.shell-panel{border:1px solid #334155;border-radius:12px;padding:18px;background:#0b1526;color:#cbd5e1;margin:16px 0;min-width:0}.heading{display:flex;align-items:center;justify-content:space-between;gap:12px}.heading h2{display:flex;align-items:center;gap:8px;margin:0;font-size:20px}p{font-size:13px;line-height:1.6;overflow-wrap:anywhere}.error{color:#fda4af}.form{display:grid;gap:10px}label{display:grid;gap:6px;font-size:14px}input,select,textarea{box-sizing:border-box;max-width:100%;width:100%;min-height:44px;font:inherit;background:#111e33;color:#e2e8f0;border:1px solid #475569;border-radius:7px;padding:8px}.consent{display:flex;align-items:center;min-height:44px}.consent input{width:20px;height:20px;min-height:20px;flex-shrink:0}.identity{margin:0;color:#67e8f9}button{min-height:44px;border:1px solid #475569;border-radius:7px;padding:8px 12px;background:#16243b;color:#e2e8f0;font:inherit;cursor:pointer}button:disabled{opacity:.45;cursor:default}.primary{border-color:#0d9488;background:#115e59}.actions,.shortcuts{display:flex;flex-wrap:wrap;gap:8px;align-items:center}.terminal{margin:16px 0 10px;height:380px;max-width:100%;overflow:hidden;background:#07101e;border:1px solid #334155;padding:8px;box-sizing:border-box;min-width:0}.command{margin-top:12px;display:flex;gap:8px;align-items:end}.command label{flex:1;min-width:0}pre{white-space:pre-wrap;overflow-wrap:anywhere;font-size:12px}summary{cursor:pointer;min-height:44px}button:focus-visible,input:focus-visible,select:focus-visible,textarea:focus-visible{outline:2px solid #5eead4;outline-offset:2px}@media(max-width:600px){.shell-panel{padding:12px}.heading h2{font-size:18px}input,select,textarea{font-size:16px}.terminal{height:320px}.command{flex-wrap:wrap}.command label{flex-basis:100%}}
</style>
