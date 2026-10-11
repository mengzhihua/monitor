import { expect, test, type Page } from '@playwright/test'
import { openTools } from './mobile-navigation'

const now = () => Math.floor(Date.now() / 1000)
const settings = { base_url: 'https://model.example/v1', model: 'fixture', has_api_key: true, allow_private: false, revision: 1, enabled: true, persistent: true }
const charts = ['system.cpu', 'system.ram', 'system.load'].map(id => ({ id, title: id, units: '%', context: id, family: 'system', chart_type: 'line', priority: 1, update_every: 1, dimensions: [{ id: 'used', name: 'Used' }], last_entry: now() }))
function snapshot() { return { node: '', host_id: 'fixture', hostname: 'browser-test', generated_at: now(), after: now() - 300, before: now(), charts: [{ id: 'system.ram', title: 'RAM', units: '%', update_every: 1, points: 120, step: 3, last_collected_at: now(), state: 'fresh', dimensions: [{ id: 'used', name: 'Used', count: 119, missing: 1, min: 20, max: 90, average: 60, first: 50, last: 70, last_at: now() }] }], alarms: [], notes: ['桶均值统计，缺失不补零。'] } }
function record() { return { id: 'a'.repeat(48), node: '', hostname: 'browser-test', created_at: now(), updated_at: now(), revision: 1, model: 'fixture', question: 'Explain the problem', baseline: snapshot(), plan: { summary: '内存使用问题人工处理方案', hypotheses: [{ explanation: '可能存在持续增长的工作负载', evidence: '当前窗口只能提供指标变化', confidence: 'low' }], steps: [
  { id: 's1', title: '核对内存占用与工作负载', kind: 'diagnostic', instruction: '由用户核对实际进程与业务负载 <script>window.injected=true</script>', expected: '确认影响范围', risk: 'low', rollback: '', chart: 'system.ram', dimension: 'used' },
  { id: 's2', title: '按业务流程处理并复核最新指标', kind: 'verification', instruction: '等待采集后读取最新指标', expected: '确认当前告警和缺失情况', risk: 'low', rollback: '', chart: 'system.ram', dimension: '' },
] }, progress: [{ id: 's1', status: 'todo', note: '' }, { id: 's2', status: 'todo', note: '' }], review: undefined as unknown } }
async function setup(page: Page) {
  let current = record()
  let hasRecord = false
  let failConflict = false
  let nextOutcome = 'insufficient_data'
  let pauseSave: Promise<void> | null = null
  const calls = { previews: [] as any[], analyses: [] as any[], updates: [] as any[], reviews: [] as any[] }
  await page.route('**/api/v1/charts*', route => route.fulfill({ json: { hostname: 'browser-test', charts: Object.fromEntries(charts.map(c => [c.id, c])), charts_count: charts.length, update_every: 1 } }))
  await page.route('**/api/v1/ai/settings', route => route.fulfill({ json: settings }))
  await page.route('**/api/v1/ai/preview', route => { const body = route.request().postDataJSON(); calls.previews.push(body); return route.fulfill({ json: { id: 'b'.repeat(48), expires_at: now() + 300, snapshot: snapshot() } }) })
  await page.route('**/api/v1/ai/analyze', route => { calls.analyses.push(route.request().postDataJSON()); hasRecord = true; return route.fulfill({ json: { text: JSON.stringify(current.plan), model: 'fixture', duration_ms: 800, truncated: false, generated_at: now(), resolution: current } }) })
  await page.route('**/api/v1/ai/resolutions**', async route => {
    const url = new URL(route.request().url()), method = route.request().method()
    if (method === 'GET') {
      if (url.searchParams.has('id')) return route.fulfill({ json: current })
      return route.fulfill({ json: { records: hasRecord && (!url.searchParams.has('node') || url.searchParams.get('node') === current.node) ? [{ id: current.id, node: current.node, hostname: current.hostname, summary: current.plan.summary, created_at: current.created_at, updated_at: current.updated_at, done: current.progress.filter(p => p.status === 'done').length, total: 2 }] : [], limit: 10, persistent: true } })
    }
    const body = route.request().postDataJSON()
    if (method === 'DELETE') { hasRecord = false; return route.fulfill({ json: { deleted: true } }) }
    if (url.pathname.endsWith('/review')) {
      calls.reviews.push(body); current = { ...current, revision: current.revision + 1, review: { at: now(), outcome: nextOutcome, snapshot: snapshot(), comparisons: [{ chart: 'system.ram', dimension: 'used', units: '%', before: 60, after: nextOutcome === 'insufficient_data' ? null : 40, delta: nextOutcome === 'insufficient_data' ? null : -20 }], notes: ['窗口可能重叠；告警恢复不保证根因已解决。'] } }
      return route.fulfill({ json: current })
    }
    calls.updates.push(body)
    if (failConflict) { failConflict = false; current.revision++; current.progress[0]!.note = '另一个设备已保存的记录'; return route.fulfill({ status: 409, body: 'changed' }) }
    current = { ...current, revision: current.revision + 1, progress: current.progress.map(p => p.id === body.step_id ? { id: p.id, status: body.status, note: body.note } : p) }
    if (pauseSave) await pauseSave
    await route.fulfill({ json: current }).catch(() => {})
  })
  return { calls, conflict: () => { failConflict = true }, outcome: (value: string) => { nextOutcome = value }, pause: (gate: Promise<void>) => { pauseSave = gate }, retired: () => { current = { ...current, node: 'retired-node', hostname: 'retired-host', baseline: { ...current.baseline, node: 'retired-node' } }; hasRecord = true } }
}
async function open(page: Page) { await page.goto('/?token=browser-test-token'); await openTools(page); await page.getByRole('button', { name: 'AI 分析', exact: true }).click(); return page.getByRole('region', { name: 'AI 分析', exact: true }) }
async function generate(page: Page) {
  const panel = await open(page)
  await panel.getByRole('button', { name: '生成修复方案', exact: true }).click()
  await panel.getByRole('button', { name: '生成本地预览', exact: true }).click()
  await expect(panel.getByRole('button', { name: '发送快照并生成方案', exact: true })).toBeEnabled()
  await panel.getByRole('button', { name: '发送快照并生成方案', exact: true }).click()
  const plan = panel.getByRole('article', { name: '当前问题解决方案' })
  await expect(plan).toBeVisible(); return plan
}

test('structured solution requires explicit generation and preserves manual progress across reopening', async ({ page }, info) => {
  const state = await setup(page)
  const plan = await generate(page)
  expect(state.calls.analyses).toHaveLength(1); expect(state.calls.analyses[0].mode).toBe('resolution')
  await expect(plan).toContainText('可能存在持续增长的工作负载')
  expect(await page.evaluate(() => (window as any).injected)).toBeUndefined()
  await expect(plan.locator('script')).toHaveCount(0)
  const step = plan.locator('ol.steps > li').first()
  await step.getByRole('combobox').selectOption('done')
  await step.getByRole('textbox').fill('已核实业务负载，等待新采样')
  expect(state.calls.updates).toHaveLength(0)
  await step.getByRole('button', { name: '保存此步记录' }).click()
  await expect(plan).toContainText('1 / 2 步已记录完成')
  expect(state.calls.updates[0]).toMatchObject({ revision: 1, step_id: 's1', status: 'done', note: '已核实业务负载，等待新采样' })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await plan.evaluate(el => {
    const header = document.querySelector<HTMLElement>('#app > header')
    const offset = header && getComputedStyle(header).position === 'sticky' ? header.getBoundingClientRect().height : 0
    window.scrollTo(0, window.scrollY + el.getBoundingClientRect().top - offset - 12)
  })
  await page.screenshot({ path: info.outputPath('ai-solution-viewport.png'), scale: 'css' })
  await page.getByRole('button', { name: '关闭 AI 分析' }).click()
  await openTools(page); await page.getByRole('button', { name: 'AI 分析', exact: true }).click()
  await page.getByRole('button', { name: /内存使用问题人工处理方案/ }).click()
  await expect(page.getByRole('article', { name: '当前问题解决方案' })).toContainText('已核实业务负载')
  expect(state.calls.analyses).toHaveLength(1)
})
test('conflicts retain the draft and review reports uncertainty without another model call', async ({ page }) => {
  const state = await setup(page); const plan = await generate(page); const step = plan.locator('ol.steps > li').first()
  await step.getByRole('textbox').fill('本设备尚未保存的处理证据'); state.conflict()
  await step.getByRole('button', { name: '保存此步记录' }).click()
  await expect(page.getByRole('region', { name: '问题解决工作台' }).getByRole('alert')).toContainText('草稿已保留')
  await expect(step.getByRole('textbox')).toHaveValue('本设备尚未保存的处理证据')
  await expect(step.getByRole('button', { name: '保存此步记录' })).toBeDisabled()
  await plan.getByRole('button', { name: '读取最新方案版本，保留草稿' }).click()
  await expect(plan).toContainText('另一个设备已保存的记录')
  await step.getByRole('button', { name: '保存此步记录' }).click()
  await expect(plan).toContainText('服务端已保存：待处理 · 本设备尚未保存的处理证据')
  await plan.getByRole('button', { name: '重新采样并复核' }).click()
  await expect(plan.getByRole('region', { name: '处理效果复核' })).toContainText('数据不足或过期')
  await expect(plan).toContainText('当前 缺失')
  state.outcome('alarm_recovered'); await plan.getByRole('button', { name: '重新采样并复核' }).click()
  await expect(plan).toContainText('关联告警当前已恢复')
  expect(state.calls.analyses).toHaveLength(1); expect(state.calls.reviews).toHaveLength(2)
  const downloadPromise = page.waitForEvent('download'); await plan.getByRole('button', { name: '下载处理报告' }).click(); const downloaded = await downloadPromise
  const stream = await downloaded.createReadStream(); const chunks: Buffer[] = []; for await (const chunk of stream!) chunks.push(Buffer.from(chunk))
  const text = Buffer.concat(chunks).toString('utf8'); expect(text).toContain('本设备尚未保存的处理证据'); expect(text).toContain('完整方案与事实快照'); expect(text).not.toContain('api_key')
})
test('canceling a save requires a fresh version and preserves the local draft', async ({ page }) => {
  const state = await setup(page); const plan = await generate(page); let release!: () => void; state.pause(new Promise<void>(resolve => { release = resolve }))
  const step = plan.locator('ol.steps > li').first(); await step.getByRole('textbox').fill('取消前的草稿')
  await step.getByRole('button', { name: '保存此步记录' }).click()
  await expect(page.getByRole('button', { name: '取消方案请求' })).toBeVisible(); await page.getByRole('button', { name: '取消方案请求' }).click()
  await expect(step.getByRole('button', { name: '保存此步记录' })).toBeDisabled()
  release(); await plan.getByRole('button', { name: '读取最新方案版本，保留草稿' }).click()
  await expect(step.getByRole('button', { name: '保存此步记录' })).toBeEnabled(); await expect(step.getByRole('textbox')).toHaveValue('取消前的草稿')
  expect(state.calls.updates).toHaveLength(1)
})
test('problem entry pins the current alarm and selects the affected chart', async ({ page }) => {
  const state = await setup(page); await page.goto('/?token=browser-test-token')
  const problem = page.locator('article.problem').filter({ hasText: 'browser_ram_notice' }).first()
  await expect(problem).toBeVisible(); await problem.getByRole('button', { name: 'AI 解决问题' }).click()
  const panel = page.getByRole('region', { name: 'AI 分析', exact: true }); await expect(panel).toContainText('关联问题：browser_ram_notice')
  const heading = panel.getByRole('heading', { name: /AI 分析/ }); await expect(heading).toBeInViewport()
  const position = await heading.boundingBox()
  const offset = await page.locator('#app > header').evaluate(el => getComputedStyle(el).position === 'sticky' ? el.getBoundingClientRect().bottom : 0)
  expect(position!.y).toBeGreaterThanOrEqual(Math.max(0, offset))
  await expect(panel.getByRole('checkbox', { name: 'system.ram', exact: true })).toBeChecked(); await expect(panel.getByRole('button', { name: '生成修复方案', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await panel.getByRole('button', { name: '生成本地预览' }).click()
  expect(state.calls.previews[0].problem_id).toMatch(/^[a-f0-9]{64}$/); expect(state.calls.previews[0].charts).toEqual(['system.ram'])
  expect(state.calls.analyses).toHaveLength(0)
  await page.route('**/api/v1/ai/preview', route => route.fulfill({ status: 409, body: 'problem changed' }))
  await panel.getByRole('button', { name: '生成本地预览' }).click()
  await expect(panel.getByRole('alert')).toContainText('关联问题已恢复')
  await expect(panel.getByRole('button', { name: '发送快照并生成方案' })).toBeDisabled()
})

test('an invalid provider plan fails clearly and cannot be sent again without a new preview', async ({ page }) => {
  const state = await setup(page)
  await page.route('**/api/v1/ai/analyze', route => { state.calls.analyses.push(route.request().postDataJSON()); return route.fulfill({ status: 502, body: 'invalid resolution plan' }) })
  const panel = await open(page); await panel.getByRole('button', { name: '生成修复方案', exact: true }).click()
  await panel.getByRole('button', { name: '生成本地预览' }).click(); await panel.getByRole('button', { name: '发送快照并生成方案' }).click()
  await expect(panel.getByRole('alert')).toContainText('方案格式不完整')
  await expect(panel.getByRole('article', { name: '当前问题解决方案' })).toHaveCount(0)
  await expect(panel.getByRole('button', { name: '发送快照并生成方案' })).toBeDisabled()
  expect(state.calls.analyses).toHaveLength(1)
  await page.route('**/api/v1/ai/analyze', route => route.fulfill({ status: 429, body: 'AI resolution storage full' }))
  await panel.getByRole('button', { name: '生成本地预览' }).click(); await panel.getByRole('button', { name: '发送快照并生成方案' }).click()
  await expect(panel.getByRole('alert')).toContainText('方案存储已满')
})
test('320px solution keeps controls usable and deletion requires an explicit choice', async ({ page }, info) => {
  await page.setViewportSize({ width: 320, height: 844 }); await setup(page); const plan = await generate(page)
  await plan.getByRole('button', { name: '重新采样并复核' }).click(); await expect(plan.getByRole('region', { name: '处理效果复核' })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  expect(await plan.getByRole('combobox').first().evaluate(el => getComputedStyle(el).fontSize)).toBe('16px')
  await plan.screenshot({ path: info.outputPath('ai-resolution-320.png'), scale: 'css' })
  await plan.getByRole('button', { name: '删除方案', exact: true }).click(); await expect(plan.getByRole('button', { name: '确认删除方案' })).toBeVisible()
  await plan.getByRole('button', { name: '保留方案', exact: true }).click(); await expect(plan).toBeVisible()
  await plan.getByRole('button', { name: '删除方案', exact: true }).click(); await plan.getByRole('button', { name: '确认删除方案' }).click(); await expect(plan).toHaveCount(0)
})

test('personal plans for removed nodes remain discoverable and can be exported or deleted', async ({ page }) => {
  const state = await setup(page); state.retired(); const panel = await open(page)
  await expect(panel.getByRole('button', { name: /内存使用问题人工处理方案/ })).toHaveCount(0)
  await panel.getByRole('checkbox', { name: '查看全部节点的个人方案（包括已移除节点）' }).check()
  await panel.getByRole('button', { name: /内存使用问题人工处理方案/ }).click()
  const plan = panel.getByRole('article', { name: '当前问题解决方案' }); await expect(plan).toContainText('方案节点：retired-host')
  await plan.getByRole('button', { name: '删除方案', exact: true }).click(); await plan.getByRole('button', { name: '确认删除方案' }).click()
  await expect(plan).toHaveCount(0); expect(state.calls.analyses).toHaveLength(0)
})
