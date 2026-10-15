import { expect, test } from '@playwright/test'
import { workspace } from './mobile-navigation'

test('default overview puts problems near the first screen and reveals advanced filters on demand', async ({ page }, info) => {
  if (info.project.name === 'mobile') await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/?token=browser-test-token')
  const first = page.locator('.problem').first()
  await expect(first).toBeVisible()
  await expect(page.getByRole('region', { name: '个人视图' }).getByText('正在同步视图…', { exact: true })).toHaveCount(0)
  await expect(page.getByLabel('搜索主机或问题')).toBeVisible()
  await expect(page.getByLabel('问题级别', { exact: true })).toBeVisible()
  await expect(page.getByLabel('责任人筛选')).toBeHidden()
  await page.evaluate(() => window.scrollTo(0, 0))
  const top = await first.evaluate(element => element.getBoundingClientRect().top)
  const mobile = await page.evaluate(() => matchMedia('(max-width:760px)').matches)
  expect(top).toBeLessThan(mobile ? 850 : 650)
  const titleBottom = await first.locator('.problem-title h3').evaluate(element => element.getBoundingClientRect().bottom)
  const dockTop = mobile ? await page.getByRole('navigation', { name: '手机导航', exact: true }).evaluate(element => element.getBoundingClientRect().top) : null
  if (dockTop !== null) expect(titleBottom).toBeLessThan(dockTop)
  console.info(JSON.stringify({ check: 'operations-first-screen', project: info.project.name, viewport: page.viewportSize(), firstProblemTop: top, firstProblemTitleBottom: titleBottom, mobileNavigationTop: dockTop }))
  await page.screenshot({ path: info.outputPath('operations-first-screen.png'), fullPage: false, scale: 'css' })

  const more = page.getByRole('button', { name: /^更多筛选/ })
  await more.click()
  await page.getByLabel('责任人筛选').selectOption('unassigned')
  await page.getByLabel('处理进度筛选').selectOption('watching')
  await more.click()
  await expect(more).toContainText('2')
  await expect(page.getByText('已启用 2 项筛选', { exact: true })).toBeVisible()
  await expect(page.getByLabel('责任人筛选')).toBeHidden()
  await page.getByRole('button', { name: '清除筛选', exact: true }).click()
  await expect(more).toHaveText('更多筛选')
  await expect(page.getByRole('button', { name: '清除筛选', exact: true })).toHaveCount(0)
  await expect(page.getByLabel('责任人筛选')).toHaveValue('all')
  await expect(page.getByLabel('处理进度筛选')).toHaveValue('all')
})

test('operations mounts only the selected section and preserves problem drafts', async ({ page }, info) => {
  const reads = { history: 0, maintenance: 0, notifications: 0 }
  page.on('request', request => {
    const leaf = new URL(request.url()).pathname.split('/').at(-1)!
    if (leaf in reads) reads[leaf as keyof typeof reads]++
  })
  await page.goto('/?token=browser-operator-token')
  const tabs = page.getByRole('tablist', { name: '运维分区' })
  await expect(tabs.getByRole('tab', { name: '问题中心', exact: true })).toHaveAttribute('aria-selected', 'true')
  const problem = page.locator('.problem').filter({ has: page.getByRole('heading', { name: 'browser_ram_notice', exact: true }) })
  await problem.getByLabel('browser_ram_notice 处理备注').fill('切换分区后继续处理的草稿')
  await expect(page.locator('.node-tile')).toHaveCount(0)
  await expect(page.getByRole('region', { name: '计划维护窗口' })).toHaveCount(0)
  await expect(page.getByRole('region', { name: '通知诊断' })).toHaveCount(0)
  await expect(page.getByRole('region', { name: '处理记录查询' })).toHaveCount(0)
  expect(reads).toEqual({ history: 0, maintenance: 0, notifications: 0 })

  await tabs.getByRole('tab', { name: '主机资源', exact: true }).click()
  await expect(page.locator('.node-tile')).toBeVisible()
  await expect(page.locator('.problem')).toHaveCount(0)
  await expect(page.getByRole('tabpanel', { name: '主机资源' })).toBeVisible()
  await tabs.getByRole('tab', { name: '问题中心', exact: true }).click()
  await expect(problem.getByLabel('browser_ram_notice 处理备注')).toHaveValue('切换分区后继续处理的草稿')

  await tabs.getByRole('tab', { name: '处理历史', exact: true }).click()
  await expect(page.getByRole('region', { name: '处理记录查询' })).toBeVisible()
  expect(reads.history).toBeGreaterThan(0)
  expect(reads.maintenance).toBe(0)
  expect(reads.notifications).toBe(0)
  await tabs.getByRole('tab', { name: '通知诊断', exact: true }).click()
  await expect(page.getByRole('region', { name: '通知诊断' })).toBeVisible()
  await expect(page.getByRole('region', { name: '处理记录查询' })).toHaveCount(0)
  expect(reads.notifications).toBeGreaterThan(0)
  expect(reads.maintenance).toBe(0)
  await page.reload()
  await expect(tabs.getByRole('tab', { name: '通知诊断', exact: true })).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByRole('region', { name: '通知诊断' })).toBeVisible()
  expect(new URL(page.url()).searchParams.has('token')).toBe(false)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.screenshot({ path: info.outputPath('operations-sections.png'), fullPage: true })
})

test('section changes abort outgoing polling and support keyboard navigation', async ({ page }) => {
  let release!: () => void
  const pending = new Promise<void>(resolve => { release = resolve })
  let started = false
  await page.route('**/api/v1/operations/notifications', async route => {
    started = true
    await pending
    await route.fulfill({ json: { available: false } }).catch(() => {})
  })
  try {
    await page.goto('/?token=browser-viewer-token')
    const tabs = page.getByRole('tablist', { name: '运维分区' })
    await tabs.getByRole('tab', { name: '通知诊断', exact: true }).click()
    await expect.poll(() => started).toBe(true)
    const aborted = page.waitForEvent('requestfailed', request => new URL(request.url()).pathname === '/api/v1/operations/notifications')
    await tabs.getByRole('tab', { name: '通知诊断', exact: true }).press('Home')
    await aborted
    await expect(tabs.getByRole('tab', { name: '问题中心', exact: true })).toBeFocused()
    await expect(page.getByRole('region', { name: '通知诊断' })).toHaveCount(0)
    await tabs.getByRole('tab', { name: '问题中心', exact: true }).press('ArrowRight')
    await expect(tabs.getByRole('tab', { name: '主机资源', exact: true })).toBeFocused()
    await expect(page.locator('.node-tile')).toBeVisible()
  } finally {
    release()
  }
})

test('leaving editing work requires a single explicit discard and cancel keeps the draft', async ({ page }) => {
  await page.goto('/?token=browser-test-token')
  await page.getByRole('tab', { name: '维护计划', exact: true }).click()
  const maintenance = page.getByRole('region', { name: '计划维护窗口' })
  await maintenance.locator('summary').click()
  await maintenance.getByLabel('维护标题', { exact: true }).fill('尚未保存的维护计划')
  page.once('dialog', dialog => dialog.dismiss())
  await page.getByRole('tab', { name: '问题中心', exact: true }).click()
  await expect(page.getByRole('tab', { name: '维护计划', exact: true })).toHaveAttribute('aria-selected', 'true')
  await expect(maintenance.getByLabel('维护标题', { exact: true })).toHaveValue('尚未保存的维护计划')
  page.once('dialog', dialog => dialog.accept())
  await page.getByRole('tab', { name: '问题中心', exact: true }).click()
  await expect(maintenance).toHaveCount(0)

  const note = page.getByLabel('browser_ram_notice 处理备注', { exact: true })
  await note.fill('尚未提交的问题备注')
  page.once('dialog', dialog => dialog.dismiss())
  await workspace(page, 'charts')
  await expect(note).toHaveValue('尚未提交的问题备注')
  let dialogs = 0
  page.once('dialog', async dialog => { dialogs++; await dialog.accept() })
  await workspace(page, 'charts')
  await expect(page.locator('.operations')).toHaveCount(0)
  expect(dialogs).toBe(1)
})
