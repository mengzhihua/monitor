import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { pluginsApi } from '../../api/plugins/client'
import { auth } from '../../api/auth'
import { ApiError } from '../../api/transport'
import type { PlatformInfo, PluginAction, PluginCatalog } from '../../api/plugins/types'
import { usePolling } from '../../polling'
import { pageVisible } from '../../visibility'
import { allowsPluginAction, pluginAccess } from './model/pluginPolicy'

interface PluginContext { role: string; authenticated: boolean; platform?: PlatformInfo }

/** Owns the local catalog revision and serializes mutations against foreground reads. */
export function usePlugins(context: PluginContext) {
  const catalog = shallowRef<PluginCatalog | null>(null)
  const loading = ref(false)
  const busy = ref(false)
  const stale = ref(true)
  const error = ref('')
  const notice = ref('')
  const denied = ref(false)
  const updatedAt = ref(0)
  const access = computed(() => pluginAccess(context.platform, context.role, context.authenticated))
  const canWrite = computed(() => access.value.control && !!auth.token && !denied.value && !stale.value
    && !loading.value && !busy.value && pageVisible.value && !!catalog.value?.running)
  let disposed = false
  let generation = 0
  let readRequest: AbortController | undefined
  let actionRequest: AbortController | undefined

  function cancelRead() {
    generation++
    readRequest?.abort()
    readRequest = undefined
    loading.value = false
  }
  function accept(next: PluginCatalog) {
    if (next.schema_version !== 1 || !Number.isSafeInteger(next.revision) || next.revision < 0
      || !Array.isArray(next.plugins) || !Array.isArray(next.diagnostics)) throw new Error('插件目录格式不受支持，请刷新页面或核对服务版本。')
    catalog.value = next
    stale.value = !pageVisible.value
    updatedAt.value = Date.now()
    error.value = ''
  }
  function failed(reason: unknown) {
    stale.value = true
    if (reason instanceof ApiError && [401, 403].includes(reason.status)) {
      denied.value = true
      catalog.value = null
      notice.value = ''
      error.value = reason.status === 401 ? '登录已失效，请重新登录后查看插件。' : '当前账号无权管理本机插件，请重新登录或联系管理员。'
    } else error.value = reason instanceof Error ? reason.message : '插件请求失败，请刷新目录后重试。'
  }

  const refresh = usePolling(async signal => {
    if (busy.value || denied.value || !access.value.read || !auth.token) return
    cancelRead()
    const expected = generation
    const token = auth.token
    const current = readRequest = new AbortController()
    const abort = () => current.abort()
    signal.addEventListener('abort', abort, { once: true })
    if (signal.aborted) abort()
    loading.value = true
    try {
      const next = await pluginsApi.catalog(current.signal)
      if (!disposed && !current.signal.aborted && generation === expected && token === auth.token) accept(next)
    } catch (reason) {
      if (!disposed && !current.signal.aborted && generation === expected && token === auth.token) failed(reason)
    } finally {
      signal.removeEventListener('abort', abort)
      if (readRequest === current) { readRequest = undefined; loading.value = false }
    }
  }, 5000)

  async function act(action: PluginAction, name?: string) {
    // Session storage can change outside Vue's reactivity; verify the live token at dispatch.
    if (!auth.token || !canWrite.value || !catalog.value || !allowsPluginAction(catalog.value, action, name)) return
    const revision = catalog.value.revision
    cancelRead()
    const expected = generation
    const token = auth.token
    const current = actionRequest = new AbortController()
    busy.value = true
    error.value = ''
    notice.value = ''
    let reconcile = false
    try {
      const next = await pluginsApi.act({ action, if_revision: revision, ...(name ? { name } : {}) }, current.signal)
      if (disposed || current.signal.aborted || expected !== generation || token !== auth.token) return
      accept(next)
      notice.value = action === 'rescan' ? '已重新扫描本机插件目录。' : '操作请求已接受，实际运行状态以目录中的最新状态为准。'
    } catch (reason) {
      if (disposed || current.signal.aborted || expected !== generation || token !== auth.token) return
      failed(reason)
      if (reason instanceof ApiError && reason.status === 409) {
        notice.value = '插件目录版本已变化，正在读取最新目录；请核对状态后重试。'
        reconcile = true
      } else if (!denied.value) notice.value = '操作未获得确认，服务端可能已处理；请刷新目录核对状态后再操作。'
    } finally {
      if (actionRequest === current) { actionRequest = undefined; busy.value = false }
    }
    if (reconcile && !disposed) await refresh()
  }

  watch(pageVisible, visible => {
    if (!visible) { stale.value = true; cancelRead() }
  }, { flush: 'sync' })
  watch(() => `${context.role}:${context.authenticated}:${context.platform?.api_version}:${context.platform?.scope}:${context.platform?.capabilities.join(',')}`, () => {
    cancelRead()
    actionRequest?.abort()
    actionRequest = undefined
    busy.value = false
    catalog.value = null
    denied.value = false
    stale.value = true
    notice.value = ''
    if (access.value.read) void refresh()
  })
  onBeforeUnmount(() => {
    disposed = true
    cancelRead()
    actionRequest?.abort()
  })

  return { catalog, loading, busy, stale, error, notice, denied, updatedAt, canWrite, refresh, act,
    canLeave: () => !busy.value }
}
