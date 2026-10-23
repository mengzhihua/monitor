import { expect, test, type Page } from '@playwright/test'
import type { PluginCatalog } from '../src/api/plugins/types'
import { workspace } from './mobile-navigation'

const platform = { api_version: 1, scope: 'local', capabilities: ['plugins.catalog.v1', 'plugins.control.v1'], plugin_protocols: ['plugins.d/v1'] }
function fixture(): PluginCatalog {
  return { schema_version: 1, revision: 4, running: true, diagnostics: [{ path: '/plugins/broken/plugin.json', error: '不支持的协议' }], plugins: [
    { name: 'demo', command: '/plugins/demo/run.plugin', state: 'running', source: 'manifest', locked: false,
      enabled: true, pid: 120, update_every: 1, restarts: 2, started: 100,
      manifest: { api_version: 1, id: 'demo', name: '演示采集器', version: '1.2.0', description: '本机插件平台测试', protocol: 'plugins.d/v1', capabilities: ['metrics'] },
      stats: { lines: 12, samples: 4, charts: 1, errors: 1, last_data: 110, last_error: '历史解析错误' } },
    { name: 'locked', command: '/plugins/locked.plugin', state: 'disabled', source: 'configured', locked: true,
      enabled: false, update_every: 5, restarts: 0, stats: { lines: 0, samples: 0, charts: 0, errors: 0 } },
  ] }
}
async function metadata(page: Page, capabilities = platform.capabilities) {
  await page.route('**/api/v1/info', async route => {
    const response = await route.fetch()
    await route.fulfill({ json: { ...await response.json(), mode: 'hub', platform: { ...platform, capabilities } } })
  })
  await page.route('**/api/v1/nodes', route => route.fulfill({ json: { nodes: [
    { id: '', hostname: 'browser-test', local: true, status: 'live', alarms: {}, charts_count: 3 },
    { id: 'remote-plugin-test', hostname: '远端节点', local: false, status: 'live', alarms: {}, charts_count: 3 },
  ] } }))
  await page.route('**/api/v1/charts*', async route => {
    const url = new URL(route.request().url())
    url.searchParams.delete('node')
    const response = await route.fetch({ url: url.href })
    await route.fulfill({ response })
  })
}

test('local plugin catalog shows metadata and locks while actions use revisions without node parameters', async ({ page }, info) => {
  await metadata(page)
  let catalog = fixture()
  const calls: { url: string; body?: Record<string, unknown> }[] = []
  await page.route('**/api/v1/plugins', route => { calls.push({ url: route.request().url() }); return route.fulfill({ json: catalog }) })
  await page.route('**/api/v1/plugins/actions', route => {
    const body = route.request().postDataJSON(); calls.push({ url: route.request().url(), body })
    catalog = { ...catalog, revision: catalog.revision + 1, plugins: catalog.plugins.map(plugin => plugin.name !== body.name ? plugin : { ...plugin, enabled: body.action !== 'disable', state: body.action === 'disable' ? 'disabled' : 'running' }) }
    return route.fulfill({ json: catalog })
  })
  await page.goto('/?view=plugins&node=remote-plugin-test&token=browser-test-token')
  const panel = page.getByRole('region', { name: '本机插件管理', exact: true })
  await expect(panel).toBeVisible()
  await expect(page.locator('.node-select-btn')).toContainText('远端节点')
  await expect(page.locator('.workspace-context')).toContainText('本机 · browser-test')
  const plugin = panel.getByRole('article', { name: '演示采集器' })
  await expect(plugin).toContainText('1.2.0')
  await expect(plugin).toContainText('清单插件包')
  await expect(panel.getByRole('region', { name: '插件目录诊断' })).toContainText('/plugins/broken/plugin.json')
  await expect(panel.getByRole('article', { name: 'locked' }).getByRole('button', { name: '启用', exact: true })).toBeDisabled()
  await plugin.getByRole('button', { name: '停用', exact: true }).click()
  await expect(plugin.getByRole('button', { name: '启用', exact: true })).toBeEnabled()
  await plugin.getByRole('button', { name: '启用', exact: true }).click()
  await expect(plugin.getByRole('button', { name: '停用', exact: true })).toBeEnabled()
  await plugin.getByRole('button', { name: '重启', exact: true }).click()
  await expect(panel).toContainText('目录版本 7')
  await panel.getByRole('button', { name: '重新扫描', exact: true }).click()
  await expect(panel).toContainText('目录版本 8')
  expect(calls.filter(call => call.body).map(call => call.body)).toEqual([
    { name: 'demo', action: 'disable', if_revision: 4 }, { name: 'demo', action: 'enable', if_revision: 5 },
    { name: 'demo', action: 'restart', if_revision: 6 }, { action: 'rescan', if_revision: 7 },
  ])
  expect(calls.every(call => !new URL(call.url).searchParams.has('node'))).toBe(true)
  expect(await panel.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true)
  await page.evaluate(() => {
    if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
    window.scrollTo(0, 0)
  })
  await page.screenshot({ path: info.outputPath('plugin-catalog.png'), fullPage: true })
})

test('viewers and servers without capability never mount or fetch the plugin manager', async ({ page }) => {
  let requests = 0
  await metadata(page)
  await page.route('**/api/v1/plugins*', route => { requests++; return route.fulfill({ json: fixture() }) })
  await page.goto('/?view=plugins&token=browser-viewer-token')
  await expect(page.getByText('当前节点或账号暂不可使用此模块。')).toBeVisible()
  expect(requests).toBe(0)
  await metadata(page, [])
  await page.goto('/?view=plugins&token=browser-test-token')
  await expect(page.getByText('当前节点或账号暂不可使用此模块。')).toBeVisible()
  expect(requests).toBe(0)
})

test('failed refresh freezes writes and a revision conflict refreshes before the next action', async ({ page }) => {
  await metadata(page)
  let fail = false, revision = 4, mutations = 0
  const revisions: number[] = []
  await page.route('**/api/v1/plugins', route => fail ? route.fulfill({ status: 503, body: 'fixture unavailable' }) : route.fulfill({ json: { ...fixture(), revision } }))
  await page.route('**/api/v1/plugins/actions', route => {
    revisions.push(route.request().postDataJSON().if_revision); mutations++
    if (mutations === 1) { revision = 8; return route.fulfill({ status: 409, body: 'revision conflict' }) }
    return route.fulfill({ json: { ...fixture(), revision: ++revision } })
  })
  await page.goto('/?view=plugins&token=browser-test-token')
  const panel = page.getByRole('region', { name: '本机插件管理', exact: true })
  const restart = panel.getByRole('article', { name: '演示采集器' }).getByRole('button', { name: '重启', exact: true })
  await expect(restart).toBeEnabled()
  fail = true
  await panel.getByRole('button', { name: '刷新目录' }).click()
  await expect(panel).toContainText('目录快照已过期')
  await expect(restart).toBeDisabled()
  expect(mutations).toBe(0)
  fail = false
  await panel.getByRole('button', { name: '刷新目录' }).click()
  await expect(restart).toBeEnabled()
  await restart.click()
  await expect(panel).toContainText('目录版本 8')
  await expect(panel).toContainText('请核对状态后重试')
  await expect(restart).toBeEnabled()
  await restart.click()
  await expect(panel).toContainText('目录版本 9')
  expect(revisions).toEqual([4, 8])
})

test('manager shutdown and permission loss clear actionable state', async ({ page }) => {
  await metadata(page)
  let status = 200
  await page.route('**/api/v1/plugins', route => status === 200
    ? route.fulfill({ json: { ...fixture(), running: false } }) : route.fulfill({ status, body: 'denied' }))
  await page.goto('/?view=plugins&token=browser-test-token')
  const panel = page.getByRole('region', { name: '本机插件管理', exact: true })
  await expect(panel).toContainText('插件管理器未运行')
  await expect(panel.getByRole('button', { name: '重新扫描' })).toBeDisabled()
  status = 403
  await panel.getByRole('button', { name: '刷新目录' }).click()
  await expect(panel.getByRole('alert')).toContainText('无权管理本机插件')
  await expect(panel.getByRole('article')).toHaveCount(0)
})

test('catalog-only capability keeps an empty plugin manager read-only', async ({ page }) => {
  await metadata(page, ['plugins.catalog.v1'])
  await page.route('**/api/v1/plugins', route => route.fulfill({ json: { ...fixture(), plugins: [], diagnostics: [] } }))
  await page.goto('/?view=plugins&token=browser-test-token')
  const panel = page.getByRole('region', { name: '本机插件管理', exact: true })
  await expect(panel).toContainText('尚未发现插件')
  await expect(panel).toContainText('仅开放目录查看')
  await expect(panel.getByRole('button', { name: '重新扫描' })).toBeDisabled()
  await expect(panel.getByRole('button', { name: '刷新目录' })).toBeEnabled()
})

test('pending actions block duplicate writes and navigation; an expired session clears plugin data', async ({ page }) => {
  await metadata(page)
  let expired = false, mutations = 0
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/v1/plugins', route => expired ? route.fulfill({ status: 401, body: 'expired' }) : route.fulfill({ json: fixture() }))
  await page.route('**/api/v1/plugins/actions', async route => {
    mutations++
    await gate
    await route.fulfill({ json: { ...fixture(), revision: 5 } })
  })
  await page.goto('/?view=plugins&token=browser-test-token')
  const panel = page.getByRole('region', { name: '本机插件管理', exact: true })
  const restart = panel.getByRole('article', { name: '演示采集器' }).getByRole('button', { name: '重启', exact: true })
  await restart.click()
  await expect(restart).toBeDisabled()
  await expect(panel.getByRole('button', { name: '重新扫描' })).toBeDisabled()
  await workspace(page, 'charts')
  await expect(panel).toBeVisible()
  expect(new URL(page.url()).searchParams.get('view')).toBe('plugins')
  expect(mutations).toBe(1)
  release()
  await expect(panel).toContainText('目录版本 5')
  expired = true
  await panel.getByRole('button', { name: '刷新目录' }).click()
  await expect(panel.getByRole('alert')).toContainText('登录已失效')
  await expect(panel.getByRole('article')).toHaveCount(0)
  await expect(panel.getByRole('button', { name: '重新扫描' })).toBeDisabled()
})

test('hidden plugin workspaces pause requests and leaving cancels a pending read', async ({ page }) => {
  await page.clock.install()
  await metadata(page)
  let requests = 0, hold = false
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  const aborted: string[] = []
  page.on('requestfailed', request => { if (request.url().endsWith('/api/v1/plugins')) aborted.push(request.url()) })
  await page.route('**/api/v1/plugins', async route => {
    requests++
    if (hold) await gate
    await route.fulfill({ json: fixture() }).catch(() => {})
  })
  await page.goto('/?view=plugins&token=browser-test-token')
  const panel = page.getByRole('region', { name: '本机插件管理', exact: true })
  await expect(panel).toContainText('目录版本 4')
  await page.evaluate(() => { Object.defineProperty(document, 'hidden', { configurable: true, value: true }); document.dispatchEvent(new Event('visibilitychange')) })
  const before = requests
  await page.clock.fastForward(30000)
  expect(requests).toBe(before)
  await page.evaluate(() => { Object.defineProperty(document, 'hidden', { configurable: true, value: false }); document.dispatchEvent(new Event('visibilitychange')) })
  await expect.poll(() => requests).toBeGreaterThan(before)
  await expect(panel.getByRole('button', { name: '刷新目录' })).toBeEnabled()
  hold = true
  await panel.getByRole('button', { name: '刷新目录' }).click()
  await expect(panel).toContainText('正在读取插件目录')
  await workspace(page, 'charts')
  await expect.poll(() => aborted.length).toBeGreaterThan(0)
  release()
  await expect(panel).toHaveCount(0)
  await expect(page.locator('.metrics-workspace')).toBeVisible()
})
