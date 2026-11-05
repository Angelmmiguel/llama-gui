import { defineConfig, devices } from '@playwright/test';

// Browser tests against the built app the Go binary embeds, so the real CSP
// and the real API are exercised. Specs start their own throwaway server
// through tests/server.ts; `just web-e2e` builds the app first.
export default defineConfig({
	testDir: 'tests/e2e',
	fullyParallel: false,
	workers: 1,
	retries: 0,
	reporter: 'list',
	timeout: 60_000,
	expect: { timeout: 10_000 },
	use: { trace: 'retain-on-failure' },
	projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }]
});
