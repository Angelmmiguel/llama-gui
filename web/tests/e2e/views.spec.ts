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

test('switching request filters keeps the page in place', async ({ page, request }) => {
	await request.post(`${server.url}/v1/chat/completions`, {
		data: { model: 'gemma-3-12b', max_tokens: 4, messages: [{ role: 'user', content: 'hello' }] }
	});
	await open(page, '/requests', 'Requests');
	const tile = page.locator('main').getByText('SUCCESS', { exact: true });
	await expect(tile).toBeVisible();
	const before = await tile.elementHandle();

	await page.route('**/api/v1/requests/summary*', async (route) => {
		await new Promise((done) => setTimeout(done, 400));
		await route.continue();
	});
	await page.getByRole('button', { name: 'chat', exact: true }).click();
	await expect(page).toHaveURL(/endpoint=chat/);
	expect(await before?.evaluate((el) => el.isConnected)).toBe(true);

	await expect(page.locator('main .results')).toHaveAttribute('aria-busy', 'false');
	expect(await before?.evaluate((el) => el.isConnected)).toBe(true);
});

test('a Library search loads more results', async ({ page }) => {
	await page.route('**/api/v1/library/repo*', (route) =>
		route.fulfill({ status: 404, json: { error: 'not found' } })
	);
	await page.route('**/api/v1/library/search*', async (route) => {
		const url = new URL(route.request().url());
		if (!url.searchParams.get('q')) return route.continue();
		const second = url.searchParams.get('cursor') === 'page2';
		await route.fulfill({
			json: {
				source: 'search',
				results: [
					{
						repo: second ? 'u/second' : 'u/first',
						name: second ? 'second' : 'first',
						tags: [],
						downloads: 1
					}
				],
				next_cursor: second ? '' : 'page2',
				hardware: {}
			}
		});
	});
	await open(page, '/library', 'Library');
	await page.getByPlaceholder('Search models or paste user/repo').fill('anything');
	await expect(page.locator('main').getByText('u/first').first()).toBeVisible();
	await page.getByRole('button', { name: 'Load more' }).click();
	await expect(page.locator('main').getByText('u/second').first()).toBeVisible();
	await expect(page.locator('main').getByText('u/first').first()).toBeVisible();
	await expect(page.getByRole('button', { name: 'Load more' })).toHaveCount(0);
});

test('the Overview chart switches range without losing the page', async ({ page, request }) => {
	// Without a model in memory a fresh Overview has no charts to show.
	await request.post(`${server.url}/api/v1/models/qwen3-coder/load`);
	await expect(async () => {
		const status = await (await request.get(`${server.url}/api/v1/status`)).json();
		expect(status.models_loaded).toBeGreaterThan(0);
	}).toPass({ timeout: 30_000 });
	await open(page, '/', 'Overview');
	const heading = page.getByRole('heading', { name: /DECODE THROUGHPUT/ });
	const panel = page.getByRole('region', { name: /DECODE THROUGHPUT/ });
	await expect(heading).toBeVisible();
	await expect(panel.getByText('last 60 s')).toBeVisible();

	const history = page.waitForResponse('**/api/v1/telemetry/history?range=1h');
	await page.getByRole('button', { name: '1h', exact: true }).click();
	expect((await history).ok()).toBe(true);
	await expect(panel.getByText('last hour')).toBeVisible();
	await expect(
		page.getByRole('img', { name: /Decode tokens per second, last hour/ })
	).toBeVisible();

	await page.getByRole('button', { name: '1m', exact: true }).click();
	await expect(panel.getByText('last 60 s')).toBeVisible();
});

test('models load, swap and unload from the dock at the top of the Overview', async ({ page }) => {
	const demo = await startDemo();
	try {
		await page.goto(`${demo.url}/ui/`);
		const dock = page.getByRole('region', { name: /MODELS IN MEMORY/ });
		await expect(dock.getByRole('heading', { name: 'MODELS IN MEMORY · 3' })).toBeVisible();

		const drawerButton = dock.getByRole('button', { name: 'Load model' });
		await expect(drawerButton).toHaveAttribute('aria-expanded', 'false');
		await drawerButton.click();
		await expect(dock.getByRole('heading', { name: 'ON YOUR DISK · NOT LOADED' })).toBeVisible();
		await dock
			.getByRole('button', { name: /^(Load|Swap & load)$/ })
			.first()
			.click();
		await expect(dock.getByRole('heading', { name: 'ON YOUR DISK · NOT LOADED' })).toBeHidden();
		await expect(dock.getByRole('heading', { name: /MODELS IN MEMORY · [34]/ })).toBeVisible();

		const unload = dock.getByRole('button', { name: /^Unload / }).first();
		const name = (await unload.getAttribute('aria-label')) ?? '';
		await unload.click();
		await expect(dock.getByRole('button', { name, exact: true })).toBeHidden({ timeout: 20_000 });
	} finally {
		await demo.stop();
	}
});

test('with no model in memory a fresh Overview shows only what to do next', async ({ page }) => {
	// The demo's traffic loads models on demand, so a settled state has to come from a snapshot.
	const snapshot = await (await page.request.get(`${server.url}/api/v1/telemetry`)).json();
	for (const m of snapshot.models) {
		m.status = 'unloaded';
		m.slots = [];
		m.memory = { total: 0, devices: [] };
	}
	await page.route('**/api/v1/telemetry/stream', (route) =>
		route.fulfill({
			contentType: 'text/event-stream',
			body: `data: ${JSON.stringify(snapshot)}\n\n`
		})
	);
	await page.goto(`${server.url}/ui/`);
	await expect(page.getByRole('heading', { name: 'No model is loaded' })).toBeVisible();
	await expect(page.getByRole('heading', { name: 'READY ON YOUR DISK' })).toBeVisible();
	await expect(page.getByRole('heading', { name: /DECODE THROUGHPUT/ })).toBeHidden();
	await expect(page.getByRole('heading', { name: /MODELS IN MEMORY/ })).toBeHidden();
	await expect(page.getByRole('group', { name: 'Chart range' })).toBeHidden();
});

test('a Library row tags the quants that have MTP heads', async ({ page }) => {
	const variant = (id: string) => ({
		id,
		quant: id,
		files: [{ name: `${id}.gguf`, size: 1 }],
		size_bytes: 1 << 30,
		shards: 1,
		fit: { total_bytes: 1 << 30, verdict: 'fits_gpu', weights_bytes: 1 << 30 },
		download_files: [`${id}.gguf`],
		download_bytes: 1 << 30
	});
	await page.route('**/api/v1/library/search*', async (route) => {
		if (!new URL(route.request().url()).searchParams.get('q')) return route.continue();
		await route.fulfill({
			json: {
				source: 'repo',
				results: [
					{
						repo: 'u/m',
						name: 'm',
						tags: [],
						downloads: 1,
						variants: [variant('Q4_K_M'), variant('Q8_0'), variant('side')]
					}
				],
				hardware: { vram_bytes: 8 << 30, ram_bytes: 8 << 30, headroom_bytes: 0 }
			}
		});
	});
	await page.route('**/api/v1/library/repo/mtp*', (route) =>
		route.fulfill({ json: { variants: { Q4_K_M: 'none', Q8_0: 'heads', side: 'companion' } } })
	);
	await open(page, '/library', 'Library');
	await page.getByPlaceholder('Search models or paste user/repo').fill('u/m');
	const main = page.locator('main');
	await expect(main.locator('.quant', { hasText: 'Q8_0' }).getByText('MTP')).toBeVisible();
	await expect(main.locator('.quant', { hasText: 'Q4_K_M' }).getByText('MTP')).toHaveCount(0);
	await expect(
		main.locator('.quant', { hasText: 'side' }).getByText('MTP companion')
	).toBeVisible();
	await expect(main.getByText(/--spec-type draft-mtp/)).toBeVisible();
});

test('Library suggestions show a skeleton only when slow, and a download shows its progress', async ({
	page
}) => {
	const G = 1024 ** 3;
	const suggestion = {
		category: 'largest_dense',
		repo: 'u/fast',
		name: 'fast-model',
		quant: 'Q8_0',
		fit: { total_bytes: G, verdict: 'fits_gpu', weights_bytes: G, kv_bytes: 0, compute_bytes: 0 },
		tags: [],
		note: 'A note.',
		metadata: { architecture: 'qwen', param_count: 1000 },
		download_files: ['fast-Q8_0.gguf'],
		download_bytes: G
	};
	let release: () => void = () => {};
	const held = new Promise<void>((done) => (release = done));
	await page.route('**/api/v1/library/suggestions*', async (route) => {
		await held;
		await route.fulfill({ json: { suggestions: [suggestion], hardware: {} } });
	});
	const running = {
		id: 'job1',
		repo: 'u/fast',
		variant: 'Q8_0',
		files: ['fast-Q8_0.gguf'],
		state: 'downloading',
		done: G / 2,
		total: G,
		bytes_per_sec: 80 * 1024 * 1024,
		eta_seconds: 7
	};
	let jobs: unknown[] = [];
	await page.route('**/api/v1/library/downloads*', async (route) => {
		if (route.request().method() === 'POST') {
			jobs = [running];
			return route.fulfill({ status: 202, json: running });
		}
		await route.fulfill({ json: { jobs } });
	});

	await open(page, '/library', 'Library');
	const suggestions = page.getByRole('region', { name: /SUGGESTED FOR THIS MACHINE/ });
	await expect(suggestions.getByLabel('Finding suggestions')).toBeVisible();
	await expect(suggestions.getByText(/Checking the starter catalog/)).toBeVisible();

	release();
	await expect(suggestions.getByRole('heading', { name: 'fast-model' })).toBeVisible();
	await expect(suggestions.getByLabel('Finding suggestions')).toHaveCount(0);
	await expect(suggestions.getByText('Updated just now')).toBeVisible();

	await suggestions.getByRole('button', { name: /^Download Q8_0/ }).click();
	await expect(suggestions.getByRole('progressbar', { name: /fast-model: 50%/ })).toBeVisible();
	await expect(suggestions.getByText(/50% · 7 s left/)).toBeVisible();
	await expect(suggestions.getByRole('button', { name: 'Cancel' })).toBeVisible();
});
