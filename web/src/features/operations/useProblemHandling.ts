import { computed, onBeforeUnmount, ref, watch, type Ref } from 'vue'
import { operationsApi as api } from '../../api/operations/client'
import { ApiError } from '../../api/transport'
import type { HandlingChange, HandlingStatus, OperationsSnapshot, Problem } from '../../api/operations/types'
import { actionName } from './problemPresentation'
import type { OperationsFilters } from './useOperationsFilters'

interface HandlingContext {
  role: Ref<string>
  snapshot: Ref<OperationsSnapshot | null>
  error: Ref<string>
  refresh: () => Promise<void>
  filters: OperationsFilters
}

/** Keeps drafts and optimistic revisions independent of mounted problem cards or sections. */
export function useProblemHandling({ role, snapshot, error, refresh, filters }: HandlingContext) {
  const actionError = ref('')
  const busy = ref('')
  const historyRefreshKey = ref(0)
  const notes = ref<Record<string, string>>({})
  const owners = ref<Record<string, string>>({})
  const progress = ref<Record<string, HandlingStatus>>({})
  const draftRevisions = ref<Record<string, number>>({})
  const selectedProblems = ref<Problem[]>([])
  const batchError = ref('')
  const batchMessage = ref('')
  const batchCompleted = ref(0)
  const batchAction = ref<HandlingChange['action']>('acknowledge')
  const batchAssignee = ref('')
  const batchStatus = ref<HandlingStatus>('investigating')
  const batchNote = ref('')
  const canHandle = computed(() => ['admin', 'troubleshooter'].includes(role.value))
  const canSelfAssign = computed(() => snapshot.value?.assignees.some(u => u.name === snapshot.value?.current_user.name))
  const mineCount = computed(() => snapshot.value?.summary.mine || 0)
  const dirty = computed(() => {
    const byID = new Map(snapshot.value?.problems.map(p => [p.id, p]))
    return Object.values(notes.value).some(note => !!note.trim())
      || Object.entries(owners.value).some(([id, owner]) => owner !== byID.get(id)?.handling.assignee)
      || Object.entries(progress.value).some(([id, state]) => state !== byID.get(id)?.handling.status)
      || !!batchNote.value.trim()
  })
  watch(filters.inputs, () => {
    if (!selectedProblems.value.length) return
    selectedProblems.value = []
    batchMessage.value = '筛选条件已改变，已清空原选择，请重新选择问题。'
  })
  watch(snapshot, value => { if (!value) selectedProblems.value = [] })
  let disposed = false
  onBeforeUnmount(() => { disposed = true })
  function edit(p: Problem) {
    draftRevisions.value[p.id] ??= p.handling.revision
  }
  async function update(p: Problem, change: HandlingChange) {
    if (busy.value || !canHandle.value || error.value) return
    busy.value = p.id
    actionError.value = ''
    try {
      const saved = await api.handleProblem({ id: p.id, ...change, note: notes.value[p.id] || '', revision: draftRevisions.value[p.id] ?? p.handling.revision })
      if (disposed) return
      historyRefreshKey.value++
      delete notes.value[p.id]
      if (change.action === 'assign' || change.action === 'unassign') delete owners.value[p.id]
      if (change.action === 'progress') delete progress.value[p.id]
      // Other fields may still contain drafts. Anchor them to our own accepted
      // write, not to a newer overview that may already include someone else's edit.
      if (owners.value[p.id] !== undefined || progress.value[p.id] !== undefined) {
        draftRevisions.value[p.id] = saved.revision
      } else delete draftRevisions.value[p.id]
      await refresh()
    } catch (e) {
      if (disposed) return
      if (e instanceof ApiError && e.status === 409) {
        await refresh()
        if (disposed) return
        actionError.value = error.value
          ? '问题或处理记录已变化，但最新记录读取失败。草稿已保留，请恢复连接并刷新后重试。'
          : '问题或处理记录已变化，已重新读取。草稿已保留，请核对责任人和进度后重试。'
        if (!error.value) delete draftRevisions.value[p.id]
        if (snapshot.value && !snapshot.value.problems.some(x => x.id === p.id) && notes.value[p.id]) {
          actionError.value = `原问题已恢复或进入新阶段，未提交备注：${notes.value[p.id]}`
        }
      } else actionError.value = e instanceof ApiError && e.status === 403 ? '当前账号没有问题处理权限。' : `保存失败：${String(e)}`
    } finally { if (!disposed) busy.value = '' }
  }
  function toggleProblem(p: Problem, checked: boolean) {
    if (busy.value) return
    if (!checked) selectedProblems.value = selectedProblems.value.filter(x => x.id !== p.id)
    else if (selectedProblems.value.length < 50 && !selectedProblems.value.some(x => x.id === p.id)) selectedProblems.value.push(p)
    batchError.value = ''; batchMessage.value = ''
  }
  async function updateBatch(change: HandlingChange & { note: string }) {
    if (busy.value || !canHandle.value || !selectedProblems.value.length || error.value) return
    const items = selectedProblems.value.map(p => ({ id: p.id, revision: p.handling.revision }))
    busy.value = 'batch'; batchError.value = ''; batchMessage.value = ''
    try {
      const result = await api.handleProblems({ ...change, items })
      if (disposed) return
      selectedProblems.value = []
      batchCompleted.value++
      batchNote.value = ''
      historyRefreshKey.value++
      batchMessage.value = `已对 ${result.count} 个问题完成${actionName[change.action] || change.action}。`
      await refresh()
    } catch (e) {
      if (disposed) return
      if (e instanceof ApiError && e.status === 409) {
        await refresh()
        if (disposed) return
        batchError.value = error.value
          ? '所选问题或处理记录已变化，整批未提交；刷新也未成功。请恢复连接、刷新总览并重新选择，统一备注已保留。'
          : '所选问题或处理记录已变化，整批未提交。已读取最新状态，请重新选择后再执行；统一备注已保留。'
      } else if (e instanceof ApiError && e.status === 403) batchError.value = '当前账号没有批量处置权限。'
      else if (e instanceof ApiError && e.status === 401) { selectedProblems.value = []; batchError.value = '登录已失效，请重新登录。' }
      else if (e instanceof ApiError && e.status === 503) batchError.value = '服务端保存失败，整批未提交，选择与统一备注已保留。'
      else if (e instanceof ApiError && e.status === 400) batchError.value = '批量请求无效，请检查责任人、备注或选择数量。'
      else {
        await refresh()
        if (disposed) return
        batchError.value = '请求中断，结果暂不确定。请核对最新处理记录后重新选择；统一备注已保留。'
      }
    } finally { if (!disposed) busy.value = '' }
  }
  function assign(p: Problem) {
    const assignee = owners.value[p.id] ?? p.handling.assignee
    void update(p, assignee ? { action: 'assign', assignee } : { action: 'unassign' })
  }
  return { actionError, busy, historyRefreshKey, notes, owners, progress, selectedProblems,
    batchError, batchMessage, batchCompleted, batchAction, batchAssignee, batchStatus, batchNote,
    canHandle, canSelfAssign, mineCount, dirty, edit, update, toggleProblem, updateBatch, assign }
}
