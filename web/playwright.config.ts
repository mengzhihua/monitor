import { defineConfig, devices } from '@playwright/test'

const port = process.env.MONITOR_E2E_PORT || '19997'

export default defineConfig({
  testDir: './e2e',
  workers: 1,
  timeout: 45000,
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    channel: process.env.MONITOR_BROWSER_CHANNEL || undefined,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'desktop', use: { viewport: {width:1280,height:900} } },
    { name: 'mobile', use: {...devices['iPhone 13'], defaultBrowserType:'chromium'} },
  ],
  webServer: {
    command: 'node ../scripts/e2e-server.mjs',
    url: `http://127.0.0.1:${port}/healthz`,
    reuseExistingServer: false,
    timeout: 30000,
  },
})
