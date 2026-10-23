import assert from 'node:assert/strict'
import { test } from 'node:test'
import { allowsPluginAction, pluginAccess, pluginSource, pluginState } from '../src/features/plugins/model/pluginPolicy.ts'
import { workspaceKey, workspaceScopeLabel } from '../src/features/workspace/model/workspaceScope.ts'

const platform = { api_version: 1, scope: 'local', capabilities: ['plugins.catalog.v1', 'plugins.control.v1'], plugin_protocols: ['plugins.d/v1'] }
const catalog = { schema_version: 1, revision: 1, running: true, plugins: [
  { name: 'active', enabled: true, locked: false },
  { name: 'off', enabled: false, locked: false },
  { name: 'locked', enabled: false, locked: true },
], diagnostics: [] }

test('plugin access requires authentication, admin and an explicit supported local capability', () => {
  assert.deepEqual(pluginAccess(platform, 'admin', true), { read: true, control: true })
  for (const [value, role, authenticated] of [
    [undefined, 'admin', true], [platform, 'admin', false], [platform, 'viewer', true], [platform, 'troubleshooter', true],
    [{ ...platform, api_version: 2 }, 'admin', true], [{ ...platform, scope: 'node' }, 'admin', true],
    [{ ...platform, capabilities: ['plugins.control.v1'] }, 'admin', true],
  ]) assert.deepEqual(pluginAccess(value, role, authenticated), { read: false, control: false })
})

test('catalog-only access never implies process control', () => {
  assert.deepEqual(pluginAccess({ ...platform, capabilities: ['plugins.catalog.v1'] }, 'admin', true), { read: true, control: false })
})

test('a stopped manager prevents all actions, including rescan', () => {
  for (const value of [null, { ...catalog, running: false }]) {
    for (const action of ['rescan', 'enable', 'disable', 'restart']) assert.equal(allowsPluginAction(value, action, 'active'), false)
  }
})

test('process actions respect enablement and configuration locks', () => {
  assert.equal(allowsPluginAction(catalog, 'disable', 'active'), true)
  assert.equal(allowsPluginAction(catalog, 'restart', 'active'), true)
  assert.equal(allowsPluginAction(catalog, 'enable', 'active'), false)
  assert.equal(allowsPluginAction(catalog, 'enable', 'off'), true)
  assert.equal(allowsPluginAction(catalog, 'restart', 'off'), false)
  for (const action of ['enable', 'disable', 'restart']) assert.equal(allowsPluginAction(catalog, action, 'locked'), false)
  assert.equal(allowsPluginAction(catalog, 'rescan'), true)
  assert.equal(allowsPluginAction(catalog, 'enable', 'unknown'), false)
})

test('presentation distinguishes protocol/runtime states and tolerates future values', () => {
  assert.equal(pluginState('waiting'), '等待重启')
  assert.equal(pluginState('disabled'), '已停用')
  assert.equal(pluginState('future-state'), '未知状态')
  assert.equal(pluginState('constructor'), '未知状态')
  assert.equal(pluginSource('manifest'), '清单插件包')
  assert.equal(pluginSource('future-source'), '未知来源')
})

test('workspace scope keeps local/global modules stable and replaces node sessions', () => {
  assert.equal(workspaceKey('plugins', 'local', 'remote'), workspaceKey('plugins', 'local', ''))
  assert.equal(workspaceKey('operations', 'global', 'remote'), workspaceKey('operations', 'global', ''))
  assert.notEqual(workspaceKey('charts', 'node', 'remote'), workspaceKey('charts', 'node', ''))
  assert.equal(workspaceScopeLabel('local', 'hub-a', 'remote-b'), '本机 · hub-a')
  assert.equal(workspaceScopeLabel('global', 'hub-a', 'remote-b'), '全局视图')
  assert.equal(workspaceScopeLabel('node', 'hub-a', 'remote-b'), 'remote-b')
  assert.equal(workspaceScopeLabel('node', 'hub-a'), 'hub-a')
})
