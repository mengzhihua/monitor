import { expect, test, type Page, type Route } from '@playwright/test'
import { openTools } from './mobile-navigation'

const nodeIDs = ['node-a', 'node-b']
async function hub(page: Page) {
  await page.route('**/api/v1/info', route => route.fulfill({ json: {
    version: 'test', mode: 'hub', uptime: 10, charts_count: 0, metrics_count: 0,
    host: { id: 'hub', hostname: 'hub-local', os: 'linux', arch: 'amd64', labels: {}, update_every: 1 },
    collectors: [], alarms: null, user: { name: 'admin', role: 'admin' },
  } }))
  await page.route('**/api/v1/nodes*', route => route.fulfill({ json: { now: 100, nodes: nodeIDs.map(id => ({
    id, hostname: id === 'node-a' ? 'agent-one' : 'agent-two', os: 'linux', arch: 'amd64', version: 'test',
    local: false, status: 'live', first_seen: 1, last_seen: 100, charts_count: 0, alarms: {},
  })) } }))
  await page.route('**/api/v1/charts*', route => route.fulfill({ json: { charts: {} } }))
  await page.route('**/api/v1/functions*', route => route.fulfill({ json: [] }))
  await page.route('**/api/v1/alarms?*', route => route.fulfill({ json: { alarms: {} } }))
  await page.route('**/api/v1/hub/spaces*', route => route.fulfill({ json: { spaces: [] } }))
  await page.route('**/api/v1/hub/rooms*', route => route.fulfill({ json: { rooms: [] } }))
}
async function openTool(page: Page, title: string) {
  await openTools(page)
  await page.getByTitle(title, { exact: true }).click()
}
async function selectNode(page: Page, hostname: string) {
  await page.getByTitle('节点', { exact: true }).click()
  await page.getByRole('option', { name: hostname, exact: false }).click()
  await expect(page.locator('.node-select-btn')).toContainText(hostname)
}
async function delayedReply(route: Route, release: Promise<void>, json: unknown) {
  await release
  // The old fetch may already have been aborted by a node/selection change.
  await route.fulfill({ json }).catch(() => {})
}
function latch() {
  let release!: () => void
  const promise = new Promise<void>(resolve => { release = resolve })
  return { promise, release }
}
const inventory = (node: string) => ({ node_id: node, auto: { hostname: node }, tags: {}, manual: { owner: node }, effective: {}, updated: 0, sort_order: ['owner'] })

async function templates(page: Page) {
  await hub(page)
  await page.route('**/api/v1/hub/templates', route => route.fulfill({ json: { templates: [] } }))
  await page.route('**/api/v1/hub/inventory', route => route.fulfill({ json: { inventory: Object.fromEntries(nodeIDs.map(id => [id, inventory(id)])) } }))
  await page.goto('/?view=charts&token=browser-test-token')
  await openTool(page, 'Zabbix 模板 / 主机组 / 资产')
  return page.getByRole('region', { name: '模板与资产', exact: true })
}

test('template edits preserve nested rule fields and use the saved revision on reopening', async ({ page }) => {
  await hub(page)
  const rule = { name: 'cpu', on: 'system.cpu', calc: '$user', warn: '$this > 80', info: 'line 1\n"quoted": # detail',
    chart_labels: { environment: 'true', location: 'east: zone' }, macros: { '{$LIMIT}': '80' }, disabled: false }
  let current = { id: 'template-1', name: 'CPU template', description: 'before', rules: [rule], updated: 7 }
  const revisions: number[] = []
  await page.route('**/api/v1/hub/inventory', route => route.fulfill({ json: { inventory: {} } }))
  await page.route('**/api/v1/hub/templates*', route => {
    if (route.request().method() === 'GET') return route.fulfill({ json: { templates: [current] } })
    const body = route.request().postDataJSON()
    revisions.push(body.if_updated)
    expect(body.rules_yaml).toContain('"chart_labels": ' + JSON.stringify(rule.chart_labels))
    expect(body.rules_yaml).toContain('"macros": ' + JSON.stringify(rule.macros))
    expect(body.rules_yaml).toContain('"info": ' + JSON.stringify(rule.info))
    expect(body.rules_yaml).toContain('"disabled": false')
    expect(body.if_updated).toBe(current.updated)
    current = { ...current, description: body.description, updated: current.updated + 1 }
    return route.fulfill({ json: current })
  })
  await page.goto('/?view=charts&token=browser-test-token')
  await openTool(page, 'Zabbix 模板 / 主机组 / 资产')
  const panel = page.getByRole('region', { name: '模板与资产', exact: true })
  for (const text of ['first edit', 'second edit']) {
    await panel.getByRole('button', { name: '编辑', exact: true }).click()
    await expect(panel.getByLabel('模板告警规则')).toHaveValue(/"chart_labels"/)
    await panel.getByLabel('描述', { exact: true }).fill(text)
    await panel.getByRole('button', { name: '保存', exact: true }).click()
    await expect(panel.locator('.editor')).toHaveCount(0)
    await expect(panel.locator('.list')).toContainText(text)
  }
  expect(revisions).toEqual([7, 8])
  await expect(panel.getByRole('alert')).toHaveCount(0)
})

test('templates without assignment metadata can add a label selector', async ({ page }) => {
  await hub(page)
  let saved: Record<string, unknown> | undefined
  await page.route('**/api/v1/hub/inventory', route => route.fulfill({ json: { inventory: {} } }))
  await page.route('**/api/v1/hub/templates*', route => {
    if (route.request().method() === 'GET') return route.fulfill({ json: { templates: [{ id: 'legacy', name: 'Legacy template', updated: 2 }] } })
    saved = route.request().postDataJSON()
    return route.fulfill({ json: { ...saved, updated: 3 } })
  })
  await page.goto('/?view=charts&token=browser-test-token')
  await openTool(page, 'Zabbix 模板 / 主机组 / 资产')
  const panel = page.getByRole('region', { name: '模板与资产', exact: true })
  await panel.getByRole('button', { name: '编辑', exact: true }).click()
  await panel.getByRole('button', { name: '+ 标签匹配', exact: true }).click()
  const inputs = panel.locator('.assign .kv input')
  await expect(inputs).toHaveCount(2)
  await inputs.nth(0).fill('environment')
  await inputs.nth(0).press('Tab')
  await inputs.nth(1).fill('production')
  await panel.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => saved).toBeTruthy()
  expect(saved?.assign).toEqual({ labels: { environment: 'production' } })
})

test('switching inventory clears the old editor and prevents late reads from writing another host', async ({ page }) => {
  const a = latch(), b = latch()
  const seen: string[] = [], writes: Array<{ node: string; fields: Record<string, string>; if_updated: number }> = []
  await page.route('**/api/v1/hub/inventory?*', async route => {
    const node = new URL(route.request().url()).searchParams.get('node')!
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON()
      writes.push({ node, ...body })
      return route.fulfill({ json: { ...inventory(node), manual: body.fields, updated: 1 } })
    }
    seen.push(node)
    return delayedReply(route, node === 'node-a' ? a.promise : b.promise, inventory(node))
  })
  const panel = await templates(page)
  await panel.getByRole('button', { name: '资产清单', exact: true }).click()
  await panel.locator('.list tr').filter({ hasText: 'node-a' }).getByRole('button', { name: '编辑' }).click()
  await expect.poll(() => seen).toContain('node-a')
  await panel.locator('.list tr').filter({ hasText: 'node-b' }).getByRole('button', { name: '编辑' }).click()
  await expect.poll(() => seen).toContain('node-b')
  await expect(panel.locator('.editor')).toHaveCount(0)
  await expect(panel.getByRole('status')).toContainText('正在读取')
  b.release()
  await expect(panel.locator('.editor')).toContainText('node-b')
  a.release()
  await panel.locator('.editor .kv input').nth(1).fill('owner-for-b')
  await panel.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes).toEqual([{ node: 'node-b', fields: { owner: 'owner-for-b' }, if_updated: 0 }])
  await expect(panel.locator('.editor .kv input').nth(1)).toHaveValue('owner-for-b')
})

test('inventory loading failure cannot leave the previous host available for saving', async ({ page }) => {
  await page.route('**/api/v1/hub/inventory?*', route => new URL(route.request().url()).searchParams.get('node') === 'node-a'
    ? route.fulfill({ json: inventory('node-a') })
    : route.fulfill({ status: 503, body: 'inventory temporarily unavailable' }))
  const panel = await templates(page)
  await panel.getByRole('button', { name: '资产清单', exact: true }).click()
  await panel.locator('.list tr').filter({ hasText: 'node-a' }).getByRole('button', { name: '编辑' }).click()
  await expect(panel.locator('.editor')).toContainText('node-a')
  await panel.locator('.list tr').filter({ hasText: 'node-b' }).getByRole('button', { name: '编辑' }).click()
  await expect(panel.getByRole('alert')).toContainText('503')
  await expect(panel.locator('.editor')).toHaveCount(0)
  await expect(panel.getByRole('button', { name: '保存', exact: true })).toHaveCount(0)
})

test('effective template preview drops stale responses and clears when selection is removed', async ({ page }) => {
  const a = latch()
  let requestedA = false
  await page.route('**/api/v1/hub/templates/effective?*', route => {
    if (new URL(route.request().url()).searchParams.get('node') === 'node-a') {
      requestedA = true
      return delayedReply(route, a.promise, { rev: 1, templates: ['obsolete-template'] })
    }
    return route.fulfill({ json: { rev: 2, templates: ['current-template'] } })
  })
  const panel = await templates(page)
  await panel.getByLabel('节点有效配置预览').selectOption('node-a')
  await expect.poll(() => requestedA).toBe(true)
  await panel.getByLabel('节点有效配置预览').selectOption('node-b')
  await expect(panel.locator('.preview')).toContainText('current-template')
  a.release()
  await expect(panel.locator('.preview')).not.toContainText('obsolete-template')
  await panel.getByLabel('节点有效配置预览').selectOption('')
  await expect(panel.locator('.preview')).toHaveCount(0)
})

for (const diagnostic of ['logs', 'weights', 'contexts'] as const) {
  test(`${diagnostic} reloads for the chosen node and discards its old in-flight response`, async ({ page }) => {
    await hub(page)
    const old = latch()
    const seen: string[] = []
    const result = (node: string) => diagnostic === 'logs' ? { time: 100, result: { columns: ['message'], total: 1, rows: [{ time: 100, priority: 'info', unit: '', pid: '', message: node + '-result' }] } }
      : diagnostic === 'weights' ? { weights: [{ chart: node, title: node + '-result', context: '', score: 1, anomaly_rate: 1 }] }
        : { contexts: { [node]: { family: '', title: node + '-result', units: '', charts: [], dimensions: [], priority: 1 } } }
    await page.route(`**/api/v1/${diagnostic}*`, route => {
      const node = new URL(route.request().url()).searchParams.get('node') || 'local'
      seen.push(node)
      return node === 'node-a' ? delayedReply(route, old.promise, result(node)) : route.fulfill({ json: result(node) })
    })
    await page.goto('/?view=charts&token=browser-test-token&node=node-a')
    await expect(page.locator('.node-select-btn')).toContainText('agent-one')
    await openTool(page, { logs: '日志', weights: '异常顾问 / 关联分析', contexts: 'Context 总览' }[diagnostic])
    await expect.poll(() => seen).toContain('node-a')
    await selectNode(page, 'agent-two')
    await expect.poll(() => seen).toContain('node-b')
    const panel = diagnostic === 'logs' ? page.locator('.panel').filter({ has: page.getByRole('heading', { name: '日志', exact: false }) })
      : page.getByRole('region', { name: diagnostic === 'weights' ? '关联分析' : 'Contexts', exact: true })
    await expect(panel).toContainText('node-b-result')
    old.release()
    await expect(panel).not.toContainText('node-a-result')
  })
}

test('correlation method changes do not accept a slower previous response', async ({ page }) => {
  await hub(page)
  const old = latch()
  let initialRequested = false
  await page.route('**/api/v1/weights*', route => {
    const method = new URL(route.request().url()).searchParams.get('method')
    const json = { weights: [{ chart: method, title: method + '-result', score: 1, anomaly_rate: 1 }] }
    if (method === 'anomaly-rate') { initialRequested = true; return delayedReply(route, old.promise, json) }
    return route.fulfill({ json })
  })
  await page.goto('/?view=charts&token=browser-test-token')
  await openTool(page, '异常顾问 / 关联分析')
  const panel = page.getByRole('region', { name: '关联分析', exact: true })
  await expect.poll(() => initialRequested).toBe(true)
  await panel.getByLabel('关联分析方法').selectOption('kmeans')
  await expect(panel).toContainText('kmeans-result')
  old.release()
  await expect(panel).not.toContainText('anomaly-rate-result')
})

test('Cloud node navigation uses node identity and clears chart search', async ({ page }) => {
  await hub(page)
  await page.route('**/api/v1/hub/console', route => route.fulfill({ json: {
    spaces: [], unassigned: [{ id: 'node-b', hostname: 'agent-two', status: 'live' }], aclk: {},
  } }))
  await page.goto('/?view=charts&token=browser-test-token&node=node-a')
  await page.getByLabel('筛选图表', { exact: true }).fill('obsolete-search')
  await openTool(page, 'Cloud 控制台')
  const panel = page.locator('.panel').filter({ has: page.getByRole('heading', { name: 'Cloud 控制台', exact: false }) })
  await panel.getByRole('button', { name: 'agent-two', exact: false }).click()
  await expect(page.locator('.node-select-btn')).toContainText('agent-two')
  await expect(page).toHaveURL(/view=charts/)
  await expect(panel).toHaveCount(0)
  await expect(page.getByLabel('筛选图表', { exact: true })).toHaveValue('')
  expect(await page.evaluate(() => sessionStorage.getItem('monitor.node'))).toBe('node-b')
})
