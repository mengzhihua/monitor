import { test, expect, type Page } from '@playwright/test'

const chartList = Object.fromEntries(['system.cpu'].map(id => [id, {
  id, title: id, context: id, family: 'test', units: '%', chart_type: 'line', priority: 1, update_every: 1,
  dimensions: [{ id: 'value', name: '数值' }], last_entry: 1,
}]))

let nodes: Array<Record<string, unknown>>
async function hubFixture(page: Page, deleteStatus = 204) {
  const deleted: string[] = []
  nodes = [
    { id: '', hostname: 'hub-local', os: 'linux', arch: 'amd64', version: 'test', local: true, status: 'live', first_seen: 1, last_seen: 2, alarms: {}, charts_count: 2 },
    { id: 'live-node', hostname: 'Live agent', os: 'linux', arch: 'amd64', version: 'test', local: false, status: 'live', first_seen: 1, last_seen: 2, alarms: {}, charts_count: 2 },
    { id: 'gone-node', hostname: 'Gone agent', os: 'linux', arch: 'amd64', version: 'test', local: false, status: 'offline', first_seen: 1, last_seen: 2, alarms: {}, charts_count: 2 },
  ]
  await page.route('**/api/v1/charts*', route => route.fulfill({ json: { charts: chartList } }))
  await page.route('**/api/v1/data?*', route => route.fulfill({ json: { units: '%', dimension_ids: ['value'], result: { data: [[1, 1], [2, 2]] } } }))
  await page.route('**/api/v1/info', async route => {
    const response = await route.fetch()
    await route.fulfill({ json: { ...await response.json(), mode: 'hub' } })
  })
  await page.route('**/api/v1/nodes*', route => {
    if (route.request().method() === 'DELETE') {
      deleted.push(new URL(route.request().url()).searchParams.get('node') ?? '')
      if (deleteStatus === 204) nodes = nodes.filter(n => n.id !== 'gone-node')
      return route.fulfill(deleteStatus === 204
        ? { status: 204 }
        : { status: deleteStatus, body: 'node is connected' })
    }
    return route.fulfill({ json: { now: Math.floor(Date.now() / 1000), nodes } })
  })
  return { deleted }
}

test('node cards offer deletion only for offline remote nodes', async ({ page }) => {
  await hubFixture(page)
  await page.goto('/?view=charts&token=browser-test-token')
  const cards = page.locator('.node-card')
  await expect(cards).toHaveCount(3)
  await expect(cards.filter({ hasText: 'Gone agent' }).getByRole('button', { name: '删除节点' })).toBeVisible()
  await expect(cards.filter({ hasText: 'Live agent' }).getByRole('button', { name: '删除节点' })).toHaveCount(0)
  await expect(cards.filter({ hasText: 'hub-local' }).getByRole('button', { name: '删除节点' })).toHaveCount(0)
})

test('forgetting an offline node confirms, sends DELETE and drops the card', async ({ page }) => {
  const { deleted } = await hubFixture(page)
  await page.goto('/?view=charts&token=browser-test-token')
  const messages: string[] = []
  page.on('dialog', async dialog => { messages.push(dialog.message()); await dialog.accept() })
  await page.locator('.node-card').filter({ hasText: 'Gone agent' }).getByRole('button', { name: '删除节点' }).click()
  expect(messages[0]).toContain('Gone agent')
  await expect.poll(() => deleted).toEqual(['gone-node'])
  await expect(page.locator('.node-card')).toHaveCount(2)
  await expect(page.locator('.node-card').filter({ hasText: 'Gone agent' })).toHaveCount(0)
})

test('cancelling the confirmation sends no DELETE', async ({ page }) => {
  const { deleted } = await hubFixture(page)
  await page.goto('/?view=charts&token=browser-test-token')
  page.on('dialog', dialog => dialog.dismiss())
  await page.locator('.node-card').filter({ hasText: 'Gone agent' }).getByRole('button', { name: '删除节点' }).click()
  await expect(page.locator('.node-card')).toHaveCount(3)
  expect(deleted).toEqual([])
})

test('a rejected delete surfaces the hub error in the banner', async ({ page }) => {
  await hubFixture(page, 409)
  await page.goto('/?view=charts&token=browser-test-token')
  page.on('dialog', dialog => dialog.accept())
  await page.locator('.node-card').filter({ hasText: 'Gone agent' }).getByRole('button', { name: '删除节点' }).click()
  await expect(page.locator('.banner').first()).toContainText(/删除节点失败/)
  await expect(page.locator('.node-card')).toHaveCount(3)
})
