import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, getStatus, listAvailableBuilds, slotAction, stream } from './client';

describe('request', () => {
	afterEach(() => vi.unstubAllGlobals());

	function respond(status: number, body: string) {
		const fetchMock = vi.fn<typeof fetch>(async () => new Response(body, { status }));
		vi.stubGlobal('fetch', fetchMock);
		return fetchMock;
	}

	it('parses JSON from the API prefix', async () => {
		const fetchMock = respond(200, '{"state":"running"}');
		await expect(getStatus()).resolves.toEqual({ state: 'running' });
		expect(fetchMock.mock.calls[0]).toMatchObject(['/api/v1/status', { method: 'GET' }]);
	});

	it('raises the error field with its status', async () => {
		respond(409, '{"error":"busy"}');
		const err = await getStatus().catch((e: unknown) => e);
		expect(err).toBeInstanceOf(ApiError);
		expect(err).toMatchObject({ status: 409, message: 'busy' });
	});

	it('falls back when an error body is not JSON', async () => {
		respond(502, '<html>bad gateway</html>');
		await expect(getStatus()).rejects.toMatchObject({ status: 502, message: 'HTTP 502' });
	});

	it('escapes ids and drops empty query values', async () => {
		const fetchMock = respond(200, '');
		await slotAction('org/model', 2, 'erase');
		expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/slots/org%2Fmodel/2/erase');
		await listAvailableBuilds();
		expect(fetchMock.mock.calls[1][0]).toBe('/api/v1/builds/available');
		await listAvailableBuilds('stable');
		expect(fetchMock.mock.calls[2][0]).toBe('/api/v1/builds/available?channel=stable');
	});
});

class FakeEventSource {
	static readonly CLOSED = 2;
	static instances: FakeEventSource[] = [];
	readyState = 0;
	closed = false;
	onopen: (() => void) | null = null;
	onmessage: ((e: { data: string }) => void) | null = null;
	onerror: (() => void) | null = null;

	listeners: Record<string, (e: { data: string }) => void> = {};

	constructor(readonly url: string) {
		FakeEventSource.instances.push(this);
	}

	addEventListener(name: string, listener: (e: { data: string }) => void) {
		this.listeners[name] = listener;
	}

	close() {
		this.closed = true;
	}

	fail(readyState: number) {
		this.readyState = readyState;
		this.onerror?.();
	}
}

describe('stream', () => {
	beforeEach(() => {
		FakeEventSource.instances = [];
		vi.useFakeTimers();
		vi.stubGlobal('EventSource', FakeEventSource);
	});

	afterEach(() => {
		vi.useRealTimers();
		vi.unstubAllGlobals();
	});

	it('delivers JSON and skips anything else', () => {
		const got: unknown[] = [];
		stream('/telemetry/stream', (m) => got.push(m));
		const [source] = FakeEventSource.instances;
		expect(source.url).toBe('/api/v1/telemetry/stream');
		source.onmessage?.({ data: '{"a":1}' });
		source.onmessage?.({ data: 'not json' });
		expect(got).toEqual([{ a: 1 }]);
	});

	it('follows a named event with a query', () => {
		const got: unknown[] = [];
		stream('/logs/stream', (m) => got.push(m), { event: 'log', query: { backlog: 9 } });
		const [source] = FakeEventSource.instances;
		expect(source.url).toBe('/api/v1/logs/stream?backlog=9');
		source.listeners.log({ data: '{"a":1}' });
		expect(got).toEqual([{ a: 1 }]);
	});

	it('leaves transient drops to EventSource', () => {
		const status: boolean[] = [];
		stream('/x', () => {}, { onStatus: (c) => status.push(c) });
		FakeEventSource.instances[0].fail(0);
		vi.advanceTimersByTime(60_000);
		expect(FakeEventSource.instances).toHaveLength(1);
		expect(status).toEqual([false]);
	});

	it('retries refused connections with a doubling delay, reset by success', () => {
		stream('/x', () => {}, { retryMs: 100, maxRetryMs: 300 });
		const all = FakeEventSource.instances;
		all[0].fail(FakeEventSource.CLOSED);
		vi.advanceTimersByTime(99);
		expect(all).toHaveLength(1);
		vi.advanceTimersByTime(1);
		expect(all).toHaveLength(2);
		all[1].fail(FakeEventSource.CLOSED);
		vi.advanceTimersByTime(200);
		expect(all).toHaveLength(3);
		all[2].fail(FakeEventSource.CLOSED);
		vi.advanceTimersByTime(300);
		expect(all).toHaveLength(4);
		all[3].onopen?.();
		all[3].fail(FakeEventSource.CLOSED);
		vi.advanceTimersByTime(100);
		expect(all).toHaveLength(5);
	});

	it('stops for good when closed', () => {
		const close = stream('/x', () => {}, { retryMs: 100 });
		const all = FakeEventSource.instances;
		all[0].fail(FakeEventSource.CLOSED);
		close();
		vi.advanceTimersByTime(10_000);
		expect(all).toHaveLength(1);
		expect(all[0].closed).toBe(true);
	});
});
