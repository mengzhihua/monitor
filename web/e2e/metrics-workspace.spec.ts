import { expect, test, type Page } from '@playwright/test'
import type { Chart } from '../src/api'
import { encodeTimeline, timelineKey } from '../src/chartTimeline'
import { indexMetrics, metricDomain, metricMatches } from '../src/features/metrics/metricCatalog'

function chart(id: string, context = id, labels: Record<string, string> = {}): Chart {
  return {
    id, context, title: `指标 ${id}`, family: 'fixture-family', units: '%', chart_type: 'line', priority: 100,
    update_every: 1, plugin: '', module: '', labels, dimensions: [{ id: 'value', name: '数值', algorithm: 'absolute' }],
    first_entry: 1, last_entry: 1,
  }
}
const hostCharts = Array.from({ length: 29 }, (_, i) => chart(`system.fixture_${String(i).padStart(2, '0')}`, 'system.cpu'))
const charts = [
  ...hostCharts,
  chart('net.interface_01', 'net.net', { interface: 'uplink-office' }),
  chart('disk.capacity', 'disk.space'),
  chart('snmp.target.floor1.ups.charge', 'snmp.ups.charge', { target: 'floor1-ups', site: '北楼机房' }),
  chart('redfish.system_health_state.rack', 'redfish.system_health_state', { target: 'rack-bmc' }),
  chart('apps.workers', 'apps.cpu'),
  chart('redis.cache', 'redis.memory'),
  chart('docker.container', 'docker.cpu'),
  chart('custom.extension', 'custom.probe', { business: '支付业务' }),
]

test('semantic classification uses stable contexts, legacy IDs and preserves unrecognized collectors', () => {
  expect(metricDomain(chart('custom.instance', 'redis.memory'))).toBe('middleware')
  expect(metricDomain(chart('disk_await.sda', ''))).toBe('storage')
  expect(metricDomain(chart('snmp_topology.lldp_neighbor.core', ''))).toBe('devices')
  expect(metricDomain(chart('k8s_kubelet.pods', ''))).toBe('containers')
  expect(metricDomain(chart('nvidia_smi.gpu', ''))).toBe('devices')
  expect(metricDomain(chart('custom.instance', 'unrelated.redis', { name: 'Redis' }))).toBe('other')
  const indexed = indexMetrics([chart('custom.instance', 'redis.memory', { target: 'North-Rack' })])
  expect(metricMatches(indexed[0]!, 'REDIS North-Rack')).toBe(true)
  expect(metricMatches(indexed[0]!, 'redis missing-rack')).toBe(false)
})

async function fixtures(page: Page, remote = false) {
  const calls: URL[] = []
  await page.route('**/api/v1/charts*', route => {
    const node = new URL(route.request().url()).searchParams.get('node')
    const list = node ? hostCharts.slice(0, 14).map(c => ({ ...c, id: c.id.replace('fixture', 'remote') })) : charts
    return route.fulfill({ json: { charts: Object.fromEntries(list.map(c => [c.id, c])) } })
  })
  await page.route('**/api/v1/data?*', route => {
    const url = new URL(route.request().url()); calls.push(url)
    const end = Number(url.searchParams.get('before')) || Math.floor(Date.now() / 1000)
    return route.fulfill({ json: { units: '%', dimension_ids: ['value'], result: { data: [[end - 1, 10], [end, 20]] } } })
  })
  if (remote) {
    await page.route('**/api/v1/info', async route => {
      const response = await route.fetch()
      return route.fulfill({ json: { ...await response.json(), mode: 'hub' } })
    })
    await page.route('**/api/v1/nodes', route => route.fulfill({ json: { nodes: [
      { id: '', hostname: 'Local catalog', local: true, status: 'live', alarms: {}, charts_count: charts.length },
      { id: 'catalog-remote', hostname: 'Remote catalog', local: false, status: 'live', alarms: {}, charts_count: 14 },
    ] } }))
  }
  return calls
}

test('metric catalog limits mounted charts and resets pagination after category or search changes', async ({ page }) => {
  await fixtures(page)
  await page.goto('/?view=charts&token=browser-test-token')
  const browser = page.getByLabel('指标工作区', { exact: true })
  const categories = browser.getByRole('navigation', { name: '指标分类' })
  await expect(browser.locator('.card')).toHaveCount(12)
  await expect(browser.locator('.card .id').first()).toHaveText('system.fixture_00')
  await browser.getByRole('button', { name: '下一页指标', exact: true }).click()
  await expect(browser.locator('.card')).toHaveCount(12)
  await expect(browser.locator('.card .id').first()).toHaveText('system.fixture_12')
  await expect(browser.getByRole('navigation', { name: '指标分页' })).toContainText('第 2 / 4 页')
  await categories.getByRole('button', { name: /^设备/ }).click()
  await expect(browser.locator('.card')).toHaveCount(2)
  await expect(browser.getByRole('heading', { name: '设备', exact: true })).toBeVisible()
  await expect(browser.getByRole('navigation', { name: '指标分页' })).toHaveCount(0)
  await categories.getByRole('button', { name: /^主机资源/ }).click()
  await expect(browser.locator('.card .id').first()).toHaveText('system.fixture_00')
  await browser.getByRole('button', { name: '下一页指标', exact: true }).click()
  await browser.getByPlaceholder('筛选图表…').fill('fixture_28')
  await expect(browser.locator('.card')).toHaveCount(1)
  await expect(browser.locator('.card .id')).toHaveText('system.fixture_28')
  await browser.getByRole('button', { name: '清除指标筛选', exact: true }).click()
  await expect(browser.locator('.card')).toHaveCount(12)
  await expect(browser.locator('.card .id').first()).toHaveText('system.fixture_00')
})

test('search finds context and device labels, keeps custom metrics and recovers from empty results', async ({ page }) => {
  await fixtures(page)
  await page.goto('/?view=charts&token=browser-test-token')
  const browser = page.getByLabel('指标工作区', { exact: true })
  const search = browser.getByPlaceholder('筛选图表…')
  await search.fill('北楼机房 floor1-ups')
  await expect(browser.locator('.card')).toHaveCount(1)
  await expect(browser.locator('.card .id')).toHaveText('snmp.target.floor1.ups.charge')
  await search.fill('net.net')
  await expect(browser.locator('.card .id')).toHaveText('net.interface_01')
  await search.fill('fixture-family 支付业务')
  await expect(browser.locator('.card .id')).toHaveText('custom.extension')
  await search.fill('no-such-metric-or-label')
  await expect(browser.locator('.card')).toHaveCount(0)
  await expect(browser.getByRole('heading', { name: '没有匹配的指标' })).toBeVisible()
  await browser.getByRole('button', { name: '查看全部指标', exact: true }).click()
  await expect(search).toHaveValue('')
  await expect(browser.locator('.card')).toHaveCount(12)
  await browser.getByRole('navigation', { name: '指标分类' }).getByRole('button', { name: /^其他指标/ }).click()
  await expect(browser.locator('.card .id')).toHaveText('custom.extension')
})

test('node switch resets the catalog page while preserving fixed history and node-scoped requests', async ({ page }) => {
  const fixed = 1767323045
  await page.addInitScript(({ key, value }) => sessionStorage.setItem(key, value), { key: timelineKey, value: encodeTimeline({ window: 300, end: fixed }) })
  const calls = await fixtures(page, true)
  await page.goto('/?view=charts&token=browser-test-token')
  const browser = page.getByLabel('指标工作区', { exact: true })
  await browser.getByRole('button', { name: '下一页指标', exact: true }).click()
  await expect(browser.locator('.card .id').first()).toHaveText('system.fixture_12')
  await page.locator('.node-select-btn').click()
  await page.getByRole('option', { name: /Remote catalog/ }).click()
  await expect(browser.getByRole('navigation', { name: '指标分页' })).toContainText('第 1 / 2 页')
  await expect(browser.locator('.card .id').first()).toHaveText('system.remote_00')
  await browser.locator('.card').first().scrollIntoViewIfNeeded()
  await expect.poll(() => calls.some(url => url.searchParams.get('node') === 'catalog-remote' && url.searchParams.get('chart') === 'system.remote_00' && url.searchParams.get('before') === String(fixed))).toBe(true)
  await expect(browser.getByRole('region', { name: '统一查看时间' })).toContainText('历史查看')
  expect(await page.evaluate(key => JSON.parse(sessionStorage.getItem(key)!).end, timelineKey)).toBe(fixed)
})

test('narrow metric workspace contains categories, controls and charts without horizontal overflow', async ({ page }, info) => {
  await page.setViewportSize({ width: 320, height: 740 })
  await fixtures(page)
  await page.goto('/?view=charts&token=browser-test-token')
  const browser = page.getByLabel('指标工作区', { exact: true })
  const categories = browser.getByRole('navigation', { name: '指标分类' })
  await categories.getByRole('button', { name: /^设备/ }).click()
  await expect(browser.locator('.card')).toHaveCount(2)
  for (const button of await categories.getByRole('button').all()) {
    const bounds = await button.boundingBox()
    expect(bounds).not.toBeNull()
    expect(bounds!.height).toBeGreaterThanOrEqual(44)
  }
  // Categories scroll inside their own row instead of expanding the document.
  expect(await categories.evaluate(el => el.scrollWidth > el.clientWidth)).toBe(true)
  const selectedBounds = await categories.getByRole('button', { name: /^设备/ }).boundingBox()
  expect(selectedBounds!.x).toBeGreaterThanOrEqual(0)
  expect(selectedBounds!.x + selectedBounds!.width).toBeLessThanOrEqual(320)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await categories.scrollIntoViewIfNeeded()
  await page.screenshot({ path: info.outputPath('metrics-workspace-mobile.png'), fullPage: true })
  const collector = browser.locator('.collector-status')
  await expect(collector).not.toHaveAttribute('open', '')
  await collector.locator('summary').click()
  await expect(collector).toHaveAttribute('open', '')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})
