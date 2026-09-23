import { test, expect } from '@playwright/test'

test('built-in dashboards use real charts, filter, range and responsive layout', async ({ page }, info) => {
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.goto('/?view=charts&token=browser-test-token')
  await page.getByRole('button', { name: '常用聚合看板', exact: true }).click()
  const panel = page.getByLabel('常用聚合看板', { exact: true })
  await expect(panel.getByRole('heading', { name: '研发总览', exact: true })).toBeVisible()
  await expect(panel.locator('.presets button')).toHaveCount(6)
  await expect(panel.locator('.card .id')).toContainText(['system.cpu', 'system.load', 'system.ram'])
  await panel.locator('.card').first().scrollIntoViewIfNeeded()
  await expect(panel.locator('.card canvas').first()).toBeVisible()
  await expect(panel.getByLabel('接口与网关', { exact: true })).toContainText('当前节点尚未采集')
  await page.getByPlaceholder('筛选图表…').fill('system.ram')
  await expect(panel.locator('.card')).toHaveCount(1)
  await Promise.all([
    page.waitForResponse(r => r.url().includes('/api/v1/data?') && r.url().includes('after=-900')),
    page.locator('.controls select').selectOption('900'),
  ])
  await page.getByPlaceholder('筛选图表…').fill('')
  const memory = panel.getByLabel('内存与交换', { exact: true })
  if (await memory.getByRole('button', { name: /展开其余/ }).count()) {
    await expect(memory.locator('.card')).toHaveCount(4)
    await memory.getByRole('button', { name: /展开其余/ }).click()
    expect(await memory.locator('.card').count()).toBeGreaterThan(4)
    await memory.getByRole('button', { name: '收起', exact: true }).click()
    await expect(memory.locator('.card')).toHaveCount(4)
  }
  for (const name of ['接口与网络', '数据库与缓存', '容器工作台', '资源瓶颈', '发布观察']) {
    await panel.getByRole('button', { name, exact: false }).click()
    await expect(panel.getByRole('heading', { name, exact: true })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  }
  await panel.getByRole('button', { name: '研发总览', exact: false }).click()
  await page.screenshot({ path: info.outputPath('presets.png'), fullPage: true })
  await page.getByRole('button', { name: '全部指标', exact: true }).click()
  await expect(panel).toHaveCount(0)
  await expect(page.locator('.card').first()).toBeVisible()
  expect(errors).toEqual([])
})

test('presets match semantic contexts, fall back to IDs and exclude unrelated charts', async () => {
  const { chartsForGroup, dashboards } = await import('../src/dashboards')
  const database = dashboards.find(b => b.id === 'data')!.groups[0]!
  const disk = dashboards.find(b => b.id === 'resources')!.groups.find(g => g.id === 'disk')!
  const charts = [
    { id: 'custom.instance', context: 'mysql.queries', priority: 2 },
    { id: 'postgres.connections', context: '', priority: 1 },
    { id: 'notmysql.queries', context: '', priority: 0 },
    { id: 'disk_await.sda', context: 'disk.await', priority: 3 },
  ] as import('../src/api').Chart[]
  expect(chartsForGroup(charts, database).map(c => c.id)).toEqual(['postgres.connections', 'custom.instance'])
  expect(chartsForGroup(charts, disk).map(c => c.id)).toEqual(['disk_await.sda'])
  expect(chartsForGroup([], database)).toEqual([])
  const containers = dashboards.find(b => b.id === 'containers')!.groups[0]!
  expect(chartsForGroup([{ id: 'k8s_kubelet.kubelet_pods_running', context: '', priority: 1 }] as import('../src/api').Chart[], containers)).toHaveLength(1)
})

test('node changes replace preset charts and scope history to the selected node', async ({ page }) => {
  await page.route('**/api/v1/info', async route => {
    const response = await route.fetch()
    await route.fulfill({ json: { ...await response.json(), mode: 'hub' } })
  })
  await page.route('**/api/v1/nodes', route => route.fulfill({ json: { now: Date.now() / 1000, nodes: [
    { id: '', hostname: 'local-test', local: true, status: 'live', alarms: {}, charts_count: 1 },
    { id: 'remote-test', hostname: 'remote-test', local: false, status: 'live', alarms: {}, charts_count: 1 },
  ] } }))
  await page.route('**/api/v1/charts?node=remote-test', async route => {
    const response = await route.fetch({ url: 'http://127.0.0.1:19997/api/v1/charts' })
    const body = await response.json()
    await route.fulfill({ json: { ...body, charts: { 'system.ram': body.charts['system.ram'] } } })
  })
  await page.goto('/?view=charts&token=browser-test-token')
  await page.getByRole('button', { name: '常用聚合看板', exact: true }).click()
  await expect(page.locator('.dashboards .card .id').first()).toHaveText('system.cpu')
  const history = page.waitForRequest(r => r.url().includes('/api/v1/data?') && new URL(r.url()).searchParams.get('node') === 'remote-test')
  await page.locator('.node-select').selectOption('remote-test')
  await expect(page.locator('.dashboards .card .id')).toHaveText(['system.ram'])
  await page.locator('.dashboards .card').scrollIntoViewIfNeeded()
  await history
  await page.locator('.node-select').selectOption('')
  await expect(page.locator('.dashboards .card .id').first()).toHaveText('system.cpu')
})
