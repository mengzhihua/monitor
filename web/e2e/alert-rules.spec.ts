import { expect, test, type Page, type Route } from '@playwright/test'
import type { AlertRuleChange, AlertRuleConfig, AlertRulePreview, AlertRuleSpec, AlertRulesSnapshot } from '../src/api'

const readPath = '/api/v1/alert_config', writePath = '/api/v1/manage/alert-rules'
const headers = { Authorization: 'Bearer browser-test-token' }
const pretty = (value: unknown) => JSON.stringify(value, null, 2)
const spec = (name: string): AlertRuleSpec => ({ name, on: 'system.ram', calc: '$used', every: '10s', warn: '$this > 80', info: '原始规则' })
function rule(name: string, origin: AlertRuleConfig['origin']): AlertRuleConfig {
  return { hash: name, name, on: 'system.ram', source: origin === 'custom' ? 'managed' : 'builtin', every: 10,
    config: spec(name), origin, has_base: origin !== 'custom', deleted: origin === 'deleted' }
}
function gate() {
  let release!: () => void
  const wait = new Promise<void>(resolve => { release = resolve })
  return { wait, release }
}
async function fixture(page: Page, role = 'admin') {
  const state = {
    snapshot: { api: 1, revision: 'opaque-1', persistent: true, hostname: 'rules-local-hub', scope: 'local', can_manage: role === 'admin',
      configs: [rule('base_ram', 'base'), rule('custom_ram', 'custom'), rule('overridden_ram', 'override'), rule('deleted_ram', 'deleted'),
        { ...rule('orphaned_deletion', 'deleted'), has_base: false, on: '', config: { name: 'orphaned_deletion' } }], count: 5 } as AlertRulesSnapshot,
    previews: [] as AlertRuleChange[], writes: [] as AlertRuleChange[], urls: [] as string[], errors: [] as string[],
    readStatus: 200, writeStatus: 200, ambiguousWrite: false,
    previewGate: undefined as ReturnType<typeof gate> | undefined,
    previewCompleted: undefined as ReturnType<typeof gate> | undefined,
    writeGate: undefined as ReturnType<typeof gate> | undefined,
  }
  page.on('pageerror', error => state.errors.push(error.message))
  function apply(body: AlertRuleChange) {
    const name = 'config' in body ? body.config.name : body.name
    const current = state.snapshot.configs.find(item => item.name === name)
    if (body.action === 'delete') {
      if (current?.has_base) Object.assign(current, { config: spec(name), origin: 'deleted', deleted: true })
      else state.snapshot.configs = state.snapshot.configs.filter(item => item.name !== name)
    }
    else if (body.action === 'reset') {
      if (current?.has_base) Object.assign(current, { config: spec(name), origin: 'base', deleted: false })
      else state.snapshot.configs = state.snapshot.configs.filter(item => item.name !== name)
    }
    else if ('config' in body) {
      if (current) Object.assign(current, { config: body.config, origin: current.has_base ? 'override' : 'custom' })
      else state.snapshot.configs.push({ ...rule(name, 'custom'), config: body.config })
    }
    state.snapshot.revision = `opaque-${Number(state.snapshot.revision.split('-')[1]) + 1}`
    state.snapshot.count = state.snapshot.configs.length
  }
  async function preview(route: Route) {
    const body = route.request().postDataJSON() as AlertRuleChange
    state.previews.push(body)
    const wait = state.previewGate, completed = state.previewCompleted
    const response: AlertRulePreview = { revision: body.revision, action: body.action, name: 'config' in body ? body.config.name : body.name,
      valid: true, persistent: true, matched: 1, charts: [{ id: 'system.ram', title: '内存', context: 'system.ram', family: 'memory' }],
      truncated: false, limit: 50, disabled: 'config' in body && body.config.disabled === true, notice: '只预览匹配范围' }
    if (wait) await wait.wait
    await route.fulfill(body.revision === state.snapshot.revision ? { json: response } : { status: 409, body: 'revision conflict' }).catch(() => {})
    completed?.release()
  }
  await page.route('**/api/v1/**', async route => {
    const url = new URL(route.request().url())
    if (url.pathname === readPath || url.pathname.startsWith(writePath)) state.urls.push(url.href)
    if (url.pathname === readPath) return route.fulfill(state.readStatus === 200 ? { json: state.snapshot } : { status: state.readStatus, body: 'unavailable' })
    if (url.pathname === writePath + '/preview') return preview(route)
    if (url.pathname === writePath) {
      const body = route.request().postDataJSON() as AlertRuleChange
      state.writes.push(body)
      if (state.writeGate) await state.writeGate.wait
      if (state.writeStatus !== 200) return route.fulfill({ status: state.writeStatus, body: 'save rejected' })
      if (body.revision !== state.snapshot.revision) return route.fulfill({ status: 409, body: 'revision conflict' })
      apply(body)
      return state.ambiguousWrite ? route.abort() : route.fulfill({ json: state.snapshot })
    }
    const host = { id: 'remote-1', hostname: 'selected-remote-host', os: 'linux', arch: 'amd64', labels: {}, update_every: 1 }
    const replies: Record<string, unknown> = {
      '/api/v1/info': { version: 'test', mode: 'hub', uptime: 1, charts_count: 0, metrics_count: 0, host,
        collectors: [], alarms: null, user: { name: 'browser-' + role, role } },
      '/api/v1/nodes': { now: 100, nodes: [{ ...host, status: 'live', local: false, first_seen: 1, last_seen: 100, charts_count: 0, alarms: { warning: 0, critical: 0 } }] },
      '/api/v1/charts': { charts: {} }, '/api/v1/functions': [], '/api/v1/auth/oidc/status': { enabled: false },
      '/api/v1/alarms': { alarms: {} }, '/api/v1/alarm_log': [],
    }
    if (url.pathname in replies) return route.fulfill({ json: replies[url.pathname] })
    return route.continue()
  })
  await page.goto('/?view=charts&node=remote-1&token=browser-test-token')
  await page.getByRole('button', { name: '告警规则', exact: true }).click()
  const panel = page.getByRole('region', { name: '本机告警规则', exact: true })
  await expect(panel).toContainText('rules-local-hub')
  return { state, panel }
}

test('local rules validate JSON, freeze the preview payload, save and retain definitions after reload', async ({ page }, info) => {
  const { state, panel } = await fixture(page)
  await expect(panel).toContainText('当前选择远端节点时，此处仍管理本机规则')
  await panel.getByLabel('搜索告警规则').fill('base_ram')
  await expect(panel.locator('[data-rule-name]')).toHaveCount(1)
  await panel.locator('[data-rule-name="base_ram"]').click()
  const editor = panel.getByLabel('规则 JSON'), validate = panel.getByRole('button', { name: '校验并预览', exact: true })
  await editor.fill('{ broken')
  await validate.click()
  await expect(panel.getByRole('alert')).toContainText('JSON 无效')
  await editor.fill(pretty({ ...spec('base_ram'), unsupported: true }))
  await validate.click()
  await expect(panel.getByRole('alert')).toContainText('不支持字段 unsupported')
  expect(state.previews).toHaveLength(0)
  const updated = { ...spec('base_ram'), info: '我的规则定义', for: '30s', keep_firing_for: '1m' }
  await editor.fill(pretty(updated))
  await validate.click()
  await expect(panel.getByRole('region', { name: '告警规则预览' })).toContainText('当前匹配 1 个图表')
  await expect(panel).toContainText('不执行告警表达式，也不发送通知')
  await editor.fill(pretty({ ...updated, warn: '$this > 95' }))
  await expect(panel.getByRole('region', { name: '告警规则预览' })).toHaveCount(0)
  await validate.click()
  await panel.getByRole('button', { name: '确认保存规则变更' }).click()
  await expect(panel.getByRole('status')).toContainText('重启后继续生效')
  expect(state.writes).toEqual([state.previews[1]])
  expect(state.writes[0]).toMatchObject({ revision: 'opaque-1', action: 'update', config: { warn: '$this > 95', for: '30s', keep_firing_for: '1m' } })
  expect(state.urls.every(url => !new URL(url).searchParams.has('node'))).toBe(true)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await panel.screenshot({ path: info.outputPath('alert-rules.png') })
  await page.reload()
  await page.getByRole('button', { name: '告警规则', exact: true }).click()
  await panel.locator('[data-rule-name="base_ram"]').click()
  await expect(editor).toHaveValue(pretty({ ...updated, warn: '$this > 95' }))
  expect(state.errors).toEqual([])
})

test('disable, delete custom or configuration rules, and restore all require their own preview', async ({ page }) => {
  const { state, panel } = await fixture(page)
  await panel.locator('[data-rule-name="base_ram"]').click()
  await expect(panel.getByLabel('规则操作').locator('option')).toHaveText(['编辑规则', '删除规则'])
  await panel.getByRole('button', { name: '切换启用 / 禁用草稿' }).click()
  expect(JSON.parse(await panel.getByLabel('规则 JSON').inputValue()).disabled).toBe(true)
  await panel.getByRole('button', { name: '校验并预览', exact: true }).click()
  await expect(panel.getByRole('region', { name: '告警规则预览' })).toContainText('规则将保持禁用')
  await panel.getByRole('button', { name: '确认保存规则变更' }).click()
  await expect(panel.locator('[data-rule-name="base_ram"]')).toContainText('已禁用')
  await panel.getByLabel('规则操作').selectOption('delete')
  await expect(panel).toContainText('保留删除标记，之后可恢复配置规则')
  await panel.getByRole('button', { name: '校验并预览', exact: true }).click()
  await panel.getByRole('button', { name: '确认保存规则变更' }).click()
  await expect(panel.locator('[data-rule-name="base_ram"]')).toContainText('已删除')
  await expect(panel.getByLabel('规则操作')).toHaveValue('reset')
  await panel.getByRole('button', { name: '校验并预览', exact: true }).click()
  await panel.getByRole('button', { name: '确认保存规则变更' }).click()
  await expect(panel.locator('[data-rule-name="base_ram"]')).toContainText('基础规则 · 已启用')
  await panel.locator('[data-rule-name="custom_ram"]').click()
  await panel.getByLabel('规则操作').selectOption('delete')
  await expect(panel.getByLabel('规则 JSON')).toHaveAttribute('readonly', '')
  await panel.getByRole('button', { name: '校验并预览', exact: true }).click()
  await panel.getByRole('button', { name: '确认保存规则变更' }).click()
  await expect(panel.locator('[data-rule-name="custom_ram"]')).toHaveCount(0)
  for (const name of ['overridden_ram', 'deleted_ram']) {
    await panel.locator(`[data-rule-name="${name}"]`).click()
    await panel.getByLabel('规则操作').selectOption('reset')
    await panel.getByRole('button', { name: '校验并预览', exact: true }).click()
    await panel.getByRole('button', { name: '确认保存规则变更' }).click()
    await expect(panel.locator(`[data-rule-name="${name}"]`)).toContainText('基础规则 · 已启用')
  }
  await panel.locator('[data-rule-name="orphaned_deletion"]').click()
  await expect(panel.getByLabel('规则操作').locator('option')).toHaveText(['清除失效删除标记'])
  await expect(panel).toContainText('不会新建规则')
  await panel.getByRole('button', { name: '校验并预览', exact: true }).click()
  await panel.getByRole('button', { name: '确认保存规则变更' }).click()
  await expect(panel.locator('[data-rule-name="orphaned_deletion"]')).toHaveCount(0)
  expect(state.writes.map(body => body.action)).toEqual(['update', 'delete', 'reset', 'delete', 'reset', 'reset', 'reset'])
  expect(state.writes).toEqual(state.previews)
  expect(state.errors).toEqual([])
})

test('conflict preserves the exact draft and requires explicit revision review before another preview', async ({ page }) => {
  const { state, panel } = await fixture(page)
  await panel.locator('[data-rule-name="base_ram"]').click()
  const json = pretty({ ...spec('base_ram'), info: '保留我的尚未保存内容' }), editor = panel.getByLabel('规则 JSON')
  await editor.fill(json)
  await panel.getByRole('button', { name: '校验并预览', exact: true }).click()
  state.snapshot.revision = 'opaque-2'
  state.snapshot.configs[0]!.config.info = '其他管理员修改'
  await panel.getByRole('button', { name: '确认保存规则变更' }).click()
  await expect(panel.getByRole('alert')).toContainText('草稿已保留')
  expect(state.writes[0]?.revision).toBe('opaque-1')
  await expect(editor).toHaveValue(json)
  await panel.getByRole('button', { name: '刷新规则列表' }).click()
  await expect(panel.getByRole('button', { name: '校验并预览', exact: true })).toBeDisabled()
  await panel.getByRole('button', { name: '读取最新并重新核对' }).click()
  await expect(editor).toHaveValue(json)
  await expect(panel.getByRole('region', { name: '告警规则预览' })).toHaveCount(0)
  await panel.getByText('查看服务器当前定义，与草稿核对', { exact: true }).click()
  await expect(panel.locator('.latest pre')).toContainText('其他管理员修改')
  await panel.getByRole('button', { name: '校验并预览', exact: true }).click()
  expect(state.previews.at(-1)?.revision).toBe('opaque-2')
  await panel.getByRole('button', { name: '确认保存规则变更' }).click()
  await expect(panel.getByRole('status')).toContainText('已保存并生效')
  expect(state.errors).toEqual([])
})

test('pending previews are canceled on draft changes and panel close without stale results or unhandled errors', async ({ page }) => {
  const { state, panel } = await fixture(page)
  await panel.locator('[data-rule-name="base_ram"]').click()
  for (const change of ['edit', 'close']) {
    state.previewGate = gate(); state.previewCompleted = gate()
    const start = state.previews.length
    await panel.getByRole('button', { name: '校验并预览', exact: true }).click()
    await expect.poll(() => state.previews.length).toBe(start + 1)
    const aborted = page.waitForEvent('requestfailed', request => new URL(request.url()).pathname === writePath + '/preview')
    if (change === 'edit') await panel.getByLabel('规则 JSON').fill(pretty({ ...spec('base_ram'), info: '更新中的草稿' }))
    else { page.once('dialog', dialog => dialog.accept()); await panel.getByRole('button', { name: '关闭告警规则' }).click() }
    await aborted
    state.previewGate.release(); await state.previewCompleted.wait
    await expect(page.getByRole('region', { name: '告警规则预览' })).toHaveCount(0)
  }
  expect(state.writes).toHaveLength(0)
  expect(state.errors).toEqual([])
})

test('failed and ambiguous writes are honest, retain drafts and prevent blind retries', async ({ page }) => {
  const { state, panel } = await fixture(page)
  await panel.getByRole('button', { name: '新建规则', exact: true }).click()
  const json = pretty(spec('my_custom_rule'))
  await panel.getByLabel('规则 JSON').fill(json)
  await panel.getByRole('button', { name: '校验并预览', exact: true }).click()
  for (const status of [422, 503]) {
    state.writeStatus = status
    await panel.getByRole('button', { name: '确认保存规则变更' }).click()
    await expect.poll(() => state.writes.length).toBe(status === 422 ? 1 : 2)
    await expect(panel.getByRole('alert')).toContainText('本次未修改')
    await expect(panel.getByRole('button', { name: '确认保存规则变更' })).toBeEnabled()
    await expect(panel.getByLabel('规则 JSON')).toHaveValue(json)
    expect(state.snapshot.configs.some(rule => rule.name === 'my_custom_rule')).toBe(false)
  }
  state.writeStatus = 200; state.ambiguousWrite = true
  await panel.getByRole('button', { name: '确认保存规则变更' }).click()
  await expect(panel.getByRole('alert')).toContainText('保存结果不确定')
  await expect(panel.getByRole('button', { name: '确认保存规则变更' })).toHaveCount(0)
  await expect(panel.getByRole('button', { name: '校验并预览', exact: true })).toBeDisabled()
  await panel.getByRole('button', { name: '读取最新并重新核对' }).click()
  await expect(panel.locator('[data-rule-name="my_custom_rule"]')).toBeVisible()
  expect(state.writes).toHaveLength(3)
  state.readStatus = 401
  await panel.getByRole('button', { name: '刷新规则列表' }).click()
  await expect(panel.getByRole('alert')).toContainText('请重新登录')
  await expect(panel.locator('[data-rule-name]')).toHaveCount(0)
  expect(state.errors).toEqual([])
})

test('viewer can inspect deleted base definitions without write controls or a false dirty-close prompt', async ({ page }) => {
  const { state, panel } = await fixture(page, 'viewer')
  await expect(panel).toContainText('当前账号只读')
  await expect(panel.getByRole('button', { name: '新建规则', exact: true })).toHaveCount(0)
  await panel.locator('[data-rule-name="deleted_ram"]').click()
  await expect(panel.getByLabel('规则 JSON')).toHaveAttribute('readonly', '')
  await expect(panel.getByRole('button', { name: '校验并预览', exact: true })).toHaveCount(0)
  let dialogs = 0
  page.on('dialog', async dialog => { dialogs++; await dialog.dismiss() })
  await panel.getByRole('button', { name: '关闭告警规则' }).click()
  await expect(panel).toHaveCount(0)
  expect(dialogs).toBe(0)
  expect(state.writes).toEqual([])
  expect(state.previews).toEqual([])
})

test('dirty-close guards both header and panel, and in-flight saves freeze duplicate writes and closing', async ({ page }) => {
  const { state, panel } = await fixture(page)
  await panel.locator('[data-rule-name="base_ram"]').click()
  const json = pretty({ ...spec('base_ram'), info: '离开前保留' })
  await panel.getByLabel('规则 JSON').fill(json)
  page.once('dialog', dialog => dialog.dismiss())
  await page.getByRole('button', { name: '告警规则', exact: true }).click()
  await expect(panel.getByLabel('规则 JSON')).toHaveValue(json)
  await panel.getByRole('button', { name: '校验并预览', exact: true }).click()
  state.writeGate = gate()
  await panel.getByRole('button', { name: '确认保存规则变更' }).click()
  await expect.poll(() => state.writes.length).toBe(1)
  await expect(panel.getByRole('button', { name: '保存中…', exact: true })).toBeDisabled()
  await expect(panel.getByRole('button', { name: '关闭告警规则' })).toBeDisabled()
  await page.getByRole('button', { name: '告警规则', exact: true }).click()
  await expect(panel).toBeVisible()
  state.writeGate.release()
  await expect(panel.getByRole('status')).toContainText('已保存并生效')
  expect(state.writes).toHaveLength(1)
  await panel.getByLabel('规则 JSON').fill(json + ' ')
  page.once('dialog', dialog => dialog.accept())
  await panel.getByRole('button', { name: '关闭告警规则' }).click()
  await expect(panel).toHaveCount(0)
})

test('disabled health engine reports unavailable and blocks stale-list edits', async ({ page }) => {
  const { state, panel } = await fixture(page)
  await panel.locator('[data-rule-name="base_ram"]').click()
  state.readStatus = 404
  await panel.getByRole('button', { name: '刷新规则列表' }).click()
  await expect(panel.getByRole('alert')).toContainText('本机健康引擎未启用')
  await expect(panel.getByLabel('规则 JSON')).toHaveAttribute('readonly', '')
  await expect(panel.getByRole('button', { name: '校验并预览', exact: true })).toHaveCount(0)
  expect(state.writes).toHaveLength(0)
})

test('real API creates a disabled rule after preview, survives browser reload, and deletes it', async ({ page, request }, info) => {
  const name = `browser_rules_e2e_${info.project.name}`
  try {
    await page.goto('/?token=browser-test-token&view=charts')
    await page.getByRole('button', { name: '告警规则', exact: true }).click()
    const panel = page.getByRole('region', { name: '本机告警规则', exact: true })
    await expect(panel).toContainText('实例：browser-test')
    await panel.getByRole('button', { name: '新建规则', exact: true }).click()
    await panel.getByLabel('规则 JSON').fill(pretty({ ...spec(name), disabled: true }))
    await panel.getByRole('button', { name: '校验并预览', exact: true }).click()
    await expect(panel.getByRole('region', { name: '告警规则预览' })).toContainText('system.ram')
    await panel.getByRole('button', { name: '确认保存规则变更' }).click()
    await expect(panel.getByRole('status')).toContainText('重启后继续生效')
    let snapshot: AlertRulesSnapshot = await (await request.get(readPath, { headers })).json()
    expect(snapshot.configs.find(rule => rule.name === name)).toMatchObject({ origin: 'custom', config: { disabled: true } })
    await page.reload()
    await page.getByRole('button', { name: '告警规则', exact: true }).click()
    await panel.locator(`[data-rule-name="${name}"]`).click()
    expect(JSON.parse(await panel.getByLabel('规则 JSON').inputValue()).disabled).toBe(true)
    await panel.getByLabel('规则操作').selectOption('delete')
    await panel.getByRole('button', { name: '校验并预览', exact: true }).click()
    await panel.getByRole('button', { name: '确认保存规则变更' }).click()
    await expect(panel.locator(`[data-rule-name="${name}"]`)).toHaveCount(0)
    snapshot = await (await request.get(readPath, { headers })).json()
    for (const token of ['browser-viewer-token', 'browser-operator-token']) {
      const denied = await request.post(writePath + '/preview', { headers: { Authorization: 'Bearer ' + token },
        data: { revision: snapshot.revision, action: 'create', config: { ...spec(name), disabled: true } } })
      expect(denied.status()).toBe(403)
    }
  } finally {
    const snapshot: AlertRulesSnapshot = await (await request.get(readPath, { headers })).json()
    if (snapshot.configs.some(rule => rule.name === name)) {
      expect((await request.post(writePath, { headers, data: { revision: snapshot.revision, action: 'delete', name } })).ok()).toBe(true)
    }
  }
})
