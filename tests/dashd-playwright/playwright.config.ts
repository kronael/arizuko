import { defineConfig, devices } from '@playwright/test';

// baseURL is written by globalSetup into env DASHD_BASE_URL.
const baseURL = process.env.DASHD_BASE_URL ?? 'http://127.0.0.1:18091';

export default defineConfig({
  testDir: './tests',
  timeout: 30_000,
  fullyParallel: false, // shared dashd + sqlite; serialize for write-isolation
  workers: 1,
  reporter: [['list']],
  globalSetup: require.resolve('./global-setup.ts'),
  globalTeardown: require.resolve('./global-teardown.ts'),
  use: {
    baseURL,
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});
