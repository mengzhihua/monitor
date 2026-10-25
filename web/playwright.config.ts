import { defineConfig, devices } from '@playwright/test'

const port = process.env.MONITOR_E2E_PORT || '19997'

export default defineConfig({
  testDir: './e2e',
  outputDir: './test-results',
  workers: 1,
  timeout: 45000,
  // Keep the first failure observable; retries must not turn flakes into passes.
  retries: 0,
  reporter: process.env.CI ? [
    ['line'],
    ['html', { outputFolder: 'playwright-report', open: 'never' }],
    ['json', { outputFile: 'test-results/results.json' }],
  ] : [['list']],
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    channel: process.env.MONITOR_BROWSER_CHANNEL || undefined,
    trace: 'retain-on-failure', // Includes the first failed attempt, without a retry.
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
    // Let the fixture release its own daemon group, sockets and temporary data.
    gracefulShutdown: { signal: 'SIGTERM', timeout: 10_000 },
    timeout: 30000,
  },
})
