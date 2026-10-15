import { ref, watch } from 'vue'
import { decodeTimeline, encodeTimeline, timelineKey, type ChartTimeline } from '../../chartTimeline'

export const metricWindows = [
  { label: '1 分钟', value: 60 }, { label: '5 分钟', value: 300 },
  { label: '15 分钟', value: 900 }, { label: '1 小时', value: 3600 },
  { label: '6 小时', value: 21600 }, { label: '24 小时', value: 86400 },
  { label: '7 天', value: 604800 },
]

export function useMetricsTimeline() {
  let initial: ChartTimeline = { window: 300, end: null }
  let readError = ''
  try { const saved = sessionStorage.getItem(timelineKey); if (saved) initial = decodeTimeline(saved) }
  catch { readError = '无法恢复查看时间，已使用实时模式；原始配置未覆盖。' }
  const windowSec = ref(initial.window)
  const chartEnd = ref<number | null>(initial.end)
  const notice = ref(readError)
  watch([windowSec, chartEnd], () => {
    try { sessionStorage.setItem(timelineKey, encodeTimeline({ window: windowSec.value, end: chartEnd.value })); notice.value = '' }
    catch { notice.value = '查看时间暂存失败，本次仍可使用；刷新后可能无法恢复。' }
  }, { flush: 'sync' })
  function showLiveChart() {
    if (chartEnd.value === null) return
    chartEnd.value = null
    notice.value = '已返回实时，以查看当前运维问题对应的指标。' + notice.value
  }
  return { windowSec, chartEnd, notice, showLiveChart }
}
