import { test, expect } from '@playwright/test'
import type { Chart, DataResponse } from '../src/api/monitoring/types'
import { appendLiveSample, chartDefinition, collectionStep, historySeries, plotSeries } from '../src/features/metrics/model/chartSeries'

const chart: Chart = {
  id: 'test.series', context: 'test', family: 'test', title: '采样模型', units: 'MiB',
  chart_type: 'stacked', priority: 1, update_every: 5, plugin: 'test', module: 'test',
  labels: null, first_entry: 1, last_entry: 10,
  dimensions: [
    { id: 'a', name: '读取', algorithm: 'absolute' },
    { id: 'b', name: '写入', algorithm: 'absolute' },
    { id: 'missing', name: '未返回', algorithm: 'absolute' },
    { id: 'hidden', name: '隐藏', algorithm: 'absolute', hidden: true },
  ],
}
const response: DataResponse = {
  id: chart.id, units: chart.units, after: 5, before: 10, view_update_every: 5,
  dimension_ids: ['hidden', 'b', 'a'], dimension_names: ['隐藏', '写入', '读取'],
  dimension_anomaly: [0, 100, 0],
  result: { labels: ['time', '隐藏', '写入', '读取'], data: [[5, 99, 10, 0], [10, 100, null, -4]] },
}

test('accepted series align by dimension ID and retain missing rows, zero values and node provenance', () => {
  const original = structuredClone(response)
  const series = historySeries(chart, response, 'remote-a')
  expect(series).toEqual({
    node: 'remote-a', times: [5, 10], dimensions: ['a', 'b', 'missing'],
    values: [[0, -4], [10, null], [null, null]],
    anomaly: { hidden: 0, b: 100, a: 0 }, anomalyBits: [0, 1, 0], sampleStep: 5,
  })
  appendLiveSample(series, 15, { a: 1 }, 300, collectionStep(chart))
  expect(response).toEqual(original)
})

test('unit mismatches never produce a usable snapshot, including omitted units', () => {
  for (const units of ['%', undefined]) {
    expect(() => historySeries(chart, { ...response, units } as DataResponse, 'local')).toThrow('采样单位与图表定义不一致')
  }
})

test('live tails insert one missing collection row and reject replayed samples', () => {
  const series = historySeries(chart, response, 'local')
  expect(appendLiveSample(series, 25, { a: 0, b: 7 }, 300, 5)).toBe(true)
  expect(series.times).toEqual([5, 10, 20, 25])
  expect(series.values).toEqual([[0, -4, null, 0], [10, null, null, 7], [null, null, null, null]])
  const accepted = structuredClone(series)
  expect(appendLiveSample(series, 25, { a: 999 }, 300, 5)).toBe(false)
  expect(appendLiveSample(series, 24, { a: 999 }, 300, 5)).toBe(false)
  expect(series).toEqual(accepted)
})

test('live tails bound all dimensions together and never expand downsampled long history', () => {
  const series = historySeries(chart, response, 'local')
  for (let timestamp = 11; timestamp <= 1300; timestamp++) appendLiveSample(series, timestamp, { a: timestamp }, 1200, 1)
  expect(series.times).toHaveLength(1200)
  expect(series.times[0]).toBe(101)
  expect(series.values.every(values => values.length === 1200)).toBe(true)
  appendLiveSample(series, 1310, { a: 1310 }, 10, 1)
  expect(series.times).toEqual([1300, 1309, 1310])
  expect(series.values[0]).toEqual([1300, null, 1310])
  const before = structuredClone(series)
  expect(appendLiveSample(series, 1400, { a: 1400 }, 86400, 1)).toBe(false)
  expect(series).toEqual(before)
})

test('stack projection leaves raw export and inspection values unchanged across redraws', () => {
  const series = historySeries(chart, response, 'local')
  const before = structuredClone(series)
  expect(plotSeries(series, true)).toEqual([[5, 10], [null, null], [10, null], [0, -4]])
  expect(plotSeries(series, false)).toEqual([[5, 10], [0, -4], [10, null], [null, null]])
  expect(series).toEqual(before)
  appendLiveSample(series, 15, { a: 2, b: 3, missing: -1 }, 300, 5)
  expect(plotSeries(series, true)).toEqual([[5, 10, 15], [null, null, 4], [10, null, 5], [0, -4, 2]])
  expect(series.values).toEqual([[0, -4, 2], [10, null, 3], [null, null, -1]])
})

test('historical definitions ignore current anomaly flags but retain structural changes', () => {
  const flagged = { ...chart, anomaly: true, dimensions: chart.dimensions.map(dimension => ({ ...dimension, anomaly: true })) }
  expect(chartDefinition(flagged, true)).toBe(chartDefinition(chart, true))
  expect(chartDefinition(flagged, false)).not.toBe(chartDefinition(chart, false))
  expect(chartDefinition({ ...chart, units: '%' }, true)).not.toBe(chartDefinition(chart, true))
  expect(chartDefinition({ ...chart, dimensions: chart.dimensions.slice(1) }, true)).not.toBe(chartDefinition(chart, true))
})
