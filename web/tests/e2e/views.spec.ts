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
		['/playground', 'Playground', 'REQUEST SETTINGS'],
		['/library', 'Library', /My machine/],
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
	await page.getByRole('tab', { name: /Explore Hugging Face/ }).click();
	await page.getByPlaceholder(/Search Hugging Face/).fill('anything');
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
	await expect(page.getByRole('heading', { name: 'Load a model first' })).toBeVisible();
	await expect(page.getByRole('heading', { name: /DECODE THROUGHPUT/ })).toBeHidden();
	await expect(page.locator('main .bands')).toHaveAttribute('inert');
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
	await page.getByRole('tab', { name: /Explore Hugging Face/ }).click();
	await page.getByPlaceholder(/Search Hugging Face/).fill('u/m');
	const main = page.locator('main');
	await expect(main.locator('.quant')).toHaveCount(0);
	await main.getByRole('button', { name: /u\/m/ }).click();
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
	await page.getByRole('tab', { name: /Explore Hugging Face/ }).click();
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

test('the Playground chats with a loaded model and shows the timings', async ({
	page,
	request
}) => {
	await request.post(`${server.url}/api/v1/models/qwen3-coder/load`);
	await expect(async () => {
		const models = (await (await request.get(`${server.url}/api/v1/models`)).json()).models;
		expect(models.find((m: { id: string }) => m.id === 'qwen3-coder').status).toBe('loaded');
	}).toPass({ timeout: 30_000 });

	await open(page, '/playground', 'Playground');
	await page.getByRole('radio', { name: /qwen3-coder/ }).click();
	await page.getByLabel('Message', { exact: true }).fill('Say hi');
	await page.getByLabel('Message', { exact: true }).press('Enter');

	const chat = page.getByRole('region', { name: 'Chat' });
	await expect(chat.getByText('Say hi')).toBeVisible();
	await expect(chat.getByRole('button', { name: 'Regenerate' })).toBeVisible({ timeout: 30_000 });
	await expect(chat.getByText(/TTFT .* tokens/)).toBeVisible();

	const inspector = page.getByRole('complementary', { name: 'LAST RESPONSE' });
	await expect(inspector.getByText('predicted_n')).toBeVisible();
	await inspector.getByRole('button', { name: 'Request' }).click();
	await expect(inspector.getByText(/"cache_prompt": true/)).toBeVisible();
	await expect(inspector.getByRole('button', { name: 'Copy as cURL' })).toBeVisible();
	await inspector.getByRole('button', { name: 'Raw' }).click();
	await expect(inspector.getByText(/"choices"/)).toBeVisible();

	await chat.getByRole('button', { name: 'New chat' }).click();
	await expect(chat.getByText(/Ask qwen3-coder anything/)).toBeVisible();
});

test('the Playground offers to load a model when none is in memory', async ({ page, request }) => {
	const snapshot = await (await request.get(`${server.url}/api/v1/telemetry`)).json();
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
	await page.goto(`${server.url}/ui/playground`);
	const chat = page.getByRole('region', { name: 'Chat' });
	await expect(chat.getByRole('heading', { name: 'Load a model to start chatting' })).toBeVisible();
	await expect(chat.getByRole('button', { name: 'Load' }).first()).toBeVisible();
	await expect(chat.getByRole('link', { name: /Find a model in the Library/ })).toBeVisible();
});

test('a Library search shows it is working from the first keystroke', async ({ page }) => {
	let release: () => void = () => {};
	const held = new Promise<void>((done) => (release = done));
	await page.route('**/api/v1/library/repo*', (route) =>
		route.fulfill({ status: 404, json: { error: 'not found' } })
	);
	await page.route('**/api/v1/library/search*', async (route) => {
		if (!new URL(route.request().url()).searchParams.get('q')) {
			return route.fulfill({
				json: {
					source: 'catalog',
					results: [{ repo: 'u/before', name: 'before-model', tags: [], downloads: 1 }],
					hardware: {}
				}
			});
		}
		await held;
		await route.fulfill({
			json: {
				source: 'search',
				results: [{ repo: 'u/after', name: 'after-model', tags: [], downloads: 1 }],
				hardware: {}
			}
		});
	});
	await open(page, '/library', 'Library');
	await page.getByRole('tab', { name: /Explore Hugging Face/ }).click();
	const main = page.locator('main');
	await expect(main.getByText('before-model').first()).toBeVisible();

	await page.getByPlaceholder(/Search Hugging Face/).fill('anything');
	const skeleton = main.getByLabel('Searching Hugging Face');
	await expect(skeleton).toBeVisible();
	await expect(main.getByText('before-model')).toHaveCount(0);
	await expect(main.getByRole('heading', { name: /SEARCHING FOR “ANYTHING”/ })).toBeVisible();

	release();
	await expect(main.getByText('after-model').first()).toBeVisible();
	await expect(skeleton).toHaveCount(0);
	await expect(main.getByRole('heading', { name: /SEARCHING FOR/ })).toHaveCount(0);
});

test('the Playground offers reasoning effort only when the template takes it, and folds the thinking', async ({
	page,
	request
}) => {
	await request.post(`${server.url}/api/v1/models/qwen3-coder/load`);
	await expect(async () => {
		const models = (await (await request.get(`${server.url}/api/v1/models`)).json()).models;
		expect(models.find((m: { id: string }) => m.id === 'qwen3-coder').status).toBe('loaded');
	}).toPass({ timeout: 30_000 });

	let supported = false;
	await page.route('**/props?*', (route) =>
		route.fulfill({
			json: {
				chat_template_caps: {
					supports_reasoning_effort: supported,
					reasoning_effort_levels: supported ? ['low', 'high'] : []
				}
			}
		})
	);
	let sent: Record<string, unknown> = {};
	await page.route('**/v1/chat/completions', async (route) => {
		sent = route.request().postDataJSON();
		const chunk = (delta: object, extra = {}) =>
			`data: ${JSON.stringify({ choices: [{ delta, finish_reason: null, ...extra }] })}\n\n`;
		await route.fulfill({
			contentType: 'text/event-stream',
			body:
				chunk({ reasoning_content: 'Let me think.' }) +
				chunk({ content: 'The answer.' }) +
				chunk({}, { finish_reason: 'stop' }) +
				'data: [DONE]\n\n'
		});
	});

	await open(page, '/playground', 'Playground');
	const settings = page.getByRole('complementary', { name: 'REQUEST SETTINGS' });
	await page.getByRole('radio', { name: /qwen3-coder/ }).click();
	await expect(settings.getByText('Reasoning effort')).toHaveCount(0);
	await expect(settings.getByText('Show reasoning')).toHaveCount(0);

	supported = true;
	await page.reload();
	await page.getByRole('radio', { name: /qwen3-coder/ }).click();
	await settings.getByLabel(/Reasoning effort/).selectOption('high');

	const message = page.getByLabel('Message', { exact: true });
	await message.fill('Why?');
	await message.press('Enter');
	const chat = page.getByRole('region', { name: 'Chat' });
	await expect(chat.getByText('The answer.')).toBeVisible();
	expect(sent.reasoning_effort).toBe('high');

	const thinking = chat.getByText('Let me think.');
	await expect(thinking).toBeHidden();
	await chat.getByText(/Thought for/).click();
	await expect(thinking).toBeVisible();
});

test('Bench compares two profiles', async ({ page }) => {
	test.setTimeout(120_000);
	const demo = await startDemo();
	try {
		await page.goto(`${demo.url}/ui/bench`);
		const panel = page.getByRole('region', { name: 'COMPARE TWO PROFILES' });
		await panel.getByLabel('First').selectOption('gemma-3-12b');
		await panel.getByLabel('Then').selectOption('nomic-embed');
		await panel.getByLabel('Max tokens').fill('8');
		await panel.getByRole('button', { name: 'Compare' }).click();
		await expect(panel.getByRole('cell', { name: 'gemma-3-12b' })).toBeVisible({ timeout: 90_000 });
		await expect(panel.getByRole('cell', { name: 'nomic-embed' })).toBeVisible();
		await expect(panel.getByText(/on this prompt|same speed/)).toBeVisible();
		await expect(panel.getByRole('button', { name: 'Compare' })).toBeEnabled({ timeout: 30_000 });
	} finally {
		await demo.stop();
	}
});

test('saving a loaded profile reloads it, even if the router already stopped it', async ({
	page
}) => {
	test.setTimeout(120_000);
	const demo = await startDemo();
	try {
		// What a real router does: the save re-reads the preset, which unloads the changed model.
		let saved = false;
		await page.route('**/api/v1/profiles/*', async (route) => {
			if (route.request().method() === 'PUT') saved = true;
			await route.fallback();
		});
		await page.route('**/api/v1/models', async (route) => {
			const res = await route.fetch();
			const body = await res.json();
			if (saved) {
				for (const m of body.models)
					if (m.id === 'qwen3-coder' && !loadedAgain) m.status = 'unloaded';
			}
			await route.fulfill({ response: res, json: body });
		});
		let loadedAgain = false;
		let unloads = 0;
		await page.route('**/api/v1/models/qwen3-coder/load', async (route) => {
			loadedAgain = true;
			await route.fulfill({ status: 202, json: { id: 'qwen3-coder' } });
		});
		await page.route('**/api/v1/models/qwen3-coder/unload', async (route) => {
			unloads++;
			await route.fulfill({
				status: 400,
				json: { error: 'llama-server: status 400: model is not running' }
			});
		});
		await page.goto(`${demo.url}/ui/profiles`);
		await page
			.getByRole('button', { name: /qwen3-coder/ })
			.first()
			.click();
		await page.getByLabel(/^Micro-batch/).fill('128');
		await page.getByRole('button', { name: 'Save & reload model' }).click();
		await expect(page.getByText(/Saved and reloaded/)).toBeVisible({ timeout: 30_000 });
		await expect(page.getByText(/model is not running/)).toHaveCount(0);
		expect(unloads).toBe(0);
	} finally {
		await demo.stop();
	}
});

test('Builds shows it is reading releases, and takes a tag under other', async ({ page }) => {
	const release = (tag: string, channel: string) => ({
		tag,
		channel,
		binary_tag: tag,
		published_at: '2026-11-01T00:00:00Z',
		options: []
	});
	let done: () => void = () => {};
	const held = new Promise<void>((resolve) => (done = resolve));
	const asked: string[] = [];
	await page.route('**/api/v1/builds/available*', async (route) => {
		const url = new URL(route.request().url());
		asked.push(url.search);
		const version = url.searchParams.get('version');
		if (version) {
			return route.fulfill({ json: { releases: [release(version, 'nightly')] } });
		}
		await held;
		const channel = url.searchParams.get('channel') ?? 'stable';
		await route.fulfill({
			json: { releases: [release(channel === 'stable' ? 'v0.9.0' : 'b12144', channel)] }
		});
	});

	await page.goto(`${server.url}/ui/builds`);
	await expect(page.getByText('Reading releases from GitHub…')).toBeVisible();
	done();
	await expect(page.getByText('Reading releases from GitHub…')).toHaveCount(0);
	expect(asked.some((q) => q.includes('channel=nightly') && q.includes('limit=25'))).toBe(true);

	const source = page.getByRole('group', { name: 'Channel' });
	await expect(page.getByLabel('Tag')).toHaveCount(0);
	await expect(page.getByLabel('Release')).toBeVisible();

	await source.getByRole('button', { name: 'other' }).click();
	await expect(page.getByLabel('Release')).toHaveCount(0);
	await page.getByLabel('Tag').fill('b8105');
	await page.getByRole('button', { name: 'Use', exact: true }).click();
	await expect(page.locator('main').getByText('b8105').first()).toBeVisible();
	await expect(source.getByRole('button', { name: 'other' })).toHaveAttribute(
		'aria-pressed',
		'true'
	);
	await expect(page.getByLabel('Tag')).toHaveValue('b8105');

	await source.getByRole('button', { name: 'stable' }).click();
	await expect(page.getByLabel('Tag')).toHaveCount(0);
	await expect(page.getByLabel('Release')).toBeVisible();
});

test('a rate-limited first install says why and when it tries again', async ({ page }) => {
	await page.route('**/api/v1/status', async (route) => {
		const res = await route.fetch();
		const body = await res.json();
		delete body.active_build;
		body.state = 'stopped';
		body.install_issue = {
			error: 'github rate limit exceeded',
			rate_limited: true,
			retry_at: '2026-11-06T09:49:53Z'
		};
		await route.fulfill({ response: res, json: body });
	});
	await page.goto(`${server.url}/ui/`);
	await expect(page.getByText('Waiting for GitHub')).toBeVisible();
	await expect(page.locator('main').getByText('LLAMA_GUI_GITHUB_TOKEN')).toBeVisible();
	await page.goto(`${server.url}/ui/logs`);
	await expect(page.getByText(/GitHub's rate limit stopped the install/)).toBeVisible();
});

test('Explore filters by fit, defaulting to what fits, with rows collapsed', async ({ page }) => {
	const G = 1024 ** 3;
	const row = (name: string, verdict: string) => ({
		repo: `u/${name}`,
		name,
		tags: [],
		downloads: 1,
		best: 'Q4',
		variants: [
			{
				id: 'Q4',
				quant: 'Q4',
				files: [{ name: `${name}.gguf`, size: G }],
				size_bytes: G,
				shards: 1,
				fit: { total_bytes: G, verdict, weights_bytes: G, kv_bytes: 0, compute_bytes: 0 },
				download_files: [`${name}.gguf`],
				download_bytes: G
			}
		]
	});
	await page.route('**/api/v1/library/search*', (route) =>
		route.fulfill({
			json: {
				source: 'catalog',
				results: [
					row('fits', 'fits_gpu'),
					row('partly', 'partial_offload'),
					row('huge', 'too_large')
				],
				hardware: { vram_bytes: 12 * G, ram_bytes: 32 * G, headroom_bytes: 0 }
			}
		})
	);
	await page.goto(`${server.url}/ui/library`);
	await expect(page.getByRole('tab', { name: /My machine/ })).toHaveAttribute(
		'aria-selected',
		'true'
	);
	await page.getByRole('tab', { name: /Explore Hugging Face/ }).click();

	const main = page.locator('main');
	const fit = page.getByRole('group', { name: 'Filter by fit' });
	await expect(fit.getByRole('button', { name: /Fits on GPU/ })).toHaveAttribute(
		'aria-pressed',
		'true'
	);
	await expect(main.getByRole('button', { name: /u\/fits/ })).toBeVisible();
	await expect(main.getByRole('button', { name: /u\/partly/ })).toHaveCount(0);
	await expect(main.locator('.quant')).toHaveCount(0);

	await fit.getByRole('button', { name: /Partial offload/ }).click();
	await expect(main.getByRole('button', { name: /u\/partly/ })).toBeVisible();
	await expect(main.getByRole('button', { name: /u\/fits/ })).toHaveCount(0);

	await fit.getByRole('button', { name: /^Any/ }).click();
	await expect(main.getByRole('button', { name: /u\/huge/ })).toBeVisible();

	await main.getByRole('button', { name: /u\/huge/ }).click();
	await expect(main.locator('.quant')).toHaveCount(1);
});
