import assert from 'node:assert/strict'
import { test } from 'node:test'
import { appendLiveSample, chartDefinition, collectionStep, emptySeries, historySeries, plotSeries } from '../src/features/metrics/model/chartSeries.ts'
import { chartCSV } from '../src/chartExport.ts'
import { summarizeSeries } from '../src/chartInspection.ts'

const chart = {
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
const response = {
  id: chart.id, units: chart.units, after: 5, before: 10, view_update_every: 5,
  dimension_ids: ['hidden', 'b', 'a'], dimension_names: ['隐藏', '写入', '读取'],
  dimension_anomaly: [0, 100, 0],
  result: { labels: ['time', '隐藏', '写入', '读取'], data: [[5, 99, 10, 0], [10, 100, null, -4]] },
}

test('empty snapshots are independent and render no fabricated samples', () => {
  const first = emptySeries()
  const second = emptySeries()
  first.times.push(1)
  first.values.push([4])
  assert.deepEqual(second, { node: '', times: [], dimensions: [], values: [], anomaly: {}, anomalyBits: [], sampleStep: null })
  assert.deepEqual(plotSeries(second, false), [[]])
  assert.deepEqual(plotSeries(second, true), [[]])
})

test('empty history retains visible dimension identity without inventing zero rows', () => {
  const series = historySeries(chart, { ...response, result: { labels: response.result.labels, data: [] } }, 'remote-empty')
  assert.equal(series.node, 'remote-empty')
  assert.deepEqual(series.dimensions, ['a', 'b', 'missing'])
  assert.deepEqual(series.times, [])
  assert.deepEqual(series.values, [[], [], []])
  assert.deepEqual(plotSeries(series, true), [[], [], [], []])
})

test('all-hidden definitions retain timestamps but expose no visible series', () => {
  const hidden = { ...chart, dimensions: chart.dimensions.map(dimension => ({ ...dimension, hidden: true })) }
  const series = historySeries(hidden, response, 'local')
  assert.deepEqual(series.dimensions, [])
  assert.deepEqual(series.values, [])
  assert.deepEqual(series.anomalyBits, [])
  assert.deepEqual(plotSeries(series, true), [[5, 10]])
})

test('truncated and non-finite history cells normalize to null instead of leaking undefined into plots', () => {
  const malformed = { ...response, result: { labels: response.result.labels, data: [[5], [10, 1, NaN, Infinity], [15, 1, -Infinity, 0]] } }
  const series = historySeries(chart, malformed, 'local')
  assert.deepEqual(series.times, [5, 10, 15])
  assert.deepEqual(series.values, [[null, null, 0], [null, null, null], [null, null, null]])
  assert.deepEqual(plotSeries(series, true), [[5, 10, 15], [null, null, null], [null, null, null], [null, null, 0]])
})

test('history sampling intervals accept finite positive values and otherwise stay unknown', () => {
  for (const view_update_every of [undefined, null, 0, -1, NaN, Infinity]) {
    assert.equal(historySeries(chart, { ...response, view_update_every }, 'local').sampleStep, null)
  }
  assert.equal(historySeries(chart, { ...response, view_update_every: 0.5 }, 'local').sampleStep, 0.5)
})

test('accepted series align by dimension ID and retain missing rows, zero values and node provenance', () => {
  const original = structuredClone(response)
  const series = historySeries(chart, response, 'remote-a')
  assert.deepEqual(series, {
    node: 'remote-a', times: [5, 10], dimensions: ['a', 'b', 'missing'],
    values: [[0, -4], [10, null], [null, null]],
    anomaly: { hidden: 0, b: 100, a: 0 }, anomalyBits: [0, 1, 0], sampleStep: 5,
  })
  appendLiveSample(series, 15, { a: 1 }, 300, collectionStep(chart))
  assert.deepEqual(response, original)
})

test('unit mismatches never produce a usable snapshot, including omitted units', () => {
  for (const units of ['%', undefined, null, '', 'mib']) {
    assert.throws(() => historySeries(chart, { ...response, units }, 'local'), /采样单位与图表定义不一致/)
  }
})

test('invalid live timestamps cannot poison ordering or append partially aligned samples', () => {
  const series = historySeries(chart, response, 'local')
  const before = structuredClone(series)
  for (const timestamp of [NaN, Infinity, -Infinity]) {
    assert.equal(appendLiveSample(series, timestamp, { a: 2 }, 300, 5), false)
    assert.deepEqual(series, before)
  }
})

test('live values normalize missing and non-finite cells and ignore inherited object keys', () => {
  const series = historySeries(chart, response, 'local')
  const values = Object.assign(Object.create({ missing: 99 }), { a: NaN, b: Infinity })
  assert.equal(appendLiveSample(series, 15, values, 300, 5), true)
  assert.deepEqual(series.values.map(values => values.at(-1)), [null, null, null])
  assert.equal(appendLiveSample(series, 20, { a: 0, b: -4 }, 300, 5), true)
  assert.deepEqual(series.values.map(values => values.at(-1)), [0, -4, null])
})

test('the exact collection gap threshold does not add a missing row', () => {
  const series = historySeries(chart, response, 'local')
  appendLiveSample(series, 20, { a: 1 }, 300, 5)
  assert.deepEqual(series.times, [5, 10, 20])
  appendLiveSample(series, 31, { a: 2 }, 300, 5)
  assert.deepEqual(series.times, [5, 10, 20, 26, 31])
})

test('range trimming keeps the inclusive lower endpoint and aligns every visible dimension', () => {
  const series = historySeries(chart, response, 'local')
  appendLiveSample(series, 15, { a: 1, b: 2 }, 10, 5)
  assert.deepEqual(series.times, [5, 10, 15])
  appendLiveSample(series, 20, { a: 3 }, 10, 5)
  assert.deepEqual(series.times, [10, 15, 20])
  assert.deepEqual(series.values, [[-4, 1, 3], [null, 2, null], [null, null, null]])
})

test('live tails insert one missing collection row and reject replayed samples', () => {
  const series = historySeries(chart, response, 'local')
  assert.equal(appendLiveSample(series, 25, { a: 0, b: 7 }, 300, 5), true)
  assert.deepEqual(series.times, [5, 10, 20, 25])
  assert.deepEqual(series.values, [[0, -4, null, 0], [10, null, null, 7], [null, null, null, null]])
  const accepted = structuredClone(series)
  assert.equal(appendLiveSample(series, 25, { a: 999 }, 300, 5), false)
  assert.equal(appendLiveSample(series, 24, { a: 999 }, 300, 5), false)
  assert.deepEqual(series, accepted)
})

test('live tails bound all dimensions together and never expand downsampled long history', () => {
  const series = historySeries(chart, response, 'local')
  for (let timestamp = 11; timestamp <= 1300; timestamp++) appendLiveSample(series, timestamp, { a: timestamp }, 1200, 1)
  assert.equal(series.times.length, 1200)
  assert.equal(series.times[0], 101)
  assert.equal(series.values.every(values => values.length === 1200), true)
  appendLiveSample(series, 1310, { a: 1310 }, 10, 1)
  assert.deepEqual(series.times, [1300, 1309, 1310])
  assert.deepEqual(series.values[0], [1300, null, 1310])
  const before = structuredClone(series)
  assert.equal(appendLiveSample(series, 1400, { a: 1400 }, 86400, 1), false)
  assert.deepEqual(series, before)
})

test('stack projection leaves raw export and inspection values unchanged across redraws', () => {
  const series = historySeries(chart, response, 'local')
  const before = structuredClone(series)
  const csv = chartCSV(chart, series.node, series.times, series.dimensions, series.values)
  assert.deepEqual(plotSeries(series, true), [[5, 10], [null, null], [10, null], [0, -4]])
  assert.deepEqual(plotSeries(series, false), [[5, 10], [0, -4], [10, null], [null, null]])
  assert.deepEqual(series, before)
  assert.equal(chartCSV(chart, series.node, series.times, series.dimensions, series.values), csv)
  assert.deepEqual(summarizeSeries(series.values[0]), { count: 2, total: 2, last: -4, min: -4, max: 0, mean: -2 })
  assert.deepEqual(summarizeSeries(series.values[1]), { count: 1, total: 2, last: null, min: 10, max: 10, mean: 10 })
  appendLiveSample(series, 15, { a: 2, b: 3, missing: -1 }, 300, 5)
  assert.deepEqual(plotSeries(series, true), [[5, 10, 15], [null, null, 4], [10, null, 5], [0, -4, 2]])
  assert.deepEqual(series.values, [[0, -4, 2], [10, null, 3], [null, null, -1]])
})

test('historical definitions ignore current anomaly flags but retain structural changes', () => {
  const flagged = { ...chart, anomaly: true, dimensions: chart.dimensions.map(dimension => ({ ...dimension, anomaly: true })) }
  assert.equal(chartDefinition(flagged, true), chartDefinition(chart, true))
  assert.notEqual(chartDefinition(flagged, false), chartDefinition(chart, false))
  assert.notEqual(chartDefinition({ ...chart, units: '%' }, true), chartDefinition(chart, true))
  assert.notEqual(chartDefinition({ ...chart, dimensions: chart.dimensions.slice(1) }, true), chartDefinition(chart, true))
})
