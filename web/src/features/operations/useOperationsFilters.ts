import { computed, ref, type Ref } from 'vue'
import type { OperationsView } from '../../api/operations/types'
import type { OperationsSection } from './useOperationsSection'

/** Filter state is shared by list reads, exports and saved views. */
export function useOperationsFilters(section: Ref<OperationsSection>) {
  const query = ref('')
  const severity = ref('all')
  const nodeStatus = ref('all')
  const pendingOnly = ref(false)
  const ownerFilter = ref('all')
  const progressFilter = ref('all')
  const moreFilters = ref(false)
  const advancedFilterCount = computed(() => Number(nodeStatus.value !== 'all') + (section.value === 'problems'
    ? Number(ownerFilter.value !== 'all') + Number(progressFilter.value !== 'all') + Number(pendingOnly.value) : 0))
  const activeFilterCount = computed(() => advancedFilterCount.value + Number(!!query.value.trim())
    + (section.value === 'problems' ? Number(severity.value !== 'all') : 0))
  function clearFilters() {
    query.value = ''; severity.value = 'all'; nodeStatus.value = 'all'
    ownerFilter.value = 'all'; progressFilter.value = 'all'; pendingOnly.value = false
  }
  const visibleCount = ref(50)
  function listQuery(limit = visibleCount.value) {
    return {
      limit: String(limit), q: query.value.trim(), node_status: nodeStatus.value, severity: severity.value,
      owner: ownerFilter.value, progress: progressFilter.value, ...(pendingOnly.value ? { pending: '1' } : {}),
    }
  }
  function applyView(v: OperationsView) {
    query.value = v.query; severity.value = v.severity; nodeStatus.value = v.nodeStatus; pendingOnly.value = v.pendingOnly; visibleCount.value = 50
    ownerFilter.value = v.ownerFilter; progressFilter.value = v.progressFilter
  }
  const inputs = [query, severity, nodeStatus, pendingOnly, ownerFilter, progressFilter] as const
  return { query, severity, nodeStatus, pendingOnly, ownerFilter, progressFilter, moreFilters,
    visibleCount, advancedFilterCount, activeFilterCount, clearFilters, listQuery, applyView, inputs }
}
export type OperationsFilters = ReturnType<typeof useOperationsFilters>
