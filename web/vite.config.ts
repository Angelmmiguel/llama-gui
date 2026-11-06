import { defineConfig } from 'vitest/config';
import { sveltekit } from '@sveltejs/kit/vite';

// The Go server listens on LLAMA_GUI_ADDR (its own default is 127.0.0.1:8080),
// and Vite proxies to the same address, so changing the port is one variable
// in .env.local for both halves. A bare ":port" means loopback.
const addr = process.env.LLAMA_GUI_ADDR ?? '127.0.0.1:8080';
const target = `http://${addr.startsWith(':') ? `127.0.0.1${addr}` : addr}`;

// SvelteKit options live in svelte.config.js; passing any of them here would
// make SvelteKit ignore that file.
export default defineConfig({
	plugins: [sveltekit()],
	// Small font subsets would otherwise become data: URLs, which font-src 'self' refuses.
	build: { assetsInlineLimit: 0 },
	server: {
		// The browser only ever talks to Vite, one origin as in production.
		proxy: { '/api': { target, ws: true }, '/v1': { target } }
	},
	test: {
		environment: 'node',
		expect: { requireAssertions: true },
		include: ['src/**/*.{test,spec}.{js,ts}']
	}
});
