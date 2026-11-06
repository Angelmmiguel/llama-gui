import { describe, expect, it } from 'vitest';
import type { ServerStatus } from '$lib/api/client';
import { serverBanner } from './banner';

const status = (over: Partial<ServerStatus>): ServerStatus => ({
	state: 'running',
	version: 'v',
	uptime_seconds: 1,
	capture: 'meta',
	...over
});

describe('serverBanner', () => {
	it('says nothing while all is well, or before the first answer', () => {
		expect(serverBanner(status({}), false)).toBeNull();
		expect(serverBanner(null, false)).toBeNull();
	});

	it('leaves a stopped router to the pages, even with a reason', () => {
		expect(serverBanner(status({ state: 'stopped', last_error: 'no build' }), false)).toBeNull();
	});

	it('ignores a past crash once the router runs again', () => {
		expect(
			serverBanner(status({ state: 'running', last_error: 'boom', restart_count: 2 }), false)
		).toBeNull();
	});

	it('wins when unreachable, even with an old status', () => {
		expect(serverBanner(status({ state: 'crashed' }), true)?.title).toBe('Cannot reach llama-gui');
		expect(serverBanner(null, true)?.title).toBe('Cannot reach llama-gui');
	});

	it('shows the reason and the restart count of a crash', () => {
		const b = serverBanner(
			status({ state: 'crashed', last_error: 'exit status 1', restart_count: 3 }),
			false
		);
		expect(b).toEqual({
			title: 'The router crashed',
			detail: 'exit status 1',
			note: '3 restarts so far'
		});
	});

	it('leaves the count out before any restart, and is singular for one', () => {
		expect(serverBanner(status({ state: 'crashed' }), false)?.note).toBeUndefined();
		expect(serverBanner(status({ state: 'crashed', restart_count: 1 }), false)?.note).toBe(
			'1 restart so far'
		);
	});

	it('stays up while a crashed router retries, but not on a plain start', () => {
		expect(
			serverBanner(status({ state: 'starting', last_error: 'x', restart_count: 1 }), false)?.title
		).toBe('The router crashed and is starting again');
		expect(serverBanner(status({ state: 'starting' }), false)).toBeNull();
	});
});
