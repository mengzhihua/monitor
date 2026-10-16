import { expect, type Page } from '@playwright/test'

/** The phone toolbar lives in its accessible native dialog. */
export async function openTools(page: Page) {
  const dock = page.getByRole('navigation', { name: '手机导航', exact: true })
  const sheet = page.getByRole('dialog', { name: '更多功能', exact: true })
  if (await page.evaluate(() => matchMedia('(max-width: 760px)').matches) && !await sheet.isVisible()) {
    await expect(dock).toBeVisible()
    await expect(sheet).toBeHidden()
    // Teleported bottom nav sits above #app visually, but Playwright's hit target check can
    // still attribute the tap to #app after a modal sheet closes; force matches real taps.
    await tapDock(dock, '更多')
  }
}

async function tapDock(dock: ReturnType<Page['getByRole']>, name: string) {
  await dock.getByRole('button', { name, exact: true }).click({ force: true })
}

export async function workspace(page: Page, section: 'charts' | 'operations') {
  const dock = page.getByRole('navigation', { name: '手机导航', exact: true })
  if (await page.evaluate(() => matchMedia('(max-width: 760px)').matches)) {
    await expect(dock).toBeVisible()
    await tapDock(dock, section === 'charts' ? '图表' : '总览')
  } else {
    await page.getByRole('button', { name: section === 'charts' ? '指标图表' : '运维总览', exact: true }).click()
  }
}
