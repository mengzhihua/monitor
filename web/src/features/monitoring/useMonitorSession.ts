import { computed, onBeforeUnmount, onMounted, ref, watch, type Ref } from 'vue'
import type { Alarm, AlarmLogEntry } from '../../api/alarms/types'
import type { Chart, FunctionInfo, Info } from '../../api/monitoring/types'
import type { NodeInfo } from '../../api/hub/types'
import { ApiError } from '../../api/transport'
import { auth } from '../../api/auth'
import { selection } from '../../api/selection'
import { monitoringApi } from '../../api/monitoring/client'
import { alarmsApi } from '../../api/alarms/client'
import { hubApi } from '../../api/hub/client'
import { live } from '../../live'
import { usePolling } from '../../polling'
import { sharedRequest } from '../../sharedRequest'
import { pageVisible } from '../../visibility'

interface MonitorSessionOptions {
  alarmHistoryVisible: Ref<boolean>
  onUnauthorized?: () => void
  onNodeChange?: () => void
}

const NODE_KEY = 'monitor.node'

/** Own the current node's data, authentication and live/polling lifecycle. */
export function useMonitorSession(options: MonitorSessionOptions) {
  const info = ref<Info | null>(null)
  const charts = ref<Chart[]>([])
  const error = ref('')
  const needToken = ref(false)
  const tokenInput = ref('')
  const loginError = ref('')
  const connected = ref(false)
  const oidcAvailable = ref(false)
  const alarms = ref<Alarm[]>([])
  const alarmLog = ref<AlarmLogEntry[]>([])
  const functions = ref<FunctionInfo[]>([])
  const nodes = ref<NodeInfo[]>([])
  const selectedNode = ref('')
  const nodeNotice = ref('')
  const isHub = computed(() => info.value?.mode === 'hub')
  const currentNode = computed(() => nodes.value.find(node => node.id === selectedNode.value) ?? null)
  // Remote alarms are mirrored by the hub rather than evaluated locally.
  const healthOn = computed(() => selectedNode.value ? true : info.value?.alarms != null)
  const raised = computed(() => ({
    warning: alarms.value.filter(alarm => alarm.status === 'WARNING').length,
    critical: alarms.value.filter(alarm => alarm.status === 'CRITICAL').length,
  }))
  const logsAvailable = computed(() => !selectedNode.value || !functions.value.length || functions.value.some(fn => fn.name === 'logs'))
  let generation = 0
  let loginAttempt = 0
  let disposed = false
  let activeLoad: AbortController | undefined
  let logoutRequest: { token: string; controller: AbortController } | undefined
  const oidcRead = new AbortController()
  const context = () => ({ generation, node: selectedNode.value })
  const isCurrent = (request: ReturnType<typeof context>, signal?: AbortSignal) =>
    !disposed && !signal?.aborted && request.generation === generation && request.node === selectedNode.value

  function clearNodeData() {
    charts.value = []
    alarms.value = []
    alarmLog.value = []
    functions.value = []
  }

  function unauthorized(request: ReturnType<typeof context>, signal?: AbortSignal) {
    if (!isCurrent(request, signal)) return
    generation++
    if (logoutRequest?.token === auth.token) auth.token = ''
    cancelLogout()
    needToken.value = true
    cancelAlarmReads()
    activeLoad?.abort()
    live.stop()
    connected.value = false
    info.value = null
    nodes.value = []
    clearNodeData()
    error.value = ''
    options.onUnauthorized?.()
  }

  function handleAuthenticationFailure(reason: unknown, request: ReturnType<typeof context>, signal?: AbortSignal) {
    if (reason instanceof ApiError && reason.status === 401) unauthorized(request, signal)
  }

  const alarmSnapshotRead = sharedRequest(async signal => {
    const request = context()
    try { return { result: await alarmsApi.alarms(signal), request } }
    catch (reason) { handleAuthenticationFailure(reason, request, signal); throw reason }
  }, ({ result, request }) => {
    if (isCurrent(request) && !needToken.value) alarms.value = Object.values(result.alarms)
  })

  const alarmLogRead = sharedRequest(async signal => {
    const request = context()
    const known = new Set(alarmLog.value.map(event => event.unique_id))
    try { return { entries: await alarmsApi.alarmLog(0, signal), known, request } }
    catch (reason) { handleAuthenticationFailure(reason, request, signal); throw reason }
  }, ({ entries, known, request }) => {
    if (!isCurrent(request) || needToken.value) return
    // The snapshot owns retained history; keep transitions received while it loaded.
    const arrived = alarmLog.value.filter(event => !known.has(event.unique_id))
    const byID = new Map([...arrived, ...entries].map(event => [event.unique_id, event]))
    alarmLog.value = [...byID.values()].sort((a, b) => a.unique_id - b.unique_id).slice(-1000)
  })

  function refreshAlarms(signal?: AbortSignal): Promise<void> {
    if (!healthOn.value || !pageVisible.value || needToken.value || disposed) return Promise.resolve()
    return alarmSnapshotRead.run(signal)
  }

  function refreshAlarmLog(signal?: AbortSignal): Promise<void> {
    if (!options.alarmHistoryVisible.value || !healthOn.value || !pageVisible.value || needToken.value || disposed) return Promise.resolve()
    return alarmLogRead.run(signal)
  }

  function cancelAlarmReads() {
    alarmSnapshotRead.cancel()
    alarmLogRead.cancel()
  }

  watch([selectedNode, pageVisible, needToken], cancelAlarmReads, { flush: 'sync' })
  watch(options.alarmHistoryVisible, open => {
    if (open) void refreshAlarmLog()
    else alarmLogRead.cancel()
  })

  async function refreshFunctions(signal: AbortSignal) {
    const request = context()
    try {
      const result = await monitoringApi.functions(signal)
      if (isCurrent(request, signal) && !needToken.value) functions.value = result
    } catch (reason) { handleAuthenticationFailure(reason, request, signal) }
  }

  async function refreshNodes(signal: AbortSignal) {
    if (!isHub.value) return
    const request = context()
    try {
      const result = await hubApi.nodes(signal)
      if (!isCurrent(request, signal)) return
      nodes.value = result.nodes
      if (selectedNode.value && !nodes.value.some(node => node.id === selectedNode.value)) await selectNode('')
    } catch (reason) { handleAuthenticationFailure(reason, request, signal) }
  }

  async function load(signal: AbortSignal) {
    const request = context()
    try {
      const [metadata, chartResponse] = await Promise.all([monitoringApi.info(signal), monitoringApi.charts(signal)])
      if (!isCurrent(request, signal)) return
      info.value = metadata
      await refreshNodes(signal)
      if (!isCurrent(request, signal)) return
      // Updating existing metadata objects keeps chart instances and subscriptions stable.
      const byID = new Map(charts.value.map(chart => [chart.id, chart]))
      charts.value = Object.values(chartResponse.charts).map(chart => {
        const previous = byID.get(chart.id)
        if (previous) { Object.assign(previous, chart); return previous }
        return chart
      })
      error.value = ''
      needToken.value = false
      await Promise.all([refreshAlarms(signal), refreshAlarmLog(signal), refreshFunctions(signal)])
    } catch (reason) {
      if (!isCurrent(request, signal)) return
      if (reason instanceof ApiError && reason.status === 401) {
        unauthorized(request, signal)
        return
      }
      if (reason instanceof ApiError && reason.status === 404 && selectedNode.value) {
        await selectNode('')
        return
      }
      error.value = String(reason)
    }
  }

  async function selectNode(id: string) {
    if (id === selectedNode.value || disposed) return
    generation++
    loginAttempt++
    activeLoad?.abort()
    live.stop()
    connected.value = false
    selectedNode.value = id
    selection.node = id
    try {
      if (id) sessionStorage.setItem(NODE_KEY, id)
      else sessionStorage.removeItem(NODE_KEY)
      nodeNotice.value = ''
    } catch { nodeNotice.value = '浏览器无法记住所选节点，本次切换仍然生效；刷新后可能恢复到先前节点。' }
    syncNodeURL(id)
    clearNodeData()
    options.onNodeChange?.()
    await refresh()
  }

  function syncNodeURL(id: string) {
    const url = new URL(location.href)
    if (id) url.searchParams.set('node', id)
    else url.searchParams.delete('node')
    url.searchParams.delete('token')
    history.replaceState(history.state, '', url)
  }

  /** Forgetting removes metadata and history; keep the existing explicit confirmation. */
  async function forgetNode(node: NodeInfo) {
    if (!confirm(`确定删除离线节点「${node.hostname}」吗？该节点的图表与历史数据将一并删除，且不可恢复。`)) return
    const request = context()
    try {
      await hubApi.forgetNode(node.id)
      if (!isCurrent(request)) return
      error.value = ''
      await refreshNodes(new AbortController().signal)
    } catch (reason) {
      if (!isCurrent(request)) return
      if (reason instanceof ApiError && reason.status === 401) unauthorized(request)
      else error.value = `删除节点失败：${reason instanceof Error ? reason.message : String(reason)}`
    }
  }

  function onAlarmEvent(event: AlarmLogEntry) {
    if (disposed || needToken.value) return
    if (!alarmLog.value.some(item => item.unique_id === event.unique_id)) alarmLog.value = [...alarmLog.value, event].slice(-1000)
    const alarm = alarms.value.find(item => item.id === event.alarm_id || (item.chart === event.chart && item.name === event.name))
    if (!alarm) { void refreshAlarms(); return }
    alarm.status = event.status
    alarm.value = event.value
    alarm.last_updated = event.when
    alarm.last_status_change = event.when
    if (event.repeat) return
    const raised = event.status === 'WARNING' || event.status === 'CRITICAL'
    if (!raised) {
      if (event.status !== 'CLEAR' || event.old_status === 'WARNING' || event.old_status === 'CRITICAL') {
        alarm.pending_status = undefined
        alarm.pending_since = undefined
        alarm.pending_until = undefined
        alarm.hold_until = undefined
      }
      alarm.recovery_hold = undefined
      return
    }
    const level = event.status === 'CRITICAL' ? 4 : 3
    const pending = alarm.pending_status === 'CRITICAL' ? 4 : alarm.pending_status === 'WARNING' ? 3 : 0
    if (pending > 0 && pending <= level) {
      alarm.pending_status = undefined
      alarm.pending_since = undefined
      alarm.pending_until = undefined
    }
    alarm.hold_until = undefined
    alarm.recovery_hold = undefined
  }

  function cancelLogout() {
    logoutRequest?.controller.abort()
    logoutRequest = undefined
  }

  async function logout() {
    const token = auth.token
    if (disposed || !token || logoutRequest?.token === token) return
    cancelLogout()
    const request = logoutRequest = { token, controller: new AbortController() }
    let timedOut = false
    const deadline = window.setTimeout(() => { timedOut = true; request.controller.abort() }, 15_000)
    // A delayed logout belongs to its original credential. Even a login with
    // the same token starts a new session and cancels this request.
    const current = () => !disposed && logoutRequest === request && auth.token === token
    try {
      const response = await fetch('/api/v1/auth/oidc/logout', {
        method: 'POST', headers: { Authorization: `Bearer ${token}` }, signal: request.controller.signal,
      })
      if (!current() || request.controller.signal.aborted) return
      if (!response.ok && response.status !== 401) throw new Error('退出失败，请重试')
      generation++
      loginAttempt++
      activeLoad?.abort()
      cancelAlarmReads()
      live.stop()
      auth.token = ''
      location.reload()
    } catch (reason) { if (current()) error.value = timedOut ? '退出请求超时，请重试。' : String(reason) }
    finally {
      window.clearTimeout(deadline)
      if (logoutRequest === request) logoutRequest = undefined
    }
  }

  async function submitToken() {
    const attempt = ++loginAttempt
    generation++
    cancelLogout()
    loginError.value = ''
    activeLoad?.abort()
    cancelAlarmReads()
    live.stop()
    auth.token = tokenInput.value.trim()
    tokenInput.value = ''
    await refresh()
    // A later login or node switch owns the session. A 401 increments the data
    // generation, but the current attempt must still display its rejection.
    if (disposed || attempt !== loginAttempt) return
    if (needToken.value) {
      auth.token = ''
      loginError.value = error.value ? '暂时无法验证登录，请检查连接后重试。' : '密码或访问令牌不正确，请重试。'
    }
  }

  const onLiveState = (up: boolean) => { if (!disposed) connected.value = up }
  // Register initialization before usePolling registers its mount callback. The very
  // first request must carry the restored token and selected node.
  onMounted(() => {
    auth.fromURL()
    let saved = new URL(location.href).searchParams.get('node')
    if (saved === null) {
      try { saved = sessionStorage.getItem(NODE_KEY) }
      catch { nodeNotice.value = '浏览器无法恢复上次选择的节点，已显示本机；本次仍可切换节点。' }
    }
    selectedNode.value = saved ?? ''
    selection.node = selectedNode.value
    syncNodeURL(selectedNode.value)
    live.onState = onLiveState
    live.onAlarm = onAlarmEvent
    fetch('/api/v1/auth/oidc/status', { signal: oidcRead.signal }).then(response => response.json()).then(status => {
      if (!disposed) oidcAvailable.value = status.enabled === true
    }).catch(() => {})
  })

  const refresh = usePolling(async signal => {
    const request = context()
    const controller = new AbortController()
    const abort = () => controller.abort()
    signal.addEventListener('abort', abort, { once: true })
    if (signal.aborted) controller.abort()
    activeLoad = controller
    try {
      await load(controller.signal)
      if (isCurrent(request, controller.signal) && !needToken.value && info.value) live.start()
    } finally {
      signal.removeEventListener('abort', abort)
      if (activeLoad === controller) activeLoad = undefined
    }
  }, 30000)

  onBeforeUnmount(() => {
    disposed = true
    generation++
    activeLoad?.abort()
    oidcRead.abort()
    cancelLogout()
    cancelAlarmReads()
    live.stop()
    if (live.onState === onLiveState) live.onState = null
    if (live.onAlarm === onAlarmEvent) live.onAlarm = null
  })

  return {
    info, charts, error, needToken, tokenInput, loginError, connected, oidcAvailable,
    alarms, alarmLog, functions, nodes, selectedNode, nodeNotice, isHub, currentNode,
    healthOn, raised, logsAvailable, refresh, selectNode, forgetNode, refreshAlarms, submitToken, logout,
  }
}
