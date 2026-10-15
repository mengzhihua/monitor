import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { operationsApi as api } from '../../api/operations/client'
import { ApiError } from '../../api/transport'
import type { OperationsSnapshot } from '../../api/operations/types'
import { usePolling } from '../../polling'
import type { OperationsFilters } from './useOperationsFilters'

/** Owns foreground polling, debounced reads and invalidation of obsolete responses. */
export function useOperationsOverview(filters: OperationsFilters) {
  const { query, severity, nodeStatus, pendingOnly, ownerFilter, progressFilter, visibleCount, listQuery } = filters
  const snapshot = ref<OperationsSnapshot | null>(null)
  const error = ref('')
  const loading = ref(true)
  let overviewRequest: AbortController | undefined
  let searchTimer: number | undefined
  let disposed = false
  function invalidateAccess(message: string) {
    overviewRequest?.abort()
    snapshot.value = null
    loading.value = false
    error.value = message
  }
  watch(filters.inputs, (next, previous) => {
    visibleCount.value = 50
    overviewRequest?.abort()
    loading.value = true
    clearTimeout(searchTimer)
    const textOnly = next[0] !== previous[0] && next.slice(1).every((value, index) => value === previous[index + 1])
    if (textOnly) searchTimer = window.setTimeout(() => { void refresh() }, 250)
    else void refresh()
  })
  onBeforeUnmount(() => {
    disposed = true
    clearTimeout(searchTimer)
    overviewRequest?.abort()
  })
  const pollRefresh = usePolling(async signal => {
    if (searchTimer !== undefined) return
    overviewRequest?.abort()
    const request = overviewRequest = new AbortController()
    const cancel = () => request.abort()
    signal.addEventListener('abort', cancel, { once: true })
    if (signal.aborted) request.abort()
    loading.value = true
    try {
      const next = await api.operations(request.signal, listQuery())
      if (request.signal.aborted || disposed) return
      snapshot.value = next
      error.value = ''
    } catch (e) {
      if (request.signal.aborted || disposed) return
      if (e instanceof ApiError && [401, 403].includes(e.status)) {
        snapshot.value = null
      }
      error.value = e instanceof ApiError && e.status === 401 ? '登录已失效，请重新登录。' : '刷新失败，正在展示上次成功的数据；请检查连接并重试。'
    } finally {
      signal.removeEventListener('abort', cancel)
      if (overviewRequest === request) overviewRequest = undefined
      if (!request.signal.aborted && !disposed) loading.value = false
    }
  }, 15000)
  function refresh() {
    clearTimeout(searchTimer)
    searchTimer = undefined
    return pollRefresh()
  }

  const filteredNodes = computed(() => {
    const q = query.value.trim().toLowerCase()
    return (snapshot.value?.nodes || []).filter(n => (nodeStatus.value === 'all' || n.status === nodeStatus.value)
      && (!q || `${n.hostname} ${n.id} ${n.os} ${JSON.stringify(n.labels)}`.toLowerCase().includes(q)))
      .sort((a,b) => (b.alarms?.critical || 0) - (a.alarms?.critical || 0) || a.hostname.localeCompare(b.hostname))
  })
  const problems = computed(() => {
    const q = query.value.trim().toLowerCase()
    return (snapshot.value?.problems || []).filter(p => (severity.value === 'all' || p.severity === severity.value)
      && (nodeStatus.value === 'all' || p.node_status === nodeStatus.value)
      && (!pendingOnly.value || !p.handling.acknowledged)
      && (ownerFilter.value === 'all' || (ownerFilter.value === 'mine' && !!p.handling.assignee && p.handling.assignee === snapshot.value?.current_user.name)
        || (ownerFilter.value === 'unassigned' && !p.handling.assignee) || (ownerFilter.value === 'assigned' && !!p.handling.assignee))
      && (progressFilter.value === 'all' || p.handling.status === progressFilter.value)
      && (!q || `${p.hostname} ${p.node} ${p.name} ${p.chart} ${p.family} ${p.info} ${p.handling.assignee}`.toLowerCase().includes(q)))
  })
  const families = computed(() => {
    if (snapshot.value?.page) return snapshot.value.page.families.map(f => [f.name, f.count] as [string, number])
    const counts = new Map<string, number>()
    for (const p of problems.value) counts.set(p.family || '其他', (counts.get(p.family || '其他') || 0) + 1)
    return [...counts].sort((a,b) => b[1] - a[1]).slice(0, 8)
  })
  const nodesVisible = computed(() => snapshot.value?.page ? filteredNodes.value : filteredNodes.value.slice(0, visibleCount.value))
  const problemsVisible = computed(() => snapshot.value?.page ? problems.value : problems.value.slice(0, visibleCount.value))
  const nodeTotal = computed(() => snapshot.value?.page?.nodes_matched ?? filteredNodes.value.length)
  const problemTotal = computed(() => snapshot.value?.page?.problems_matched ?? problems.value.length)
  const activeIDs = computed(() => new Set(snapshot.value?.problems.map(p => p.id) || []))
  return { snapshot, error, loading, refresh, invalidateAccess,
    nodesVisible, problems, problemsVisible, nodeTotal, problemTotal, families, activeIDs }
}
