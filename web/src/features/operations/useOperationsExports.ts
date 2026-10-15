import { computed, onBeforeUnmount, ref, watch, type Ref } from 'vue'
import { operationsApi as api } from '../../api/operations/client'
import { ApiError } from '../../api/transport'
import type { OperationsSnapshot } from '../../api/operations/types'
import type { OperationsFilters } from './useOperationsFilters'

interface ExportContext {
  role: Ref<string>
  snapshot: Ref<OperationsSnapshot | null>
  loading: Ref<boolean>
  filters: OperationsFilters
  invalidateAccess: (message: string) => void
}

/** Downloads use independent fresh requests and are invalidated by filters, access loss or unmount. */
export function useOperationsExports({ role, snapshot, loading, filters, invalidateAccess }: ExportContext) {
  const { query, severity, nodeStatus, pendingOnly, ownerFilter, progressFilter, listQuery } = filters
  const exporting = ref(false)
  const exportingDiagnostic = ref(false)
  const exportError = ref('')
  const canExportDiagnostic = computed(() => role.value === 'admin' || role.value === 'troubleshooter')
  let exportRequest: AbortController | undefined
  let diagnosticRequest: AbortController | undefined
  let disposed = false
  function cancelExport() {
    exportRequest?.abort()
    exportRequest = undefined
    exporting.value = false
    diagnosticRequest?.abort()
    diagnosticRequest = undefined
    exportingDiagnostic.value = false
  }
  watch(filters.inputs, () => { cancelExport(); exportError.value = '' })
  watch(snapshot, value => { if (!value) cancelExport() })
  onBeforeUnmount(() => { disposed = true; cancelExport() })
  function matchingActivity(records: OperationsSnapshot['activity'], search: string) {
    const q = search.trim().toLowerCase()
    return records.filter(r => !q || `${r.problem.hostname} ${r.problem.name} ${r.problem.chart} ${r.assignee} ${r.history.map(h => `${h.actor} ${h.assignee || ''} ${h.previous_assignee || ''} ${h.note}`).join(' ')}`.toLowerCase().includes(q))
  }
  async function exportSnapshot() {
    if (!snapshot.value || exporting.value || exportingDiagnostic.value || loading.value || disposed) return
    const request = exportRequest = new AbortController()
    const filters = { query: query.value, severity: severity.value, node_status: nodeStatus.value,
      pending_only: pendingOnly.value, owner: ownerFilter.value, progress: progressFilter.value }
    const params = listQuery(200)
    exporting.value = true
    exportError.value = ''
    try {
      const full = await api.operations(request.signal, params)
      if (disposed || request.signal.aborted || exportRequest !== request) return
      const data = { captured_at: full.now, stale: false, scope: 'filtered', filters,
        nodes: full.nodes, problems: full.problems, activity: matchingActivity(full.activity, filters.query), matched: full.page }
      const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' }))
      const a = document.createElement('a'); a.href = url; a.download = `monitor-operations-${full.now}.json`; a.click()
      setTimeout(() => URL.revokeObjectURL(url), 1000)
    } catch (e) {
      if (disposed || request.signal.aborted || exportRequest !== request) return
      if (e instanceof ApiError && [401, 403].includes(e.status)) {
        invalidateAccess('登录已失效或没有读取权限，请重新登录。')
        exportError.value = '当前登录无法导出快照，本次未生成文件。'
      } else exportError.value = '快照导出失败，本次未生成文件。请检查连接后重试。'
    } finally {
      if (exportRequest === request) {
        exportRequest = undefined
        exporting.value = false
      }
    }
  }
  async function exportDiagnostic() {
    if (!canExportDiagnostic.value || exporting.value || exportingDiagnostic.value || loading.value || disposed) return
    const request = diagnosticRequest = new AbortController()
    exportingDiagnostic.value = true
    exportError.value = ''
    try {
      const { blob, filename } = await api.exportDiagnostic({ include: 'ops,alarms,notify,history,ai' }, request.signal)
      if (disposed || request.signal.aborted || diagnosticRequest !== request) return
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a'); a.href = url; a.download = filename; a.click()
      setTimeout(() => URL.revokeObjectURL(url), 1000)
    } catch (e) {
      if (disposed || request.signal.aborted || diagnosticRequest !== request) return
      if (e instanceof ApiError && [401, 403].includes(e.status)) {
        exportError.value = '当前账号不能导出诊断包；需要管理员或排障人员登录。'
      } else exportError.value = '诊断包导出失败，本次未生成文件。请检查连接后重试。'
    } finally {
      if (diagnosticRequest === request) {
        diagnosticRequest = undefined
        exportingDiagnostic.value = false
      }
    }
  }
  return { exporting, exportingDiagnostic, exportError, canExportDiagnostic, exportSnapshot, exportDiagnostic }
}
