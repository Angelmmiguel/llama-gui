import { describe, expect, it, vi } from 'vitest';
import type { ServerStatus, StreamOptions, TelemetrySnapshot } from '$lib/api/client';
import { ServerState } from './server.svelte';
import { Telemetry } from './telemetry.svelte';

const STATUS: ServerStatus = {
	state: 'running',
	version: 'dev',
	uptime_seconds: 10,
	capture: 'meta'
};

// The state only stores snapshots, so a marker stands in for the whole shape.
const snap = (n: number) => ({ n }) as unknown as TelemetrySnapshot;

describe('ServerState', () => {
	it('starts as connecting', () => {
		expect(new ServerState(async () => STATUS).state).toBe('connecting');
	});

	it('takes state and uptime from a good poll', async () => {
		const server = new ServerState(async () => STATUS);
		await server.poll();
		expect(server.state).toBe('running');
		expect(server.uptimeSeconds).toBe(10);
	});

	it('fails soft, then recovers', async () => {
		const fetchStatus = vi.fn(async () => STATUS);
		const server = new ServerState(fetchStatus);
		await server.poll();

		fetchStatus.mockRejectedValueOnce(new Error('down'));
		await server.poll();
		expect(server.state).toBe('unreachable');
		expect(server.uptimeSeconds).toBeNull();
		expect(server.status).toEqual(STATUS);

		await server.poll();
		expect(server.state).toBe('running');
	});

	it('polls and ticks until stopped', async () => {
		vi.useFakeTimers();
		try {
			const fetchStatus = vi.fn(async () => STATUS);
			const server = new ServerState(fetchStatus);
			const stop = server.start();
			await vi.advanceTimersByTimeAsync(3000);
			expect(fetchStatus).toHaveBeenCalledTimes(1);
			expect(server.uptimeSeconds).toBe(13);
			await vi.advanceTimersByTimeAsync(2000);
			expect(fetchStatus).toHaveBeenCalledTimes(2);
			expect(server.uptimeSeconds).toBe(10);
			stop();
			await vi.advanceTimersByTimeAsync(20_000);
			expect(fetchStatus).toHaveBeenCalledTimes(2);
		} finally {
			vi.useRealTimers();
		}
	});
});

describe('Telemetry', () => {
	function setup() {
		let emit: (s: TelemetrySnapshot) => void = () => {};
		let options: StreamOptions = {};
		const close = vi.fn();
		const connect = vi.fn(
			(_: string, onMessage: (s: TelemetrySnapshot) => void, opts: StreamOptions) => {
				emit = onMessage;
				options = opts;
				return close;
			}
		);
		return {
			telemetry: new Telemetry(connect),
			connect,
			close,
			emit: (s: TelemetrySnapshot) => emit(s),
			status: (c: boolean) => options.onStatus?.(c)
		};
	}

	it('holds the latest snapshot and tracks the connection', () => {
		const { telemetry, emit, status } = setup();
		telemetry.subscribe();
		expect(telemetry.connected).toBe(false);
		status(true);
		expect(telemetry.connected).toBe(true);
		emit(snap(1));
		emit(snap(2));
		expect(telemetry.latest).toEqual({ n: 2 });
		status(false);
		expect(telemetry.connected).toBe(false);
		expect(telemetry.latest).toEqual({ n: 2 });
	});

	it('freezes while paused', () => {
		const { telemetry, emit } = setup();
		telemetry.subscribe();
		emit(snap(1));
		telemetry.live = false;
		emit(snap(2));
		expect(telemetry.latest).toEqual({ n: 1 });
		telemetry.live = true;
		emit(snap(3));
		expect(telemetry.latest).toEqual({ n: 3 });
	});

	it('shares one connection, closed by the last subscriber', () => {
		const { telemetry, connect, close, status } = setup();
		const a = telemetry.subscribe();
		const b = telemetry.subscribe();
		status(true);
		expect(connect).toHaveBeenCalledTimes(1);
		a();
		a();
		expect(close).not.toHaveBeenCalled();
		b();
		expect(close).toHaveBeenCalledTimes(1);
		expect(telemetry.connected).toBe(false);
		telemetry.subscribe();
		expect(connect).toHaveBeenCalledTimes(2);
	});
});
