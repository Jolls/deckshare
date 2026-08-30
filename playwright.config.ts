import { defineConfig, devices } from '@playwright/test';

// Runs against a live dev server -- start one first with
// `go run ./scripts/run-app start` (tests/e2e/README.md). Not auto-started here: that script
// also owns Postgres and migrations, and CLAUDE.md's dev-server rule is "explicit start only."
export default defineConfig({
  testDir: './tests/e2e',
  fullyParallel: false,
  retries: process.env.CI ? 1 : 0,
  reporter: 'list',
  use: {
    baseURL: 'http://localhost:3000',
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
});
