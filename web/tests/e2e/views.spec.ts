import { expect, test, type Page } from '@playwright/test';
import { startDemo, type TestServer } from '../server';

let server: TestServer;

test.beforeAll(async () => {
	test.setTimeout(180_000);
	server = await startDemo();
	// So Requests has a chat to show, whatever the generated traffic has got to.
	await fetch(`${server.url}/v1/chat/completions`, {
		method: 'POST',
		body: JSON.stringify({
			model: 'qwen3-coder',
			max_tokens: 4,
			messages: [{ role: 'user', content: 'hello' }]
		})
	});
});

test.afterAll(async () => {
	await server?.stop();
});

/** Fails the test on any CSP refusal or uncaught error while it runs. */
function watchErrors(page: Page): string[] {
	const errors: string[] = [];
	page.on('pageerror', (err) => errors.push(err.message));
	page.on('console', (message) => {
		if (/content security policy|refused to/i.test(message.text())) errors.push(message.text());
	});
	return errors;
}

async function open(page: Page, path: string, heading: string) {
	await page.goto(`${server.url}/ui${path}`);
	await expect(page.getByRole('heading', { level: 1, name: heading })).toBeVisible();
}

async function noPageScrollOnPhone(page: Page) {
	await page.setViewportSize({ width: 390, height: 844 });
	const overflow = await page.evaluate(
		() => document.documentElement.scrollWidth - document.documentElement.clientWidth
	);
	expect(overflow).toBeLessThanOrEqual(0);
}

test('every view renders live data without errors or phone overflow', async ({ page }) => {
	const errors = watchErrors(page);
	const views: [string, string, string | RegExp][] = [
		['/', 'Overview', 'qwen3-coder'],
		['/slots', 'Slots', 'qwen3-coder'],
		['/requests', 'Requests', '/v1/chat/completions'],
		['/logs', 'Logs', /slot|srv/],
		['/library', 'Library', /On this machine/],
		['/profiles', 'Profiles', 'gpt-oss-20b'],
		['/builds', 'Builds', 'b12110'],
		['/bench', 'Bench', 'llama-bench']
	];
	for (const [path, heading, content] of views) {
		await page.setViewportSize({ width: 1440, height: 1000 });
		await open(page, path, heading);
		await expect(
			page.locator('main').getByText(content).filter({ visible: true }).first()
		).toBeVisible();
		await noPageScrollOnPhone(page);
	}
	expect(errors).toEqual([]);
});

test('a chat through the proxy shows up in Requests with its timings', async ({
	page,
	request
}) => {
	const answer = await request.post(`${server.url}/v1/chat/completions`, {
		data: { model: 'gemma-3-12b', max_tokens: 4, messages: [{ role: 'user', content: 'hello' }] }
	});
	expect(answer.ok()).toBe(true);
	expect((await answer.json()).timings.predicted_n).toBe(4);

	await open(page, '/requests?model=gemma-3-12b', 'Requests');
	await expect(
		page.locator('main').getByRole('button', { name: /gemma-3-12b.*200/ })
	).toBeVisible();
});

test('the proxy keeps cross-site browsers away from admin routes', async ({ request }) => {
	const answer = await request.post(`${server.url}/models/load`, {
		headers: { 'Sec-Fetch-Site': 'cross-site', Origin: 'https://evil.example' },
		data: { model: 'gemma-3-12b' }
	});
	expect(answer.status()).toBe(403);
	const health = await request.get(`${server.url}/health`);
	expect(health.headers()['access-control-allow-origin']).toBeUndefined();
});

test('the palette goes to a page and the server menu opens', async ({ page }) => {
	const errors = watchErrors(page);
	await open(page, '/', 'Overview');

	await page.keyboard.press('Control+k');
	const palette = page.getByRole('dialog');
	await expect(palette).toBeVisible();
	await page.keyboard.type('bench');
	await page.keyboard.press('Enter');
	await expect(page.getByRole('heading', { level: 1, name: 'Bench' })).toBeVisible();
	await expect(palette).toBeHidden();

	await page.getByRole('button', { name: /open server controls/i }).click();
	await expect(page.getByRole('button', { name: /restart/i }).first()).toBeVisible();
	await page.keyboard.press('Escape');
	await expect(page.getByRole('button', { name: /restart/i })).toHaveCount(0);
	expect(errors).toEqual([]);
});

test('a slot can be saved from the page', async ({ page }) => {
	await open(page, '/slots', 'Slots');
	await page
		.locator('main')
		.getByRole('button', { name: /slot 0/i })
		.first()
		.click();
	await expect(page.locator('main').getByRole('button', { name: /save kv/i })).toBeVisible();
});

test('a profile edit marks the form dirty and Revert clears it', async ({ page }) => {
	await open(page, '/profiles', 'Profiles');
	await page
		.getByRole('button', { name: /gpt-oss-20b/ })
		.first()
		.click();
	await expect(page.getByText('No changes')).toBeVisible();
	await page.getByRole('button', { name: 'More slots' }).click();
	await expect(page.getByText(/unsaved change/)).toBeVisible();
	await page.getByRole('button', { name: 'Revert' }).click();
	await expect(page.getByText('No changes')).toBeVisible();
});

test('a benchmark runs to results and the models come back', async ({ page, request }) => {
	test.setTimeout(120_000);
	await open(page, '/bench', 'Bench');
	await page.getByRole('button', { name: /run llama-bench/i }).click();
	await expect(
		page
			.locator('main')
			.getByText(/pp512|tg128/)
			.first()
	).toBeVisible({
		timeout: 60_000
	});
	await expect(async () => {
		const status = await (await request.get(`${server.url}/api/v1/status`)).json();
		expect(status.models_loaded).toBeGreaterThan(0);
	}).toPass({ timeout: 60_000 });
});

test('the Library lists the local models', async ({ page }) => {
	await open(page, '/library', 'Library');
	await page.getByRole('tab', { name: /On this machine/ }).click();
	await expect(page.locator('main').getByText('Q4_K_M').first()).toBeVisible();
	await expect(
		page
			.locator('main')
			.getByText(/Qwen3-Coder/)
			.first()
	).toBeVisible();
});
