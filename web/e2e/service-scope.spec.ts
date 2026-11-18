import { expect, test } from '@playwright/test'

test('services keep their local scope when a remote node is selected', async ({ page }) => {
  await page.route('**/api/v1/info', route => route.fulfill({ json: {
    version: 'test', mode: 'hub', uptime: 10, charts_count: 0, metrics_count: 0,
    host: { id: 'hub', hostname: 'hub-local', os: 'linux', arch: 'amd64', labels: {}, update_every: 1 },
    collectors: [], alarms: null, user: { name: 'admin', role: 'admin' },
  } }))
  await page.route('**/api/v1/nodes*', route => route.fulfill({ json: { nodes: [{
    id: 'remote-agent', hostname: 'agent-one', os: 'linux', arch: 'amd64', version: 'test',
    local: false, status: 'live', first_seen: 1, last_seen: 100, charts_count: 0, alarms: {},
  }] } }))
  await page.route('**/api/v1/charts*', route => route.fulfill({ json: { charts: {} } }))
  await page.route('**/api/v1/functions*', route => route.fulfill({ json: [] }))
  await page.routeWebSocket('**/api/v1/live*', () => {})
  const requests: string[] = []
  await page.route(/\/api\/v1\/(services|topology|reports\/availability)(\?|$)/, route => {
    requests.push(route.request().url())
    return route.fulfill({ json: { from: 1, to: 2, services: [], edges: [], alarms: [] } })
  })
  await page.goto('/?view=services&token=browser-test-token')
  await expect(page.locator('.services')).toBeVisible()
  await expect(page.locator('.workspace-context')).toContainText('本机 · hub-local')
  await page.getByTitle('节点', { exact: true }).click()
  await page.getByRole('option', { name: /agent-one/ }).click()
  await expect(page.locator('.node-select-btn')).toContainText('agent-one')
  await expect(page.locator('.workspace-context')).toContainText('本机 · hub-local')
  await page.locator('.services').getByRole('button', { name: '刷新', exact: true }).click()
  await expect.poll(() => requests.length).toBeGreaterThanOrEqual(6)
  expect(requests.every(url => !new URL(url).searchParams.has('node'))).toBe(true)
})
