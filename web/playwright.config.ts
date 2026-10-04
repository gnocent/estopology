import { defineConfig, devices } from '@playwright/test';

// End-to-end tests of the UI embedded in estopo, against essim clusters
// (development only, SPEC §13.2). Run with: npm run test:e2e
export default defineConfig({
  testDir: './e2e',
  timeout: 30_000,
  fullyParallel: false,
  workers: 1,
  reporter: [['list']],
  use: {
    baseURL: 'http://127.0.0.1:18093',
    trace: 'retain-on-failure',
    locale: 'fr-FR',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'], viewport: { width: 1500, height: 950 } } }],
  webServer: {
    command: 'bash e2e/serve.sh',
    url: 'http://127.0.0.1:18093/api/v1/clusters',
    timeout: 240_000,
    reuseExistingServer: false,
    stdout: 'pipe',
  },
});
