import { ref } from 'vue'

export const operationsSections = [
  { id: 'problems', label: '问题中心' },
  { id: 'nodes', label: '主机资源' },
  { id: 'history', label: '处理历史' },
  { id: 'maintenance', label: '维护计划' },
  { id: 'notifications', label: '通知诊断' },
] as const

export type OperationsSection = typeof operationsSections[number]['id']

export function useOperationsSection() {
  const saved = new URL(location.href).searchParams.get('ops')
  const section = ref<OperationsSection>(operationsSections.find(item => item.id === saved)?.id ?? 'problems')
  function selectSection(value: OperationsSection) {
    section.value = value
    const url = new URL(location.href)
    if (value === 'problems') url.searchParams.delete('ops')
    else url.searchParams.set('ops', value)
    url.searchParams.delete('token')
    history.replaceState(history.state, '', url)
  }
  return { section, selectSection }
}
