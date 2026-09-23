import { ranges } from './dashboardConfig'

export const timelineKey = 'monitor.chart-timeline.v1'
export interface ChartTimeline { window: number; end: number | null }
export function decodeTimeline(raw: string, now = Date.now()/1000): ChartTimeline {
  if (raw.length > 1000) throw new Error('查看时间配置过大。')
  const value = JSON.parse(raw)
  if (value?.version !== 1 || !ranges.includes(value.window) || !value.window
    || value.end !== null && (!Number.isSafeInteger(value.end) || value.end > Math.floor(now) || value.end-value.window < 1)) {
    throw new Error('查看时间配置无效，已使用实时模式。')
  }
  return { window: value.window, end: value.end }
}
export function encodeTimeline(value: ChartTimeline): string {
  const raw = JSON.stringify({ version: 1, ...value })
  decodeTimeline(raw)
  return raw
}
