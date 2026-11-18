import { expect, test } from '@playwright/test'

test('a stalled module exits loading and refresh recovers it', async ({ page }) => {
  await page.clock.install()
  let stalled = true, intercepted = false
  await page.route('**/assets/SetupPanel-*.js', route => {
    intercepted = true
    if (!stalled) return route.continue()
  })
  await page.goto('/?view=setup&token=browser-test-token', { waitUntil: 'domcontentloaded' })
  await expect.poll(() => intercepted).toBe(true)
  await page.clock.fastForward(31_000)
  const error = page.locator('.module-error')
  await expect(error).toContainText('当前模块未能加载或运行')
  await expect(page.getByRole('status', { name: '正在加载工作区' })).toHaveCount(0)
  stalled = false
  await error.getByRole('button', { name: '刷新页面' }).click()
  await expect(page.getByRole('region', { name: '接入与运行检查' })).toBeVisible()
  await expect(error).toHaveCount(0)
})

test('a failed module download exposes recovery instead of a blank workspace', async ({ page }) => {
  let blocked = true
  await page.route('**/assets/SetupPanel-*.js', route => blocked ? route.abort('failed') : route.continue())
  await page.goto('/?view=setup&token=browser-test-token')
  const error = page.locator('.module-error')
  await expect(error).toBeVisible()
  blocked = false
  await error.getByRole('button', { name: '刷新页面' }).click()
  await expect(page.getByRole('region', { name: '接入与运行检查' })).toBeVisible()
})
