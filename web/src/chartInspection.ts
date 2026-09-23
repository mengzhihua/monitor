export function localDateTime(seconds: number): string {
  const date = new Date(seconds * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth()+1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
}

export function historyEnd(value: string, duration: number, now = Date.now()/1000): number {
  const seconds = new Date(value).getTime()/1000
  if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d(:\d\d)?$/.test(value) || !Number.isFinite(seconds)
    || localDateTime(seconds) !== (value.length === 16 ? value + ':00' : value)) throw new Error('请输入有效的本地日期和时间。')
  if (seconds > Math.floor(now)) throw new Error('结束时间不能晚于当前时间。')
  if (seconds - duration < 1) throw new Error('时间范围过早，请选择 1970 年之后的完整时段。')
  return seconds
}

/** Arithmetic mean of returned samples, not a time-weighted mean or SLA. */
export function summarizeSeries(values: (number | null)[]) {
  let count = 0, min = Infinity, max = -Infinity, mean = 0
  for (const value of values) {
    if (value === null || !Number.isFinite(value)) continue
    count++
    min = Math.min(min, value); max = Math.max(max, value)
    mean = mean * ((count-1)/count) + value/count
  }
  const last = values.at(-1)
  return { count, total: values.length, last: last != null && Number.isFinite(last) ? last : null,
    min: count ? min : null, max: count ? max : null, mean: count ? mean : null }
}
