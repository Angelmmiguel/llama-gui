import { expect, test, type Page } from '@playwright/test';
import { startServer, type TestServer } from '../server';

const PAGES = [
	{ link: 'Overview', path: '/ui/' },
	{ link: 'Slots', path: '/ui/slots' },
	{ link: 'Requests', path: '/ui/requests' },
	{ link: 'Logs', path: '/ui/logs' },
	{ link: 'Library', path: '/ui/library' },
	{ link: 'Profiles', path: '/ui/profiles' },
	{ link: 'Builds', path: '/ui/builds' },
	{ link: 'Bench', path: '/ui/bench' }
];

let server: TestServer;

test.beforeAll(async () => {
	test.setTimeout(180_000);
	server = await startServer();
});

test.afterAll(async () => {
	await server?.stop();
});

function watchForCspViolations(page: Page): string[] {
	const violations: string[] = [];
	page.on('console', (message) => {
		if (/content security policy|refused to/i.test(message.text())) {
			violations.push(message.text());
		}
	});
	return violations;
}

async function expectActive(page: Page, name: string) {
	const nav = page.getByRole('navigation', { name: 'Primary' });
	await expect(page.getByRole('heading', { level: 1, name })).toBeVisible();
	await expect(nav.getByRole('link', { name })).toHaveAttribute('aria-current', 'page');
	await expect(nav.locator('[aria-current="page"]')).toHaveCount(1);
}

test('every nav link loads and is marked active', async ({ page }) => {
	const violations = watchForCspViolations(page);
	await page.goto(`${server.url}/ui/`);
	for (const { link, path } of PAGES) {
		await page
			.getByRole('navigation', { name: 'Primary' })
			.getByRole('link', { name: link })
			.click();
		await expect(page).toHaveURL(new RegExp(`${path.replace(/\/$/, '')}/?$`));
		await expectActive(page, link);
	}
	expect(violations).toEqual([]);
});

test('every route survives a cold load, with self-hosted fonts', async ({ page }) => {
	const violations = watchForCspViolations(page);
	const external: string[] = [];
	page.on('request', (request) => {
		if (!request.url().startsWith(server.url)) external.push(request.url());
	});

	for (const { link, path } of PAGES) {
		await page.goto(server.url + path);
		await expectActive(page, link);
	}

	const fonts = await page.evaluate(async () => {
		await document.fonts.ready;
		return ['14px "Geist Sans"', '12px "Geist Mono"'].map((font) => document.fonts.check(font));
	});
	expect(fonts).toEqual([true, true]);
	expect(external).toEqual([]);
	expect(violations).toEqual([]);
});
