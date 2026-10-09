import { expect, test, type Page, type Route } from '@playwright/test'
import { openTools } from './mobile-navigation'

const emptySettings = { base_url: '', model: '', has_api_key: false, allow_private: false, revision: 1, enabled: false, persistent: true }
const configured = { ...emptySettings, base_url: 'https://model.example/v1', model: 'my-custom-model', has_api_key: true, enabled: true }
const now = () => Math.floor(Date.now() / 1000)
const chartList = Array.from({ length: 8 }, (_, i) => ({
  id: ['system.cpu', 'system.ram', 'system.load'][i] || `custom.chart-${i}`,
  title: i ? `Resource chart ${i}` : 'Production CPU with a deliberately long chart name',
  units: '%', context: 'system.cpu', family: 'system', chart_type: 'line', priority: i,
  update_every: 1, dimensions: [{ id: 'used', name: 'Used' }], last_entry: now(),
}))
function fixture(node = '') {
  const time = now()
  return {
    id: `snapshot-${node || 'local'}-${time}`, expires_at: time + 300,
    snapshot: {
      node, hostname: node || 'monitor-local', generated_at: time, after: time - 900, before: time,
      charts: [{ id: 'system.cpu', title: chartList[0]!.title, units: '%', update_every: 1, points: 300, step: 3,
        dimensions: [{ id: 'used', name: 'Used', count: 299, missing: 1, min: 1, max: 30, average: 12, first: 10, last: 20, last_at: time },
          { id: 'absent', name: 'Missing dimension', count: 0, missing: 300, min: null, max: null, average: null, first: null, last: null, last_at: null }] }],
      alarms: [{ name: 'cpu_notice', chart: 'system.cpu', status: 'WARNING', value: 20, units: '%' }], notes: ['仅包含所选图表的数据。'],
    },
  }
}
function answer(text = '检查 CPU 使用情况，当前窗口不足以确定根因。') {
  return { text, model: 'my-custom-model', duration_ms: 1100, truncated: false, generated_at: now(), usage: { prompt_tokens: 200, completion_tokens: 40, total_tokens: 240 } }
}
async function setup(page: Page, settings = configured) {
  const calls = { settings: [] as Record<string, unknown>[], previews: [] as Record<string, unknown>[], analyses: [] as Record<string, unknown>[] }
  await page.route('**/api/v1/charts*', route => route.fulfill({ json: { hostname: 'monitor-local', update_every: 1, charts_count: chartList.length, charts: Object.fromEntries(chartList.map(chart => [chart.id, chart])) } }))
  await page.route('**/api/v1/ai/settings', route => {
    if (route.request().method() !== 'GET') calls.settings.push(route.request().postDataJSON())
    return route.fulfill({ json: settings })
  })
  await page.route('**/api/v1/ai/preview', route => {
    const body = route.request().postDataJSON()
    calls.previews.push(body)
    return route.fulfill({ json: fixture(body.node) })
  })
  await page.route('**/api/v1/ai/analyze', route => {
    calls.analyses.push(route.request().postDataJSON())
    return route.fulfill({ json: answer() })
  })
  return calls
}
async function openPanel(page: Page) {
  await openTools(page)
  await page.getByRole('button', { name: 'AI 分析', exact: true }).click()
  const panel = page.getByRole('region', { name: 'AI 分析', exact: true })
  await expect(panel).toBeVisible()
  await expect(panel.getByRole('button', { name: '生成本地预览', exact: true })).toBeEnabled()
  await expect(panel.getByRole('button', { name: '取消请求' })).toHaveCount(0)
  return panel
}
async function preview(page: Page) {
  const panel = page.getByRole('region', { name: 'AI 分析', exact: true })
  await panel.getByRole('button', { name: '生成本地预览', exact: true }).click()
  await expect(panel.getByRole('region', { name: '监控事实预览', exact: true })).toBeVisible()
  return panel
}

test('personal settings never echo secrets and conflict recovery preserves the draft', async ({ page }, info) => {
  const calls = await setup(page, emptySettings)
  let saved = { ...emptySettings }
  let failConflict = false
  await page.route('**/api/v1/ai/settings', route => {
    if (route.request().method() === 'GET') return route.fulfill({ json: saved })
    const body = route.request().postDataJSON()
    calls.settings.push(body)
    if (route.request().method() === 'DELETE') { saved = { ...emptySettings, revision: saved.revision + 1 }; return route.fulfill({ json: saved }) }
    if (failConflict) { failConflict = false; saved.revision++; return route.fulfill({ status: 409, json: { error: 'settings changed' } }) }
    saved = { ...saved, base_url: body.base_url, model: body.model, has_api_key: !body.clear_api_key, enabled: true, revision: saved.revision + 1 }
    return route.fulfill({ json: saved })
  })
  await page.goto('/?token=browser-test-token')
  const panel = await openPanel(page)
  expect(calls.previews).toEqual([])
  expect(calls.analyses).toEqual([])
  await panel.getByLabel('API 基础地址', { exact: true }).fill('https://model.example/v1')
  await panel.getByLabel('模型名', { exact: true }).fill('my-model')
  await panel.getByLabel('API Key', { exact: true }).fill('unit-test-private-provider-key')
  await panel.getByRole('button', { name: '保存个人配置', exact: true }).click()
  await expect(panel.getByRole('status')).toContainText('已保存')
  expect(calls.settings[0]?.api_key).toBe('unit-test-private-provider-key')
  await panel.locator('.settings summary').click()
  await expect(panel.getByLabel('API Key', { exact: true })).toHaveValue('')
  await panel.screenshot({ path: info.outputPath('ai-config.png'), scale: 'css' })
  await page.evaluate(() => window.scrollTo(0, 0))
  await page.screenshot({ path: info.outputPath('ai-config-viewport.png'), scale: 'css' })
  expect(await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }))).not.toContain('unit-test-private-provider-key')
  await panel.getByLabel('模型名', { exact: true }).fill('draft-revision-model')
  failConflict = true
  await panel.getByRole('button', { name: '保存个人配置', exact: true }).click()
  await expect(panel.getByRole('alert')).toContainText('草稿已保留')
  await expect(panel.getByLabel('模型名', { exact: true })).toHaveValue('draft-revision-model')
  await panel.getByRole('button', { name: '读取最新配置，保留草稿' }).click()
  await expect(panel.getByLabel('模型名', { exact: true })).toHaveValue('draft-revision-model')
  await panel.getByRole('button', { name: '保存个人配置', exact: true }).click()
  await expect.poll(() => calls.settings.length).toBe(3)
  expect(calls.settings[2]).toMatchObject({ model: 'draft-revision-model', revision: 3 })
  expect(calls.settings[2]).not.toHaveProperty('api_key')
  await panel.locator('.settings summary').click()
  await panel.getByLabel('API 基础地址', { exact: true }).fill('https://different.example/v1')
  await expect(panel.getByRole('button', { name: '保存个人配置', exact: true })).toBeDisabled()
  await panel.getByLabel('清除已保存的 API Key', { exact: true }).check()
  await expect(panel.getByRole('button', { name: '保存个人配置', exact: true })).toBeEnabled()
  await panel.getByRole('button', { name: '保存个人配置', exact: true }).click()
  await expect.poll(() => calls.settings.length).toBe(4)
  expect(calls.settings[3]).toMatchObject({ clear_api_key: true, base_url: 'https://different.example/v1' })
  expect(calls.settings[3]).not.toHaveProperty('api_key')
  await panel.locator('.settings summary').click()
  await panel.getByRole('button', { name: '删除个人配置', exact: true }).click()
  await expect(panel.getByRole('button', { name: '确认删除配置', exact: true })).toBeVisible()
  expect(calls.settings).toHaveLength(4)
  await panel.getByRole('button', { name: '确认删除配置', exact: true }).click()
  await expect(panel.getByRole('status')).toContainText('已删除')
  expect(calls.settings[4]).toEqual({ revision: 5 })
  await expect(panel.getByLabel('模型名', { exact: true })).toHaveValue('')
})

test('preview stays local until explicitly sent and analysis is rendered as inert text with a downloadable snapshot', async ({ page }) => {
  const calls = await setup(page)
  const html = '<img src=x onerror="window.aiInjected=true"><script>window.aiInjected=true</script>'
  await page.route('**/api/v1/ai/analyze', route => {
    calls.analyses.push(route.request().postDataJSON())
    return route.fulfill({ json: { ...answer(html), truncated: true } })
  })
  await page.goto('/?token=browser-viewer-token')
  const panel = await openPanel(page)
  await panel.locator('.settings summary').click()
  await expect(panel.getByLabel('允许本机或内网模型服务（含 HTTP）', { exact: true })).toHaveCount(0)
  await preview(page)
  expect(calls.previews).toEqual([{ node: '', charts: ['system.cpu', 'system.ram', 'system.load'], window: 900 }])
  expect(calls.analyses).toEqual([])
  await expect(panel.locator('.snapshot')).toContainText('300 个时间桶 · 桶宽 3 秒')
  await expect(panel.locator('.snapshot')).toContainText('299 / 1')
  await expect(panel.locator('.snapshot')).toContainText('+10')
  await expect(panel.locator('.snapshot')).toContainText('缺失')
  await panel.getByLabel('分析问题', { exact: true }).fill('请核查异常。')
  await panel.getByRole('button', { name: '发送快照并分析', exact: true }).click()
  await expect(panel.getByRole('region', { name: 'AI 分析结果', exact: true })).toBeVisible()
  expect(calls.analyses).toHaveLength(1)
  expect(Object.keys(calls.analyses[0]!).sort()).toEqual(['preview_id', 'question', 'revision'])
  expect(calls.analyses[0]).toMatchObject({ question: '请核查异常。', revision: 1 })
  expect(calls.analyses[0]?.preview_id).toMatch(/^snapshot-local-/)
  await expect(panel.locator('.answer')).toHaveText(html)
  await expect(panel.locator('.result img, .result script')).toHaveCount(0)
  expect(await page.evaluate(() => (window as unknown as { aiInjected?: boolean }).aiInjected)).toBeUndefined()
  await expect(panel.locator('.result')).toContainText('被截断')
  const [download] = await Promise.all([page.waitForEvent('download'), panel.getByRole('button', { name: '下载结果' }).click()])
  expect(download.suggestedFilename()).toMatch(/^monitor-ai-analysis-\d+\.txt$/)
  const stream = await download.createReadStream()
  const buffers: Buffer[] = []
  for await (const chunk of stream!) buffers.push(Buffer.from(chunk))
  const report = Buffer.concat(buffers).toString()
  expect(report).toContain('监控事实快照')
  expect(report).toContain('请核查异常。')
  expect(report).not.toContain('api_key')
  await panel.getByRole('combobox', { name: '统计时间', exact: true }).selectOption('3600')
  await expect(panel.getByRole('region', { name: '监控事实预览', exact: true })).toHaveCount(0)
  await expect(panel.getByRole('region', { name: 'AI 分析结果', exact: true })).toHaveCount(0)
  await expect(panel.getByRole('button', { name: '发送快照并分析', exact: true })).toBeDisabled()
})

test('closing cancels in-flight analysis and reopening does not restore private output', async ({ page }) => {
  await setup(page)
  let held: Route | undefined
  await page.route('**/api/v1/ai/analyze', route => { held = route })
  await page.goto('/?token=browser-test-token')
  const panel = await openPanel(page)
  await preview(page)
  await panel.getByRole('button', { name: '发送快照并分析', exact: true }).click()
  await expect.poll(() => !!held).toBe(true)
  await expect(panel.getByRole('button', { name: '发送快照并分析', exact: true })).toBeDisabled()
  await panel.getByRole('button', { name: '关闭 AI 分析', exact: true }).click()
  await expect(panel).toHaveCount(0)
  await held!.fulfill({ json: answer('late private output') }).catch(() => {})
  const reopened = await openPanel(page)
  await expect(reopened.getByRole('region', { name: '监控事实预览', exact: true })).toHaveCount(0)
  await expect(reopened).not.toContainText('late private output')
})

test('switching nodes invalidates a pending preview and scopes the next one', async ({ page }) => {
  await setup(page)
  await page.route('**/api/v1/info', async route => {
    const response = await route.fetch()
    await route.fulfill({ json: { ...await response.json(), mode: 'hub' } })
  })
  const node = { os: 'linux', arch: 'arm64', version: 'test', status: 'live', first_seen: 1, last_seen: 2, alarms: {}, charts_count: 8 }
  await page.route('**/api/v1/nodes*', route => route.fulfill({ json: { now: now(), nodes: [
    { ...node, id: '', hostname: 'local', local: true }, { ...node, id: 'remote-ai', hostname: 'remote-ai', local: false },
  ] } }))
  let held: Route | undefined
  const scopes: string[] = []
  await page.route('**/api/v1/ai/preview', route => {
    const body = route.request().postDataJSON()
    scopes.push(body.node)
    if (!body.node) { held = route; return }
    return route.fulfill({ json: fixture(body.node) })
  })
  await page.goto('/?token=browser-test-token')
  let panel = await openPanel(page)
  await panel.getByRole('button', { name: '生成本地预览', exact: true }).click()
  await expect.poll(() => !!held).toBe(true)
  await page.getByTitle('节点', { exact: true }).click()
  await page.locator('.node-menu').getByRole('option', { name: /remote-ai/ }).click()
  await expect(page.getByTitle('节点', { exact: true })).toContainText('remote-ai')
  await held!.fulfill({ json: fixture('') }).catch(() => {})
  if (!await panel.isVisible()) panel = await openPanel(page)
  await expect(panel.getByRole('region', { name: '监控事实预览', exact: true })).toHaveCount(0)
  await preview(page)
  expect(scopes).toEqual(['', 'remote-ai'])
  await expect(panel.getByRole('region', { name: '监控事实预览', exact: true })).toContainText('remote-ai')
})

test('provider failures are explicit, never retry automatically, and expiry disables sending', async ({ page }) => {
  const calls = await setup(page)
  let status = 429
  await page.route('**/api/v1/ai/analyze', route => {
    calls.analyses.push(route.request().postDataJSON())
    return route.fulfill({ status, json: { error: 'provider failed' } })
  })
  await page.goto('/?token=browser-test-token')
  const panel = await openPanel(page)
  await preview(page)
  for (const [code, message] of [[429, '过于频繁'], [502, '连接失败'], [504, '响应超时']] as const) {
    status = code
    await preview(page)
    await panel.getByRole('button', { name: '发送快照并分析', exact: true }).click()
    await expect(panel.getByRole('alert')).toContainText(message)
    await expect(panel.getByRole('region', { name: 'AI 分析结果', exact: true })).toHaveCount(0)
  }
  expect(calls.analyses).toHaveLength(3)
  await page.route('**/api/v1/ai/preview', route => route.fulfill({ json: { ...fixture(), expires_at: now() - 1 } }))
  await preview(page)
  await expect(panel.getByRole('region', { name: '监控事实预览', exact: true })).toContainText('已过期')
  await expect(panel.getByRole('button', { name: '发送快照并分析', exact: true })).toBeDisabled()
})

test('320px analysis keeps controls usable, bounds selections and clears content on unauthorized responses', async ({ page }, info) => {
  await page.setViewportSize({ width: 320, height: 844 })
  await setup(page)
  await page.goto('/?token=browser-test-token')
  const panel = await openPanel(page)
  await panel.locator('.settings summary').click()
  await expect.poll(() => page.evaluate(() => Math.max(document.documentElement.scrollWidth, document.body.scrollWidth) - innerWidth)).toBeLessThanOrEqual(0)
  expect(await panel.getByLabel('API Key', { exact: true }).evaluate(element => getComputedStyle(element).fontSize)).toBe('16px')
  await panel.locator('.settings summary').click()
  for (const id of ['custom.chart-3', 'custom.chart-4', 'custom.chart-5']) await panel.getByRole('checkbox', { name: id, exact: true }).check()
  await expect(panel.getByRole('checkbox', { name: 'custom.chart-6', exact: true })).toBeDisabled()
  await preview(page)
  await panel.getByRole('button', { name: '发送快照并分析', exact: true }).click()
  await expect(panel.getByRole('region', { name: 'AI 分析结果', exact: true })).toBeVisible()
  await expect.poll(() => page.evaluate(() => Math.max(document.documentElement.scrollWidth, document.body.scrollWidth) - innerWidth)).toBeLessThanOrEqual(0)
  for (const button of await panel.getByRole('button').all()) {
    if (await button.isVisible()) expect((await button.boundingBox())?.height).toBeGreaterThanOrEqual(44)
  }
  await panel.screenshot({ path: info.outputPath('ai-mobile-320.png'), scale: 'css' })
  await page.route('**/api/v1/ai/analyze', route => route.fulfill({ status: 401, json: { error: 'unauthorized' } }))
  await preview(page)
  await panel.getByRole('button', { name: '发送快照并分析', exact: true }).click()
  await expect(panel.getByRole('alert')).toContainText('登录已失效')
  await expect(panel.getByRole('region', { name: '监控事实预览', exact: true })).toHaveCount(0)
  await expect(panel.getByRole('region', { name: 'AI 分析结果', exact: true })).toHaveCount(0)
})


test('selection and settings edits invalidate snapshots while canceling prevents a late result', async ({ page }) => {
  await setup(page)
  let held: Route | undefined
  await page.route('**/api/v1/ai/analyze', route => { held = route })
  await page.goto('/?token=browser-test-token')
  const panel = await openPanel(page)
  await preview(page)
  await panel.getByRole('checkbox', { name: 'system.cpu', exact: true }).uncheck()
  await expect(panel.getByRole('region', { name: '监控事实预览', exact: true })).toHaveCount(0)
  await preview(page)
  await panel.locator('.settings summary').click()
  await panel.getByLabel('模型名', { exact: true }).fill('changed-model')
  await expect(panel.getByRole('region', { name: '监控事实预览', exact: true })).toHaveCount(0)
  await panel.getByRole('button', { name: '保存个人配置', exact: true }).click()
  await expect(panel.getByRole('status')).toContainText('已保存')
  await preview(page)
  await panel.getByRole('button', { name: '发送快照并分析', exact: true }).click()
  await expect.poll(() => !!held).toBe(true)
  await panel.getByRole('button', { name: '取消请求', exact: true }).click()
  await expect(panel.getByRole('status')).toContainText('费用以服务商记录为准')
  await expect(panel.getByRole('button', { name: '发送快照并分析', exact: true })).toBeDisabled()
  await held!.fulfill({ json: answer('late canceled output') }).catch(() => {})
  await expect(panel.getByRole('region', { name: 'AI 分析结果', exact: true })).toHaveCount(0)
  await expect(panel).not.toContainText('late canceled output')
})

test('canceling a settings write requires a fresh server revision before another write', async ({ page }) => {
  await setup(page)
  let held: Route | undefined
  let remote = { ...configured }
  await page.route('**/api/v1/ai/settings', route => {
    if (route.request().method() === 'GET') return route.fulfill({ json: remote })
    remote = { ...remote, model: route.request().postDataJSON().model, revision: remote.revision + 1 }
    held = route
  })
  await page.goto('/?token=browser-test-token')
  const panel = await openPanel(page)
  await panel.locator('.settings summary').click()
  await panel.getByLabel('模型名', { exact: true }).fill('changed-while-canceling')
  await panel.getByRole('button', { name: '保存个人配置', exact: true }).click()
  await expect.poll(() => !!held).toBe(true)
  await panel.getByRole('button', { name: '取消请求', exact: true }).click()
  await expect(panel).toContainText('服务端可能已经完成修改')
  await expect(panel.getByRole('button', { name: '保存个人配置', exact: true })).toBeDisabled()
  await held!.fulfill({ json: remote }).catch(() => {})
  await panel.getByRole('button', { name: '刷新配置版本', exact: true }).click()
  await expect(panel.getByRole('status')).toContainText('已读取最新配置版本')
  await expect(panel).not.toContainText('上次配置请求的保存状态尚未确认')
  await preview(page)
  await expect(panel.getByRole('button', { name: '发送快照并分析', exact: true })).toBeEnabled()
})
