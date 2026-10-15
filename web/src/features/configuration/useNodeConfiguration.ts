import { computed, ref, shallowRef, watch } from 'vue'
import type { NodeConfigFull, NodeInfo } from '../../api/hub/types'
import { hubApi as api } from '../../api/hub/client'
import { ApiError } from '../../api/transport'
import { usePolling } from '../../polling'
import { configurationAuthError, configurationConflictError, configurationErrorDetail, configurationSaveError, isConfigurationAuthError } from './configurationErrors'
import type { ConfigurationFeedback } from './useConfigurationFeedback'
import { useConfigurationRequest } from './useConfigurationRequest'

export function nodeConfigurationText(config: NodeConfigFull) { return config.yaml || config.reported || '' }

export function useNodeConfiguration(active: () => boolean, canManage: () => boolean, feedback: ConfigurationFeedback) {
  const request = useConfigurationRequest()
  const listRequest = useConfigurationRequest()
  const nodes = ref<NodeInfo[]>([])
  const nodesLoaded = ref(false)
  const nodeID = ref('')
  const config = shallowRef<NodeConfigFull | null>(null)
  const baseline = shallowRef<NodeConfigFull | null>(null)
  const draft = ref('')
  let base = ''
  let updated = 0
  function dirty() { return draft.value !== base }
  const conflict = computed(() => !!config.value && !!baseline.value && ((config.value.updated ?? 0) !== (baseline.value.updated ?? 0) || nodeConfigurationText(config.value) !== nodeConfigurationText(baseline.value)))

  function apply(next: NodeConfigFull, force = false) {
    config.value = next
    if (!force && dirty()) return
    baseline.value = next
    draft.value = base = nodeConfigurationText(next)
    updated = next.updated ?? 0
  }

  async function loadNodes() {
    const read = listRequest.start()
    if (!read) return
    try {
      const result = await api.nodes(read.signal)
      if (!read.current() || !active()) return
      nodes.value = result.nodes.filter(node => !node.local)
      if (nodeID.value && !nodes.value.some(node => node.id === nodeID.value)) nodeID.value = ''
      nodesLoaded.value = true
    } catch (error) {
      if (read.current()) feedback.error.value = isConfigurationAuthError(error) ? configurationAuthError : `读取节点列表失败：${configurationErrorDetail(error)}`
    } finally { read.finish() }
  }

  async function load(force = false, signal?: AbortSignal) {
    const id = nodeID.value
    if (!id || !active()) { request.cancel(); return }
    const read = request.start(force, signal)
    if (!read) return
    try {
      const next = await api.nodeConfig(id, read.signal)
      if (read.current() && nodeID.value === id && active()) apply(next, force)
    } catch (error) {
      if (read.current() && nodeID.value === id) feedback.error.value = isConfigurationAuthError(error) ? configurationAuthError : `读取节点配置失败：${configurationErrorDetail(error)}`
    } finally { read.finish() }
  }

  async function reload() {
    if (feedback.busy.value || request.loading.value) return
    if (dirty() && !confirm('载入最新配置将放弃当前草稿。请先复制需要保留的内容，确定继续？')) return
    feedback.clear()
    await load(true)
  }

  function selectNode(event: Event) {
    const select = event.target as HTMLSelectElement
    if (feedback.busy.value || request.reloading.value || (dirty() && !confirm('切换节点将放弃当前节点的未保存修改，确定继续？'))) {
      select.value = nodeID.value
      return
    }
    nodeID.value = select.value
  }

  async function save() {
    if (!canManage() || !active() || request.loading.value || conflict.value || !nodeID.value || !config.value) return
    const mutation = feedback.beginMutation()
    if (!mutation) return
    const id = nodeID.value
    request.cancel()
    try {
      const saved = await api.putNodeConfig({ node_id: id, yaml: draft.value, if_updated: updated })
      if (!mutation.current() || nodeID.value !== id) return
      apply(saved, true)
      feedback.message.value = saved.pushed && saved.online
        ? '已校验并推送给节点，等待 agent 应用（会自动重启重连）。'
        : '已保存，节点上线后自动下发。'
    } catch (error) {
      if (!mutation.current() || nodeID.value !== id) return
      if (error instanceof ApiError && error.status === 409) {
        await load()
        if (mutation.current() && nodeID.value === id) feedback.error.value = configurationConflictError
      } else feedback.error.value = configurationSaveError(error)
    } finally { mutation.finish() }
  }

  watch(nodeID, () => {
    request.cancel()
    config.value = baseline.value = null
    draft.value = base = ''
    updated = 0
    feedback.clear()
    void load()
  })
  watch(active, enabled => {
    request.cancel()
    listRequest.cancel()
    if (!enabled) return
    if (!nodesLoaded.value) void loadNodes()
    if (nodeID.value) void load()
  }, { immediate: true })
  usePolling(async signal => {
    // A slow foreground read must finish; polling must not abort it every interval.
    if (feedback.busy.value || request.loading.value || !active() || !nodeID.value) return
    await load(false, signal)
  }, 10000)

  return { nodes, nodesLoaded, nodeID, config, draft, conflict, dirty, selectNode, reload, save, loading: request.loading, reloading: request.reloading }
}
