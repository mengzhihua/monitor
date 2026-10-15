import { expect, test, type Page, type Route } from '@playwright/test'
import { openTools, workspace } from './mobile-navigation'

const node = { id: 'node-a', hostname: 'node-a', os: 'linux', arch: 'amd64', version: 'test', status: 'live', local: false,
  first_seen: 1, last_seen: 100, charts_count: 0, alarms: { warning: 0, critical: 0 } }
const template = { id: 'template-a', name: 'Template A', description: 'saved', updated: 1, rules: [] }
const inventory = { node_id: node.id, auto: {}, tags: {}, manual: { owner: 'saved-owner' }, effective: {}, updated: 1, sort_order: ['owner'] }
function gate() {
  let release!: () => void
  const wait = new Promise<void>(resolve => { release = resolve })
  return { wait, release }
}
async function fixture(page: Page, read?: (route: Route, url: URL) => Promise<boolean>) {
  await page.routeWebSocket('**/api/v1/live*', () => {})
  await page.route('**/api/v1/**', async route => {
    const url = new URL(route.request().url())
    if (await read?.(route, url)) return
    const replies: Record<string, unknown> = {
      '/api/v1/info': { version: 'test', mode: 'hub', uptime: 1, charts_count: 0, metrics_count: 0,
        host: { id: 'local', hostname: 'fixture', os: 'linux', arch: 'amd64', labels: {}, update_every: 1 },
        collectors: [], alarms: { warning: 0, critical: 0, normal: 0, silent: 0 }, user: { name: 'admin', role: 'admin' } },
      '/api/v1/charts': { charts: {} }, '/api/v1/functions': [], '/api/v1/alarms': { alarms: {} },
      '/api/v1/auth/oidc/status': { enabled: false }, '/api/v1/nodes': { nodes: [node] },
      '/api/v1/hub/templates': { templates: [template] }, '/api/v1/hub/spaces': { spaces: [] }, '/api/v1/hub/rooms': { rooms: [] },
      '/api/v1/hub/inventory': url.searchParams.has('node') ? inventory : { inventory: { [node.id]: inventory } },
      '/api/v1/manage/config': { path: '/tmp/fixture.yaml', yaml: 'global:\n  hostname: saved\n', updated: 1, writable: true },
    }
    await route.fulfill({ json: replies[url.pathname] ?? {} })
  })
}
async function openTool(page: Page, title: string) {
  await openTools(page)
  await page.getByTitle(title, { exact: true }).click()
}

test('a slow node drill cannot replace a newer workspace or discard its config draft', async ({ page }) => {
  const pending = gate()
  let started = false
  await fixture(page, async (route, url) => {
    if (url.pathname !== '/api/v1/charts' || url.searchParams.get('node') !== node.id) return false
    started = true
    await pending.wait
    await route.fulfill({ json: { charts: {} } })
    return true
  })
  await page.goto('/?view=nodes&token=browser-test-token')
  await page.getByRole('button', { name: '查看指标', exact: true }).click()
  await expect.poll(() => started).toBe(true)
  await openTool(page, '配置：本机与节点')
  const area = page.getByLabel('本机配置内容')
  await area.fill('global:\n  hostname: keep-this-draft\n')
  const completed = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/functions' && new URL(response.url()).searchParams.get('node') === node.id)
  pending.release()
  await completed
  // Let the awaited refresh and Vue render finish after the HTTP response.
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  await expect(area).toHaveValue('global:\n  hostname: keep-this-draft\n')
  expect(new URL(page.url()).searchParams.get('view')).toBe('config')
})

test('template drafts guard navigation and close, with one confirmation per action', async ({ page }) => {
  await fixture(page)
  await page.goto('/?view=templates&token=browser-test-token')
  const panel = page.getByRole('region', { name: '模板与资产', exact: true })
  await panel.getByRole('button', { name: '编辑', exact: true }).click()
  await panel.getByLabel('描述', { exact: true }).fill('unsaved-description')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  let prompts = 0
  const reject = async (dialog: import('@playwright/test').Dialog) => { prompts++; await dialog.dismiss() }
  page.on('dialog', reject)
  await workspace(page, 'charts')
  await expect(panel.getByLabel('描述', { exact: true })).toHaveValue('unsaved-description')
  expect(prompts).toBe(1)
  await panel.getByTitle('关闭', { exact: true }).click()
  await expect(panel.getByLabel('描述', { exact: true })).toHaveValue('unsaved-description')
  expect(prompts).toBe(2)
  page.off('dialog', reject)
  page.on('dialog', async dialog => { prompts++; await dialog.accept() })
  await workspace(page, 'charts')
  await expect(page.locator('.metrics-workspace')).toBeVisible()
  expect(prompts).toBe(3)
})

test('asset drafts guard cancellation and module navigation', async ({ page }) => {
  await fixture(page)
  await page.goto('/?view=templates&token=browser-test-token')
  const panel = page.getByRole('region', { name: '模板与资产', exact: true })
  await panel.getByRole('button', { name: '资产清单', exact: true }).click()
  await panel.getByRole('button', { name: '编辑', exact: true }).click()
  const owner = panel.locator('.editor .kv input').nth(1)
  await owner.fill('unsaved-owner')
  await owner.press('Tab')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  let prompts = 0
  page.on('dialog', async dialog => { prompts++; await dialog.dismiss() })
  await panel.getByRole('button', { name: '取消', exact: true }).click()
  await expect(owner).toHaveValue('unsaved-owner')
  await workspace(page, 'charts')
  await expect(owner).toHaveValue('unsaved-owner')
  expect(prompts).toBe(2)
})

test('a template save blocks navigation until the mutation settles', async ({ page }) => {
  const pending = gate()
  let saving = false
  await fixture(page, async (route, url) => {
    if (url.pathname !== '/api/v1/hub/templates' || route.request().method() !== 'PUT') return false
    saving = true
    await pending.wait
    await route.fulfill({ json: { ...template, updated: 2 } })
    return true
  })
  await page.goto('/?view=templates&token=browser-test-token')
  const panel = page.getByRole('region', { name: '模板与资产', exact: true })
  await panel.getByRole('button', { name: '编辑', exact: true }).click()
  await panel.getByLabel('描述', { exact: true }).fill('saving-description')
  await panel.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => saving).toBe(true)
  await workspace(page, 'charts')
  await expect(panel).toBeVisible()
  await expect(panel.getByTitle('关闭', { exact: true })).toBeDisabled()
  pending.release()
  await expect(panel.locator('.editor')).toHaveCount(0)
  await workspace(page, 'charts')
  await expect(page.locator('.metrics-workspace')).toBeVisible()
})

test('rejecting browser back preserves the history entries and the unsaved draft', async ({ page }) => {
  await fixture(page)
  await page.goto('/?view=charts&token=browser-test-token')
  await expect(page.locator('.metrics-workspace')).toBeVisible()
  await openTool(page, '日志')
  await expect(page.getByRole('heading', { name: /^日志/ })).toBeVisible()
  await openTool(page, '配置：本机与节点')
  const area = page.getByLabel('本机配置内容')
  await area.fill('global:\n  hostname: history-draft\n')
  let prompts = 0
  const reject = async (dialog: import('@playwright/test').Dialog) => { prompts++; await dialog.dismiss() }
  page.on('dialog', reject)
  await page.goBack()
  await expect.poll(() => page.evaluate(() => history.state?.['monitor.workspace']?.index)).toBe(2)
  await expect(area).toHaveValue('global:\n  hostname: history-draft\n')
  expect(new URL(page.url()).searchParams.get('view')).toBe('config')
  expect(prompts).toBe(1)
  page.off('dialog', reject)
  page.on('dialog', async dialog => { prompts++; await dialog.accept() })
  await page.goBack()
  await expect(page.getByRole('heading', { name: /^日志/ })).toBeVisible()
  expect(new URL(page.url()).searchParams.get('view')).toBe('logs')
  expect(prompts).toBe(2)
  await page.goBack()
  await expect(page.locator('.metrics-workspace')).toBeVisible()
  expect(new URL(page.url()).searchParams.get('view')).toBe('charts')
})

test('node selection updates the shareable URL and survives refresh without restoring the old URL node', async ({ page }) => {
  await fixture(page, async (route, url) => {
    if (url.pathname !== '/api/v1/nodes') return false
    await route.fulfill({ json: { nodes: [node, { ...node, id: 'node-b', hostname: 'node-b' }] } })
    return true
  })
  await page.goto('/?view=charts&token=browser-test-token&node=node-a')
  await expect(page.locator('.node-select-btn')).toContainText('node-a')
  await page.getByTitle('节点', { exact: true }).click()
  await page.getByRole('option').filter({ hasText: 'node-b' }).click()
  await expect(page.locator('.node-select-btn')).toContainText('node-b')
  expect(new URL(page.url()).searchParams.get('node')).toBe('node-b')
  expect(new URL(page.url()).searchParams.has('token')).toBe(false)
  await page.reload()
  await expect(page.locator('.node-select-btn')).toContainText('node-b')
  expect(new URL(page.url()).searchParams.get('node')).toBe('node-b')
})

test('removing a URL token preserves the workspace history metadata', async ({ page }) => {
  await page.addInitScript(() => history.replaceState({ marker: 'preserve-me', 'monitor.workspace': { session: 'fixture-history', index: 4 } }, '', location.href))
  await fixture(page)
  await page.goto('/?view=charts&token=browser-test-token')
  await expect(page.locator('.metrics-workspace')).toBeVisible()
  expect(new URL(page.url()).searchParams.has('token')).toBe(false)
  expect(await page.evaluate(() => history.state)).toMatchObject({ marker: 'preserve-me', 'monitor.workspace': { session: 'fixture-history', index: 4 } })
})

test('a failed chunk from an abandoned module cannot replace the current workspace', async ({ page }) => {
  await fixture(page)
  const pending = gate()
  let requested = false, finished = false
  await page.route('**/assets/LogsPanel-*.js', async route => {
    requested = true
    await pending.wait
    await route.abort('failed')
    finished = true
  })
  await page.goto('/?view=charts&token=browser-test-token')
  await expect(page.locator('.metrics-workspace')).toBeVisible()
  await openTool(page, '日志')
  await expect.poll(() => requested).toBe(true)
  await workspace(page, 'charts')
  await expect(page.locator('.metrics-workspace')).toBeVisible()
  pending.release()
  await expect.poll(() => finished).toBe(true)
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  await expect(page.locator('.metrics-workspace')).toBeVisible()
  await expect(page.getByRole('alert')).toHaveCount(0)
})

test('a current module chunk failure has a local error and other modules stay usable', async ({ page }) => {
  await fixture(page)
  await page.route('**/assets/LogsPanel-*.js', route => route.abort('failed'))
  await page.goto('/?view=logs&token=browser-test-token')
  await expect(page.getByRole('alert')).toContainText('当前模块加载失败')
  await expect(page.getByRole('button', { name: '刷新页面', exact: true })).toBeVisible()
  await workspace(page, 'charts')
  await expect(page.locator('.metrics-workspace')).toBeVisible()
  await expect(page.getByRole('alert')).toHaveCount(0)
})
