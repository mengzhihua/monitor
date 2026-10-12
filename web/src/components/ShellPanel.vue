<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import type { Terminal } from '@xterm/xterm'
import type { FitAddon } from '@xterm/addon-fit'
import { shell, type ShellSession, type ShellSessions, type ShellTargets } from '../shell'
import { ApiError } from '../api'
import AppIcon from './AppIcon.vue'

const props = defineProps<{node: string}>()
const emit = defineEmits<{close: []; ready: []}>()
const config = shallowRef<ShellTargets | null>(null)
const targetID = ref('')
const target = computed(() => config.value?.targets.find(t => t.id === targetID.value))
const password = ref(''); const privateKey = ref(''); const passphrase = ref(''); const method = ref('password'); const consent = ref(false)
const status = ref<'idle'|'connecting'|'connected'>('idle'); const error = ref(''); const notice = ref(''); const loading = ref(false)
const sessions = shallowRef<ShellSessions | null>(null)
const sessionsLoading = ref(false); const sessionsError = ref(''); const terminating = ref(''); const currentSessionID = ref('')
const terminalElement = ref<HTMLElement>(); const commandInput = ref<HTMLTextAreaElement>(); const command = ref('')
let term: Terminal | undefined; let fit: FitAddon | undefined; let ws: WebSocket | undefined
let controller: AbortController | undefined; let sessionsController: AbortController | undefined; let terminationController: AbortController | undefined
let observer: ResizeObserver | undefined; let epoch = 0; let sessionEpoch = 0; let fitTimer: number | undefined; let connectTimer: number | undefined
let warningTimer: number | undefined; let ackFrame = 0; let disposed = false
let lastSentCols = 0; let lastSentRows = 0; let connectedAt = 0; let lastLocalInputAt = 0
let sessionRefreshTimers: number[] = []; let viewportCleanup: (() => void) | undefined
const encoder = new TextEncoder()
// Bound xterm's asynchronous write queue even when an older server does not negotiate ACKs.
const maxPendingOutput = 256 * 1024
const secure = computed(() => location.protocol === 'https:' || ['localhost','127.0.0.1','[::1]'].includes(location.hostname))
const canConnect = computed(() => !loading.value && status.value === 'idle' && config.value?.enabled && config.value?.secure && secure.value && !!target.value && consent.value && (target.value.kind === 'local' || (method.value === 'password' ? !!password.value : !!privateKey.value)))
const coarsePointer = () => window.matchMedia('(pointer: coarse)').matches
function clearCredentials() { password.value = ''; privateKey.value = ''; passphrase.value = '' }
function clearWarnings() { clearInterval(warningTimer); warningTimer = undefined }
function clearSessionRefreshTimers() { for (const id of sessionRefreshTimers) clearTimeout(id); sessionRefreshTimers = [] }
function scheduleSessionRefresh(delays: number[]) {
 clearSessionRefreshTimers()
 sessionRefreshTimers = delays.map(delay => window.setTimeout(() => { if (!disposed) void refreshSessions() }, delay))
}
function startWarnings() {
 clearWarnings()
 connectedAt = Date.now(); lastLocalInputAt = connectedAt
 warningTimer = window.setInterval(() => {
  if (status.value !== 'connected' || !config.value) return
  const idleLimit = Math.max(30, config.value.idle_seconds || 300) * 1000
  const maxLimit = Math.max(60, config.value.max_seconds || 1800) * 1000
  const idleLeft = idleLimit - (Date.now() - lastLocalInputAt)
  const maxLeft = maxLimit - (Date.now() - connectedAt)
  if (maxLeft > 0 && maxLeft <= 120000) notice.value = `会话将在约 ${Math.max(1, Math.ceil(maxLeft / 60000))} 分钟后达到时限，请及时完成操作；到期后需手动重连。`
  else if (idleLeft > 0 && idleLeft <= 60000) notice.value = `约 ${Math.max(1, Math.ceil(idleLeft / 1000))} 秒无输入将自动断开；在终端输入任意内容可续期。`
 }, 1000)
}
function disconnect(message = '会话已断开。') {
 epoch++; controller?.abort(); controller = undefined; loading.value = false
 clearTimeout(connectTimer); connectTimer = undefined; clearWarnings(); if (ackFrame) { cancelAnimationFrame(ackFrame); ackFrame = 0 }
 const old = ws; ws = undefined
 // Socket send can throw if the network disappears between its readyState check and send.
 try { if (old?.readyState === WebSocket.OPEN) old.send(JSON.stringify({type:'close'})) } catch { /* close still releases the socket */ }
 old?.close()
 status.value = 'idle'; currentSessionID.value = ''; clearCredentials(); consent.value = false; notice.value = message
 lastSentCols = 0; lastSentRows = 0
 if (message) scheduleSessionRefresh([0])
}
function explain(e: unknown) {
 if (e instanceof ApiError) { if (e.status === 401 || e.status === 403) return '当前登录、权限或安全连接不满足要求，请重新登录或核对 HTTPS 配置。'; if (e.status === 429) return '已有终端会话或达到全局并发上限，可在“我的会话”中检查并终止遗留连接。'; if (e.status === 404) return '目标或会话已移除，或当前机房权限不允许访问。'; if (e.status === 503) return 'WebShell 尚未启用，请按下方配置示例启用并重启。' }
 return '终端请求失败，请检查服务器连接。'
}
async function refreshSessions() {
 const request = ++sessionEpoch; sessionsController?.abort(); const abort = new AbortController(); sessionsController = abort
 sessionsLoading.value = true; sessionsError.value = ''
 try { const result = await shell.sessions(abort.signal); if (!disposed && request === sessionEpoch) sessions.value = result }
 catch(e) { if (!disposed && request === sessionEpoch && !abort.signal.aborted) sessionsError.value = explain(e) }
 finally { if (request === sessionEpoch) { sessionsLoading.value = false; sessionsController = undefined } }
}
async function terminateSession(session: ShellSession) {
 if (terminating.value || !window.confirm(`终止 ${session.target.name || session.target.id} 的会话？正在该终端内运行的前台任务会中断，已执行的操作不会撤销。`)) return
 const abort = new AbortController(); terminationController = abort; terminating.value = session.id; sessionsError.value = ''
 try {
  const result = await shell.terminate(session.id,abort.signal)
  if (disposed || abort.signal.aborted) return
  if (session.id === currentSessionID.value) disconnect('当前会话已由你终止。')
  if (sessions.value) sessions.value = {...sessions.value, sessions: result?.closing ? sessions.value.sessions.map(s => s.id === session.id ? {...s,state:'closing'} : s) : sessions.value.sessions.filter(s => s.id !== session.id)}
  if (result?.closing) {
   notice.value = '会话正在清理，稍后会自动刷新列表；也可手动刷新后再连接。'
   scheduleSessionRefresh([1000, 3000])
  } else {
   notice.value = '会话已终止，可以重新连接。'
   scheduleSessionRefresh([0])
  }
 } catch(e) {
  if (!disposed && !abort.signal.aborted) { sessionsError.value = explain(e); if (e instanceof ApiError && e.status === 404) void refreshSessions() }
 } finally { if (terminationController === abort) { terminating.value = ''; terminationController = undefined } }
}
async function load() {
 disconnect(''); const requestEpoch = epoch; controller = new AbortController(); loading.value = true; error.value = ''; config.value = null
 void refreshSessions()
 try { const value = await shell.targets(controller.signal); if (requestEpoch !== epoch) return; config.value = value; targetID.value = value.targets.find(t => t.node === props.node || ((!t.node || t.node === 'local') && !props.node))?.id || value.targets[0]?.id || '' }
 catch(e) { if (requestEpoch === epoch) error.value = explain(e) }
 finally { if (requestEpoch === epoch) {loading.value = false; controller = undefined} }
}
function sendInput(data: string, confirmPaste = true): boolean {
 if (ws?.readyState !== WebSocket.OPEN || status.value !== 'connected') { error.value = '连接尚未就绪，输入内容已保留。'; return false }
 if (encoder.encode(data).length > 16000) { error.value = '单次输入过长，请减少到 16 KB 以内；输入内容已保留。'; return false }
 if (confirmPaste && data.length > 1 && /[\r\n]/.test(data) && !window.confirm('粘贴含换行的内容可能立即执行命令，是否发送？')) return false
 if (ws.bufferedAmount > 64 * 1024) { error.value = '发送队列繁忙，请稍后重试；输入内容已保留。'; return false }
 try { ws.send(JSON.stringify({type:'input',data})); error.value = ''; lastLocalInputAt = Date.now(); return true }
 catch { error.value = '发送失败，输入内容已保留，请检查连接状态。'; return false }
}
function resize() {
 if (!term || !fit || !terminalElement.value?.clientWidth) return
 fit.fit()
 const cols = Math.min(400, Math.max(2, term.cols)); const rows = Math.min(200, Math.max(2, term.rows))
 if (status.value !== 'connected' || ws?.readyState !== WebSocket.OPEN) return
 if (cols === lastSentCols && rows === lastSentRows) return
 lastSentCols = cols; lastSentRows = rows
 try { ws.send(JSON.stringify({type:'resize',cols,rows})) } catch { disconnect('网络连接中断，请手动重连。') }
}
function disposeTerminal() {
 clearTimeout(fitTimer); observer?.disconnect(); observer = undefined; viewportCleanup?.(); viewportCleanup = undefined
 term?.dispose(); term = undefined; fit = undefined
}
function bindViewport() {
 const vv = window.visualViewport
 if (!vv || !coarsePointer()) return
 const onVV = () => { clearTimeout(fitTimer); fitTimer = window.setTimeout(resize, coarsePointer() ? 160 : 100) }
 vv.addEventListener('resize', onVV); vv.addEventListener('scroll', onVV)
 viewportCleanup = () => { vv.removeEventListener('resize', onVV); vv.removeEventListener('scroll', onVV) }
}
async function ensureTerminal(requestEpoch: number) {
 const [{Terminal},{FitAddon}] = await Promise.all([import('@xterm/xterm'),import('@xterm/addon-fit'),import('@xterm/xterm/css/xterm.css')])
 await nextTick(); if (disposed || requestEpoch !== epoch || !terminalElement.value) return
 // A fresh instance prevents buffered output from a closed connection entering its successor.
 disposeTerminal()
 term = new Terminal({fontSize:13,scrollback:1000,cursorBlink:true,convertEol:false,theme:{background:'#07101e',foreground:'#dbeafe'},allowProposedApi:false})
 fit = new FitAddon(); term.loadAddon(fit); term.open(terminalElement.value)
 term.parser.registerOscHandler(52, () => true)
 const terminal = term
 term.onData(data => {if (requestEpoch === epoch && term === terminal) sendInput(data)})
 observer = new ResizeObserver(() => {clearTimeout(fitTimer);fitTimer=window.setTimeout(resize, coarsePointer() ? 160 : 100)})
 observer.observe(terminalElement.value); bindViewport(); resize()
}
function focusTerminal() { if (!coarsePointer()) term?.focus() }
function closeReason(reason: string): string {
 const idleMin = Math.max(1, Math.round((config.value?.idle_seconds || 300) / 60))
 const maxMin = Math.max(1, Math.round((config.value?.max_seconds || 1800) / 60))
 const reasons: Record<string,string> = {
  session_revoked:'会话已从其他页面终止。勾选确认后可手动重连。',
  max_duration:`会话已达到 ${maxMin} 分钟时限。目标仍保留，勾选确认后可手动重连。`,
  output_limit:'会话输出达到 32 MB 上限，连接已结束。勾选确认后可手动重连。',
  server_shutdown:'服务器正在关闭或重启，连接已结束。',
  output_stalled:'浏览器长时间未完成输出渲染，连接已结束，请缩小命令输出后手动重连。',
  input_overflow:'终端输入队列已满，连接已结束，请减少粘贴内容后手动重连。',
 }
 return reasons[reason] || '连接已关闭；未自动重连。勾选确认后可再次连接。'
}
function queueAck(socket: WebSocket, getBytes: () => number, isCurrent: () => boolean) {
 if (ackFrame) return
 ackFrame = requestAnimationFrame(() => {
  ackFrame = 0
  if (!isCurrent() || socket.readyState !== WebSocket.OPEN) return
  try { socket.send(JSON.stringify({type:'ack',bytes:getBytes()})) }
  catch { disconnect('输出确认发送失败，请手动重连。') }
 })
}
async function connect() {
 if (!canConnect.value || !target.value) return
 error.value = ''; notice.value = ''; status.value = 'connecting'; const thisEpoch = ++epoch; controller = new AbortController()
 try {
  await ensureTerminal(thisEpoch); if (thisEpoch !== epoch || !term) return
  const ticket = await shell.ticket(targetID.value,controller.signal); if (thisEpoch !== epoch) return
  const socket = new WebSocket(shell.url(),['monitor-shell',`shell-ticket.${ticket.ticket}`]); ws = socket; socket.binaryType='arraybuffer'
  const terminal = term; let pendingBytes = 0; let renderedBytes = 0; let acknowledge = false
  const isCurrent = () => thisEpoch === epoch && ws === socket && term === terminal
  connectTimer = window.setTimeout(() => { if (isCurrent() && status.value === 'connecting') disconnect('连接超时，请手动重试。') },20000)
  socket.onopen = () => {
   if (!isCurrent()) {socket.close(); return}
   try { socket.send(JSON.stringify({type:'connect',flow_control:true,cols:Math.min(400,Math.max(2,terminal.cols)),rows:Math.min(200,Math.max(2,terminal.rows)),...(target.value?.kind === 'ssh' ? method.value === 'password' ? {password:password.value} : {private_key:privateKey.value,passphrase:passphrase.value} : {})})) }
   catch { error.value = '发送连接请求失败，请手动重试。'; disconnect('连接未完成。') }
   finally { clearCredentials() }
  }
  socket.onmessage = event => {
   if (!isCurrent()) return
   if (event.data instanceof ArrayBuffer) {
    const bytes = event.data.byteLength
    if (bytes > maxPendingOutput - pendingBytes) { error.value = '终端输出超过浏览器渲染速度，已断开以限制内存占用。请缩小命令输出后重连。'; disconnect('输出渲染队列已达到上限。'); return }
    pendingBytes += bytes
    terminal.write(new Uint8Array(event.data), () => {
     if (!isCurrent()) return
     pendingBytes -= bytes; renderedBytes += bytes
     if (acknowledge) queueAck(socket, () => renderedBytes, isCurrent)
    })
    return
   }
   let m: {type: string; message?: string; code?: number; session_id?: string; flow_control?: string; window_bytes?: number}
   try { m=JSON.parse(event.data); if (!m || typeof m.type !== 'string') throw new Error('invalid control') } catch {disconnect('终端协议异常。');return}
   if (m.type === 'ready') {
    clearTimeout(connectTimer); connectTimer = undefined; acknowledge = m.flow_control === 'ack'
    currentSessionID.value = m.session_id || ''; status.value = 'connected'
    lastSentCols = 0; lastSentRows = 0; startWarnings(); void refreshSessions(); resize(); focusTerminal()
   }
   else if (m.type === 'error') {error.value = translate(m.message || '');disconnect(idleOrMaxNotice(m.message || '') || '会话已结束。勾选确认后可手动重连。')}
   else if (m.type === 'exit') disconnect(`Shell 已退出（${m.code === 0 ? '正常退出' : '异常退出'}）。勾选确认后可再次连接。`)
  }
  socket.onerror = () => {if (isCurrent()) error.value='终端连接失败，票据可能过期或权限已变化。请手动重连。'}
  socket.onclose = event => {if (isCurrent()) disconnect(closeReason(event.reason))}
 } catch(e) {
  if (thisEpoch === epoch) {error.value=explain(e);disconnect('连接未完成。');if (e instanceof ApiError && e.status === 429) void refreshSessions()}
 } finally {if (thisEpoch === epoch) controller=undefined}
}
function idleOrMaxNotice(value: string) {
 if (value.includes('idle')) {
  const idleMin = Math.max(1, Math.round((config.value?.idle_seconds || 300) / 60))
  return `${idleMin} 分钟没有输入，连接已结束。目标仍保留，勾选确认后可手动重连。`
 }
 return ''
}
function translate(value: string) {
 if (value.includes('host key') || value.includes('authentication')) return 'SSH 认证或主机指纹校验失败，请核对凭据与管理员配置的 SHA256 指纹。'
 if (value.includes('unreachable')) return 'SSH 主机不可达，请检查地址、端口和网络。'
 if (value.includes('permission')) return '登录已过期或机房权限变化，连接已结束。'
 if (value.includes('idle')) return idleOrMaxNotice(value)
 if (value.includes('private key')) return 'SSH 私钥格式或口令不正确。'
 return '终端启动失败，请检查目标平台、Shell 路径和 SSH PTY 支持。'
}
function sendCommand() {
 const text = command.value
 if (!text.trim()) return
 // Confirm only when the draft itself contains newlines; the trailing CR we append is not a paste hazard.
 const confirmPaste = /[\r\n]/.test(text)
 const payload = /[\r\n]$/.test(text) ? text : text + '\r'
 if (sendInput(payload, confirmPaste)) { command.value = ''; commandInput.value?.focus() }
}
async function copySelection() {if(term?.hasSelection())try{await navigator.clipboard.writeText(term.getSelection());notice.value='已复制选中内容。'}catch{error.value='复制失败，请使用浏览器复制操作。'}}
function sessionTime(value: number) { return value > 0 ? new Date(value * 1000).toLocaleString() : '等待连接' }
function outputSize(value: number) { return value >= 1024 * 1024 ? `${(value / 1024 / 1024).toFixed(1)} MB` : `${Math.ceil(value / 1024)} KB` }
function close() {disconnect();emit('close')}
function pageClosed() {disconnect('页面离开或网络中断，连接已关闭。')}
watch(() => props.node, () => {command.value='';void load()})
watch(targetID, () => {if(status.value !== 'idle') disconnect('目标已切换，请重新连接。');clearCredentials();consent.value=false;command.value='';term?.clear()})
watch(method,clearCredentials)
onMounted(() => {emit('ready');void load();window.addEventListener('pagehide',pageClosed);window.addEventListener('offline',pageClosed)})
onBeforeUnmount(() => {
 disposed=true;disconnect();sessionEpoch++;sessionsController?.abort();terminationController?.abort()
 clearSessionRefreshTimers();clearWarnings();disposeTerminal()
 window.removeEventListener('pagehide',pageClosed);window.removeEventListener('offline',pageClosed)
})
</script>
<template>
 <section class="shell-panel" aria-label="WebShell 主机终端">
  <div class="heading"><h2><AppIcon name="shell" />WebShell 主机终端</h2><button @click="close" aria-label="关闭 WebShell">关闭</button></div>
  <p>本机 Shell 与 SSH 交互终端。操作以目标系统账号权限执行，请先核对主机。关闭面板、切换节点或离线会断开；不自动重连。临近空闲或时限时会提示。</p>
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
  <section class="sessions" aria-label="我的终端会话">
   <div class="heading"><h3>我的会话</h3><button :disabled="sessionsLoading || !!terminating" @click="refreshSessions">刷新会话</button></div>
   <p>仅列出当前账号可访问的活动会话。连接成功、断开或终止后会自动刷新；其他页面遗留的连接也可手动终止。</p>
   <p v-if="sessionsError" class="error" role="alert">{{sessionsError}}</p>
   <p v-if="sessionsLoading">正在查询会话…</p>
   <p v-else-if="sessions && !sessions.sessions.length">查询时没有活动会话。</p>
   <ul v-if="sessions?.sessions.length" class="session-list"><li v-for="session in sessions.sessions" :key="session.id">
    <div><strong>{{session.target.name || session.target.id}}</strong><span v-if="session.id === currentSessionID"> · 当前页面</span><p>{{session.target.kind === 'ssh' ? `${session.target.user}@${session.target.address}` : '本机服务账号'}} · {{session.state === 'closing' ? '正在清理' : session.state === 'connecting' ? '正在连接' : '已建立连接'}}</p><p>开始 {{sessionTime(session.started_at)}} · 最近输入 {{sessionTime(session.last_input_at)}} · 已输出 {{outputSize(session.output_bytes)}}</p></div>
    <button :disabled="!!terminating || session.state === 'closing'" @click="terminateSession(session)">{{terminating === session.id ? '正在终止…' : '终止会话'}}</button>
   </li></ul>
  </section>
  <div ref="terminalElement" class="terminal" aria-label="交互终端输出"></div>
  <div class="shortcuts" aria-label="终端快捷键"><button v-for="key in [{label:'Ctrl+C',value:'\x03'},{label:'Ctrl+D',value:'\x04'},{label:'Tab',value:'\t'},{label:'Esc',value:'\x1b'},{label:'↑',value:'\x1b[A'},{label:'↓',value:'\x1b[B'}]" :key="key.label" :disabled="status !== 'connected'" @click="sendInput(key.value);focusTerminal()">{{key.label}}</button><button @click="term?.clear()">清屏</button><button @click="copySelection">复制选中内容</button></div>
  <form class="command" @submit.prevent="sendCommand"><label>手机命令输入<textarea ref="commandInput" v-model="command" rows="2" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" :disabled="status !== 'connected'" placeholder="输入命令后点击发送；含换行会先确认"></textarea></label><button :disabled="status !== 'connected' || !command.trim()">发送命令</button></form>
  <p>默认 {{Math.max(1, Math.round((config?.idle_seconds || 300) / 60))}} 分钟无输入或会话满 {{Math.max(1, Math.round((config?.max_seconds || 1800) / 60))}} 分钟自动结束。终端会执行你输入的命令；AI 方案不会自动发送到终端。断开不会撤销已执行操作，主动脱离终端的后台任务可能继续运行。</p>
 </section>
</template>
<style scoped>
.shell-panel{border:1px solid #334155;border-radius:12px;padding:18px;background:#0b1526;color:#cbd5e1;margin:16px 0;min-width:0}.heading{display:flex;align-items:center;justify-content:space-between;gap:12px}.heading h2{display:flex;align-items:center;gap:8px;margin:0;font-size:20px}p{font-size:13px;line-height:1.6;overflow-wrap:anywhere}.error{color:#fda4af}.form{display:grid;gap:10px}label{display:grid;gap:6px;font-size:14px}input,select,textarea{box-sizing:border-box;max-width:100%;width:100%;min-height:44px;font:inherit;background:#111e33;color:#e2e8f0;border:1px solid #475569;border-radius:7px;padding:8px}.consent{display:flex;align-items:center;min-height:44px}.consent input{width:20px;height:20px;min-height:20px;flex-shrink:0}.identity{margin:0;color:#67e8f9}button{min-height:44px;border:1px solid #475569;border-radius:7px;padding:8px 12px;background:#16243b;color:#e2e8f0;font:inherit;cursor:pointer}button:disabled{opacity:.45;cursor:default}.primary{border-color:#0d9488;background:#115e59}.actions,.shortcuts{display:flex;flex-wrap:wrap;gap:8px;align-items:center}.terminal{margin:16px 0 10px;height:380px;max-width:100%;overflow:hidden;background:#07101e;border:1px solid #334155;padding:8px;box-sizing:border-box;min-width:0}.command{margin-top:12px;display:flex;gap:8px;align-items:end}.command label{flex:1;min-width:0}.sessions{margin-top:16px;border-top:1px solid #334155;padding-top:12px}.sessions h3{margin:0;font-size:16px}.session-list{list-style:none;padding:0;display:grid;gap:10px}.session-list li{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap;border:1px solid #334155;border-radius:8px;padding:10px}.session-list li>div{min-width:0;flex:1 1 200px;overflow-wrap:anywhere}.session-list p{margin:5px 0 0}pre{white-space:pre-wrap;overflow-wrap:anywhere;font-size:12px}summary{cursor:pointer;min-height:44px}button:focus-visible,input:focus-visible,select:focus-visible,textarea:focus-visible{outline:2px solid #5eead4;outline-offset:2px}@media(max-width:600px){.shell-panel{padding:12px}.heading h2{font-size:18px}input,select,textarea{font-size:16px}.terminal{height:320px}.command{flex-wrap:wrap}.command label{flex-basis:100%}}
</style>
