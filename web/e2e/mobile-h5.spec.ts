import { expect, test, type Page } from '@playwright/test'

const token = 'browser-test-token'

async function noPageOverflow(page: Page) {
  await expect.poll(() => page.evaluate(() =>
    Math.max(document.documentElement.scrollWidth, document.body.scrollWidth) - window.innerWidth,
  ), { message: 'The phone page must fit the viewport without horizontal scrolling.' }).toBeLessThanOrEqual(0)
}

function mobileNav(page: Page) {
  return page.getByRole('navigation', { name: '手机导航', exact: true })
}

async function more(page: Page) {
  await mobileNav(page).getByRole('button', { name: '更多', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '更多功能', exact: true })
  await expect(dialog).toBeVisible()
  return dialog
}

test('phone dock opens a tool with a normal click', async ({ page, isMobile }) => {
  test.skip(!isMobile, 'Phone dock only.')
  await page.goto('/?view=charts&token=' + token)
  await expect(page.locator('.metrics-workspace')).toBeVisible()
  const dialog = await more(page)
  await dialog.getByTitle('日志', { exact: true }).click()
  await expect(page.getByRole('heading', { name: /^日志/ })).toBeVisible()
  await mobileNav(page).getByRole('button', { name: '图表', exact: true }).click()
  await expect(page.locator('.metrics-workspace')).toBeVisible()
  await noPageOverflow(page)
})

test.describe('H5 phone workspace', () => {
  test.skip(({ isMobile }) => !isMobile, 'Phone-only navigation is tested in the mobile project.')

  for (const width of [320, 390, 430]) {
    test(`${width}px supports charts, alarms, services and a dismissible menu without page overflow`, async ({ page }, info) => {
      await page.setViewportSize({ width, height: 844 })
      const errors: string[] = []
      page.on('pageerror', error => errors.push(error.message))
      await page.route('**/api/v1/services*', route => route.fulfill({ json: {
        from: 1, to: 2, services: [{ name: 'checkout-production-with-a-long-service-name', status: 'WARNING', sla: 97.5, alarms: ['payment_database_connection_pool_exhausted'], children: [] }],
      } }))
      await page.route('**/api/v1/topology', route => route.fulfill({ json: {
        edges: [{ source: 'production-database-primary', target: 'production-api-checkout-service', kind: 'manual' }],
      } }))
      await page.route('**/api/v1/reports/availability*', route => route.fulfill({ json: {
        from: 1, to: 2, alarms: [{ name: 'payment_database_connection_pool_exhausted', chart: 'http.checkout-production-with-a-long-service-name', uptime: 97.5, raised_seconds: 90, window_seconds: 3600 }],
      } }))
      await page.goto('/?token=' + token)
      const nav = mobileNav(page)
      await expect(nav).toBeVisible()
      await expect(nav.getByRole('button')).toHaveCount(4)
      for (const button of await nav.getByRole('button').all()) {
        const box = await button.boundingBox()
        expect(box?.height).toBeGreaterThanOrEqual(44)
        expect(box?.width).toBeGreaterThanOrEqual(44)
      }
      await noPageOverflow(page)
      await nav.getByRole('button', { name: '图表', exact: true }).click()
      expect(await page.getByPlaceholder('筛选图表…').evaluate(element => getComputedStyle(element).fontSize)).toBe('16px')
      expect(await page.locator('.controls select').evaluate(element => getComputedStyle(element).fontSize)).toBe('16px')
      await page.getByPlaceholder('筛选图表…').fill('system.ram')
      await expect(page.locator('.card')).toHaveCount(1)
      await expect(page.locator('.card canvas').first()).toBeVisible()
      await Promise.all([
        page.waitForResponse(response => response.url().includes('/api/v1/data?') && response.url().includes('after=-3600')),
        page.locator('.controls select').selectOption('3600'),
      ])
      await page.getByRole('button', { name: '设置历史时间', exact: true }).click()
      await expect(page.getByLabel('看板结束时间（本地）', { exact: true })).toBeVisible()
      await noPageOverflow(page)
      await page.screenshot({ path: info.outputPath(`h5-charts-${width}.png`), fullPage: true })
      await nav.getByRole('button', { name: /^告警/ }).click()
      await expect(page.locator('.panel').getByRole('heading', { name: /告警/ })).toBeVisible()
      await expect(page.locator('.panel')).toContainText('browser_ram_notice')
      await noPageOverflow(page)
      await page.screenshot({ path: info.outputPath(`h5-alarms-${width}.png`), fullPage: true })
      const dialog = await more(page)
      await noPageOverflow(page)
      await expect(dialog.getByRole('button', { name: '关闭更多功能', exact: true })).toBeFocused()
      await page.keyboard.press('Tab')
      await expect(dialog.getByRole('button', { name: '服务与拓扑', exact: true })).toBeFocused()
      await page.keyboard.press('Shift+Tab')
      await expect(dialog.getByRole('button', { name: '关闭更多功能', exact: true })).toBeFocused()
      await dialog.getByRole('button', { name: '关闭更多功能', exact: true }).click()
      await expect(dialog).toBeHidden()
      await expect(nav.getByRole('button', { name: '更多', exact: true })).toBeFocused()
      await more(page)
      await page.keyboard.press('Escape')
      await expect(dialog).toBeHidden()
      await expect(nav.getByRole('button', { name: '更多', exact: true })).toBeFocused()
      const reopened = await more(page)
      await reopened.getByRole('button', { name: '服务与拓扑', exact: true }).click()
      await expect(reopened).toBeHidden()
      await expect(page.locator('.services').getByRole('heading', { name: '服务与拓扑' })).toBeVisible()
      await expect(page.locator('.services')).toContainText('97.5%')
      await noPageOverflow(page)
      await page.screenshot({ path: info.outputPath(`h5-services-${width}.png`), fullPage: true })
      await nav.getByRole('button', { name: '总览', exact: true }).click()
      await expect(page.locator('.services')).toBeHidden()
      await noPageOverflow(page)
      expect(errors).toEqual([])
    })
  }

  test('phone login retains authentication and viewer role restrictions, and logout clears the session', async ({ page, request }) => {
    expect((await request.get('/api/v1/info')).status()).toBe(401)
    await page.goto('/')
    const password = page.getByPlaceholder('登录密码或访问令牌', { exact: true })
    await expect(password).toBeVisible()
    await expect(mobileNav(page)).toBeHidden()
    await password.fill('wrong-password')
    await page.getByRole('button', { name: '进入', exact: true }).click()
    await expect(page.getByRole('alert')).toContainText('不正确')
    await password.fill('browser-viewer-token')
    await page.getByRole('button', { name: '进入', exact: true }).click()
    await expect(mobileNav(page)).toBeVisible()
    const dialog = await more(page)
    await expect(dialog.getByTitle('配置：本机与节点', { exact: true })).toHaveCount(0)
    await page.keyboard.press('Escape')
    await mobileNav(page).getByRole('button', { name: /^告警/ }).click()
    await expect(page.locator('.panel')).toContainText('browser_ram_notice')
    await expect(page.locator('.panel').getByRole('button', { name: /全部静默|静默 1h|关闭问题/ })).toHaveCount(0)
    expect((await request.post('/api/v1/alarms/silence', {
      headers: { Authorization: 'Bearer browser-viewer-token' }, data: { all: true, until: -60 },
    })).status()).toBe(403)
    await more(page)
    await page.getByRole('button', { name: '退出登录', exact: true }).click()
    await expect(password).toBeVisible()
    await expect(mobileNav(page)).toBeHidden()
    expect(await page.evaluate(() => sessionStorage.getItem('monitor.token'))).toBeFalsy()
  })

  test('home screen shortcuts contain no credentials and still require login', async ({ page, request }) => {
    const response = await request.get('/manifest.webmanifest')
    expect(response.ok()).toBe(true)
    const manifest = await response.json()
    expect(manifest.display).toBe('standalone')
    for (const icon of manifest.icons) {
      const iconResponse = await request.get(icon.src)
      expect(iconResponse.status()).toBe(200)
      expect(iconResponse.headers()['content-type']).toContain('image/png')
    }
    const appleIcon = await request.get('/icons/apple-touch-icon.png')
    expect(appleIcon.status()).toBe(200)
    expect(appleIcon.headers()['content-type']).toContain('image/png')
    const entries = [manifest.start_url, ...manifest.shortcuts.map((entry: { url: string }) => entry.url)]
    for (const entry of entries) {
      const url = new URL(entry, response.url())
      expect(url.origin).toBe(new URL(response.url()).origin)
      expect(url.username).toBe('')
      expect(url.password).toBe('')
      expect([...url.searchParams.keys()]).toEqual(['view'])
    }
    await page.goto('/?view=alarms')
    await expect(page.getByPlaceholder('登录密码或访问令牌', { exact: true })).toBeVisible()
    await expect(page.locator('.panel')).toHaveCount(0)
    await expect(mobileNav(page)).toBeHidden()
    expect(await page.evaluate(async () => (await navigator.serviceWorker.getRegistrations()).length)).toBe(0)
    expect(await page.evaluate(async () => await caches.keys())).toEqual([])
  })

  test('resizing with the phone menu open restores scrolling and the desktop toolbar', async ({ page }) => {
    await page.goto('/?view=charts&token=' + token)
    const originalOverflow = await page.evaluate(() => document.body.style.overflow)
    const dialog = await more(page)
    expect(await page.evaluate(() => document.body.style.overflow)).toBe('hidden')
    await page.setViewportSize({ width: 1280, height: 900 })
    await expect(dialog).toHaveCount(0)
    await expect(mobileNav(page)).toBeHidden()
    expect(await page.evaluate(() => document.body.style.overflow)).toBe(originalOverflow)
    await expect(page.getByTitle('本机告警规则', { exact: true })).toBeVisible()
    await page.getByTitle('本机告警规则', { exact: true }).click()
    await expect(page.getByRole('heading', { name: '本机告警规则', exact: true })).toBeVisible()
    await page.setViewportSize({ width: 390, height: 844 })
    const phoneDialog = await more(page)
    await phoneDialog.getByRole('button', { name: '关闭更多功能', exact: true }).click()
    await expect(phoneDialog).toBeHidden()
    await expect(mobileNav(page).getByRole('button', { name: '更多', exact: true })).toBeFocused()
    expect(await page.evaluate(() => document.body.style.overflow)).toBe(originalOverflow)
  })

  test('phone node selection scopes chart requests and preserves the selected node across reload', async ({ page }) => {
    const remote = 'phone-remote-node'
    const node = { os: 'linux', arch: 'arm64', version: 'test', status: 'live', first_seen: 1, last_seen: 2, alarms: {}, charts_count: 1 }
    await page.route('**/api/v1/info', async route => {
      const response = await route.fetch()
      await route.fulfill({ json: { ...await response.json(), mode: 'hub' } })
    })
    await page.route('**/api/v1/nodes*', route => route.fulfill({ json: { now: 2, nodes: [
      { ...node, id: '', hostname: 'hub-local', local: true },
      { ...node, id: remote, hostname: 'production-remote-node-with-a-long-hostname', local: false },
    ] } }))
    await page.route('**/api/v1/charts*', route => route.fulfill({ json: { charts: { 'system.ram': {
      id: 'system.ram', title: 'Memory', context: 'system.ram', family: 'ram', units: 'MiB', chart_type: 'line', priority: 1,
      update_every: 1, dimensions: [{ id: 'used', name: 'Used' }], last_entry: Math.floor(Date.now() / 1000),
    } } } }))
    const scoped: string[] = []
    await page.route('**/api/v1/data?*', route => {
      scoped.push(new URL(route.request().url()).searchParams.get('node') ?? '')
      const now = Math.floor(Date.now() / 1000)
      return route.fulfill({ json: { units: 'MiB', dimension_ids: ['used'], result: { data: [[now - 1, 10], [now, 12]] } } })
    })
    await page.goto('/?view=charts&token=' + token)
    await expect(page.locator('.card canvas').first()).toBeVisible()
    await page.getByTitle('节点', { exact: true }).click()
    await page.locator('.node-menu').getByRole('option', { name: /production-remote-node/ }).click()
    await expect.poll(() => scoped.includes(remote), { timeout: 15000 }).toBe(true)
    await expect(page.getByTitle('节点', { exact: true })).toContainText('production-remote-node')
    await noPageOverflow(page)
    await page.reload()
    await expect(page.getByTitle('节点', { exact: true })).toContainText('production-remote-node')
    expect(await page.evaluate(() => sessionStorage.getItem('monitor.node'))).toBe(remote)
    expect(new URL(page.url()).searchParams.has('token')).toBe(false)
  })
})

test('desktop keeps its navigation without displaying the phone dock', async ({ page, isMobile }) => {
  test.skip(isMobile, 'Desktop-only check.')
  await page.goto('/?view=charts&token=' + token)
  await expect(page.locator('.card').first()).toBeVisible()
  await expect(mobileNav(page)).toBeHidden()
  await expect(page.getByRole('button', { name: '运维总览', exact: true })).toBeVisible()
  await noPageOverflow(page)
})
