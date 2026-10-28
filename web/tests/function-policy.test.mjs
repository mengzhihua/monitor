import assert from 'node:assert/strict'
import { test } from 'node:test'
import { functionRequest, pluginRequestTimeout } from '../src/features/functions/model/functionPolicy.ts'

test('plugin execution needs explicit intent while view refreshes remain reads', () => {
  assert.equal(functionRequest('plugin.demo.inspect', 'cpu'), null)
  assert.equal(functionRequest('plugin.demo.inspect', 'value'), null)
  assert.deepEqual(functionRequest('plugin.demo.inspect', 'value', true), { name: 'plugin.demo.inspect', args: {} })
  assert.equal(functionRequest('', 'cpu', true), null)
})

test('built-in diagnostics preserve server sorting without adding plugin arguments', () => {
  for (const name of ['processes', 'services', 'network-connections']) {
    assert.deepEqual(functionRequest(name, 'cpu'), { name, args: { sort: 'cpu' } })
  }
  assert.deepEqual(functionRequest('mounts', 'cpu'), { name: 'mounts', args: {} })
})

test('plugin request deadline permits the declared runtime and bounds invalid values', () => {
  for (const timeout of [undefined, 0, -1, NaN, Infinity]) {
    assert.equal(pluginRequestTimeout({ name: 'plugin.demo.inspect', help: '', timeout }), 15_000)
  }
  assert.equal(pluginRequestTimeout({ name: 'plugin.demo.inspect', help: '', timeout: 2 }), 7_000)
  assert.equal(pluginRequestTimeout({ name: 'plugin.demo.inspect', help: '', timeout: 60 }), 65_000)
  assert.equal(pluginRequestTimeout({ name: 'plugin.demo.inspect', help: '', timeout: 600 }), 65_000)
})
