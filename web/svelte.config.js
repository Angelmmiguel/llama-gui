import adapter from '@sveltejs/adapter-static';

/** @type {import('@sveltejs/kit').Config} */
const config = {
	compilerOptions: {
		// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
		runes: ({ filename }) => (filename.split(/[/\\]/).includes('node_modules') ? undefined : true)
	},
	kit: {
		// A single index.html fallback, served by the Go binary for every app
		// route. Nothing is prerendered because the app is client-only.
		adapter: adapter({ fallback: 'index.html', precompress: false, strict: true }),

		// /slots, /metrics, /models and / belong to llama-server, which the Go
		// binary proxies, so the dashboard lives under its own prefix.
		paths: { base: '/ui' },

		// The policy is authored here and delivered by the Go server, which
		// reads the <meta http-equiv="content-security-policy"> tag out of the
		// built index.html and appends frame-ancestors (a directive meta tags
		// cannot carry). Hash mode covers SvelteKit's inline bootstrap script.
		csp: {
			mode: 'hash',
			directives: {
				'default-src': ['self'],
				'script-src': ['self'],
				'style-src': ['self', 'unsafe-inline'],
				'img-src': ['self', 'data:'],
				'font-src': ['self'],
				'connect-src': ['self'],
				'object-src': ['none'],
				'base-uri': ['none'],
				'form-action': ['self']
			}
		}
	}
};

export default config;
