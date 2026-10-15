import type { Page } from '@playwright/test'

export async function openOperationsFilters(page: Page) {
  const button = page.getByRole('button', { name: /^更多筛选/ })
  if (await button.getAttribute('aria-expanded') !== 'true') await button.click()
}

export async function openOperationsExports(page: Page) {
  const menu = page.locator('details.export-menu')
  if (!await menu.evaluate(element => (element as HTMLDetailsElement).open)) await menu.locator('summary').click()
}
