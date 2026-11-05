import { describe, expect, it } from 'vitest';
import type {
	InflightRequest,
	Profile,
	RequestRecord,
	Slot,
	TelemetryModel
} from '$lib/api/client';
import {
	allSlots,
	busiestProfile,
	cellSize,
	contextSummary,
	deviceIndex,
	gpuSummary,
	logMessage,
	memoryLane,
	modelMeta,
	niceMax,
	padSeries,
	prefillPercent,
	profileGroups,
	quantOf,
	residentsOn,
	slotActivity,
	slotFill,
	waterfallRows
} from './overview';

const GiB = 1024 ** 3;

const slot = (over: Partial<Slot> = {}): Slot => ({
	id: 0,
	state: 'idle',
	cached: 0,
	prompt: 0,
	generated: 0,
	total: 0,
	n_ctx: 16384,
	task: null,
	speed: 0,
	...over
});

const model = (over: Partial<TelemetryModel> = {}): TelemetryModel => ({
	id: 'm',
	status: 'loaded',
	memory: { total: 0, devices: [] },
	slots: [],
	...over
});

describe('slots', () => {
	it('sorts by slot count and keeps ids', () => {
		const out = allSlots([
			model({ id: 'a', slots: [slot()] }),
			model({ id: 'none' }),
			model({ id: 'b', slots: [slot(), slot({ id: 1 })] })
		]);
		expect(out.map((s) => `${s.model}/${s.id}`)).toEqual(['b/0', 'b/1', 'a/0']);
	});

	it('counts an idle slot as all cached', () => {
		expect(slotFill(slot({ total: 900 }))).toEqual({ cached: 900, prompt: 0, generated: 0 });
		expect(
			slotFill(slot({ state: 'decode', cached: 1, prompt: 2, generated: 3, total: 6 }))
		).toEqual({ cached: 1, prompt: 2, generated: 3 });
	});

	it('describes activity', () => {
		expect(slotActivity(slot({ state: 'decode', speed: 38.44, task: 7 }))).toBe(
			'38.4 tok/s · task 7'
		);
		expect(slotActivity(slot({ state: 'prefill', cached: 100, prompt: 400, total: 1000 }))).toBe(
			'50% of 1.0k prompt'
		);
		expect(slotActivity(slot({ total: 10 }))).toBe('prompt cached');
		expect(slotActivity(slot())).toBe('empty');
		expect(prefillPercent(slot({ state: 'prefill' }))).toBe(0);
	});

	it('summarises context', () => {
		expect(contextSummary([slot(), slot()])).toBe('16.4k tokens per slot');
		expect(contextSummary([slot({ n_ctx: 1000 }), slot({ n_ctx: 2000 })])).toBe(
			'1.0k to 2.0k tokens per slot'
		);
		expect(contextSummary([])).toBe('');
	});
});

describe('hardware labels', () => {
	it('maps devices to GPUs', () => {
		expect(deviceIndex({ kind: 'device', name: 'CUDA1' })).toBe(1);
		expect(deviceIndex({ kind: 'host', name: 'Host' })).toBeNull();
		expect(deviceIndex({ kind: 'device', name: 'odd' })).toBeNull();
	});

	it('summarises GPUs', () => {
		const rtx = { name: 'NVIDIA GeForce RTX 4090' };
		expect(gpuSummary([rtx, rtx])).toBe('2× RTX 4090');
		expect(gpuSummary([rtx, { name: 'AMD Radeon RX 7900' }])).toBe('RTX 4090 + RX 7900');
		expect(gpuSummary([])).toBe('CPU only');
	});
});

describe('memoryLane', () => {
	const cell = GiB / 2;

	it('picks a cell size that keeps a row readable', () => {
		expect(cellSize(24 * GiB)).toBe(GiB / 2);
		expect(cellSize(64 * GiB)).toBe(GiB);
		expect(cellSize(4 * GiB)).toBe(GiB / 4);
	});

	it('lays out models, compute and free, largest model first', () => {
		const lane = memoryLane(
			[
				{ id: 'small', device: { model: 0.1 * GiB, context: 0, compute: 0 }, busy: false },
				{ id: 'big', device: { model: 14 * GiB, context: 3 * GiB, compute: 1.5 * GiB }, busy: true }
			],
			24 * GiB,
			null,
			cell
		);
		expect(lane.segments.map((s) => [s.key, s.cells])).toEqual([
			['model:big', 34],
			['model:small', 1],
			['compute', 3],
			['free', 10]
		]);
		expect(lane.segments[0].runs).toEqual([
			{ kind: 'weights', cells: 28 },
			{ kind: 'kv', cells: 6, breathe: true }
		]);
		expect(lane.note).toBe('big 14.0 G weights + 3.0 G KV · small 0.1 G · compute 1.5 G');
	});

	it('shows what the system reports beyond the models', () => {
		const lane = memoryLane([], 24 * GiB, 5 * GiB, cell);
		expect(lane.segments.map((s) => [s.key, s.cells])).toEqual([
			['other', 10],
			['free', 38]
		]);
	});

	it('never goes negative when models report more than fits', () => {
		const lane = memoryLane(
			[{ id: 'x', device: { model: 30 * GiB, context: 0, compute: 0 }, busy: false }],
			24 * GiB,
			20 * GiB,
			cell
		);
		expect(lane.segments.map((s) => s.key)).toEqual(['model:x']);
	});

	it('finds residents per device', () => {
		const device = { kind: 'device', name: 'CUDA1', model: 1, context: 2, compute: 3 };
		const models = [model({ id: 'a', memory: { total: 6, devices: [device] } })];
		expect(residentsOn(models, 1).map((r) => r.id)).toEqual(['a']);
		expect(residentsOn(models, 0)).toEqual([]);
		expect(residentsOn(models, null)).toEqual([]);
	});
});

describe('waterfallRows', () => {
	const now = Date.parse('2026-01-01T00:01:00Z');
	const iso = (msAgo: number) => new Date(now - msAgo).toISOString();
	const done = (over: Partial<RequestRecord> = {}): RequestRecord => ({
		id: 1,
		started_at: iso(30_000),
		model: 'm',
		endpoint: '/v1/chat/completions',
		method: 'POST',
		status: 200,
		slot: 0,
		task_id: 1,
		ttft_ms: 0,
		total_ms: 15_000,
		queue_ms: 1000,
		prompt_n: 100,
		cache_n: 900,
		prompt_ms: 2000,
		predicted_n: 50,
		predicted_ms: 12_000,
		predicted_per_second: 0,
		stream: true,
		error: '',
		...over
	});
	const live = (over: Partial<InflightRequest> = {}): InflightRequest => ({
		id: 9,
		model: 'm',
		endpoint: '/infill',
		started_at: iso(6000),
		phase: 'waiting',
		...over
	});

	it('places a finished request in the window with real phases', () => {
		const [row] = waterfallRows([], [done()], [], now);
		expect(row).toMatchObject({
			key: 'done:1',
			id: '#1',
			left: 50,
			width: 25,
			queue: 1000,
			prefill: 2000,
			decode: 12_000,
			live: false,
			io: '1.0k (900 cached) → 50',
			status: '200',
			tone: 'ok'
		});
	});

	it('clips a request that began before the window and drops one that ended before it', () => {
		const rows = waterfallRows(
			[],
			[
				done({ id: 1, started_at: iso(70_000), total_ms: 20_000 }),
				done({ id: 2, started_at: iso(90_000), total_ms: 1000 })
			],
			[],
			now
		);
		expect(rows.map((r) => r.id)).toEqual(['#1']);
		expect(rows[0].left).toBe(0);
		expect(rows[0].width).toBeCloseTo((10 / 60) * 100);
	});

	it('flags errors', () => {
		const [row] = waterfallRows(
			[],
			[done({ status: 400, error: '400 ctx', prompt_ms: 0, predicted_ms: 0 })],
			[],
			now
		);
		expect(row).toMatchObject({ status: '400 ctx', tone: 'warn', queue: 0, decode: 1 });
	});

	it('shows in-flight first, in the phase the slot reports', () => {
		const m = model({
			slots: [slot({ id: 1, state: 'decode', prompt: 10, cached: 0, generated: 4 })]
		});
		const rows = waterfallRows(
			[live(), live({ id: 10, slot: 1, started_at: iso(1000), phase: 'responding' })],
			[done()],
			[m],
			now
		);
		expect(rows.map((r) => r.status)).toEqual(['stream', 'queued', '200']);
		expect(rows[0]).toMatchObject({
			decode: 1,
			queue: 0,
			live: true,
			io: '10 → 4',
			left: 100 - (1 / 60) * 100
		});
		expect(rows[1]).toMatchObject({ queue: 1, decode: 0, io: '… → …' });
	});

	it('caps the rows', () => {
		const many = Array.from({ length: 20 }, (_, i) => done({ id: i }));
		expect(waterfallRows([], many, [], now)).toHaveLength(9);
	});
});

describe('profiles', () => {
	const profile = (over: Partial<Profile> = {}): Profile => ({
		id: 'p',
		name: 'p',
		alias: 'a',
		model_file: 'x-Q4_K_M.gguf',
		ctx_size: 65536,
		...over
	});

	it('picks the busiest, then the loaded', () => {
		const a = profile({ alias: 'a' });
		const b = profile({ alias: 'b' });
		expect(
			busiestProfile(
				[a, b],
				[
					model({ id: 'a', slots: [slot(), slot({ id: 1 })] }),
					model({ id: 'b', slots: [slot({ state: 'decode' })] })
				]
			)
		).toBe(b);
		expect(busiestProfile([a, b], [model({ id: 'b' })])).toBe(b);
		expect(busiestProfile([a, b], [])).toBe(a);
		expect(busiestProfile([], [])).toBeUndefined();
	});

	it('groups set flags and drops empty groups', () => {
		const groups = profileGroups(profile({ parallel: 4, jinja: true, batch: 2048 }));
		expect(groups).toEqual([
			{
				group: 'Context',
				flags: [{ k: '-c', v: 65536 }, { k: '-np', v: 4 }, { k: '--jinja' }]
			},
			{ group: 'Batching', flags: [{ k: '-b', v: 2048 }] }
		]);
	});

	it('reads the quant from a file name', () => {
		expect(quantOf('Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf')).toBe('Q4_K_M');
		expect(quantOf('gpt-oss-20b-mxfp4.gguf')).toBe('MXFP4');
		expect(quantOf('nomic-embed-text-v1.5.f16.gguf')).toBe('F16');
		expect(quantOf('model.gguf')).toBeUndefined();
		expect(quantOf(undefined)).toBeUndefined();
	});
});

describe('modelMeta', () => {
	const gpu = {
		index: 1,
		name: 'g',
		uuid: '',
		driver_version: '',
		utilization: null,
		temperature: null,
		memory_total: 24 * GiB,
		memory_used: 4 * GiB
	};
	const device = { kind: 'device', name: 'CUDA0', model: 2 * GiB, context: GiB, compute: GiB };
	const none = { n_ctx: 0, devices: null };

	it('describes a loaded model', () => {
		expect(
			modelMeta({
				model: model({
					memory: { total: 4 * GiB, devices: [device, { ...device, name: 'CUDA1' }] }
				}),
				profile: { id: 'p', name: 'p', alias: 'm', model_file: 'f', ctx_size: 65536, ngl: '99' },
				gpus: [],
				lastRequestAgo: 31
			})
		).toBe('4.0 GB · ctx 65.5k · ngl 99 · split 2 GPUs · last request 31 s ago');
	});

	it('estimates an unloaded model and says where it fits', () => {
		const estimate = {
			id: 'm',
			status: 'unloaded',
			memory: { min: none, max: { n_ctx: 1, devices: [device] }, effective: none }
		};
		expect(modelMeta({ model: model({ status: 'unloaded' }), estimate, gpus: [gpu] })).toBe(
			'~4.0 GB · would fit on GPU 1'
		);
		expect(modelMeta({ model: model({ status: 'unloaded' }), gpus: [gpu] })).toBe('');
	});
});

describe('charts and text', () => {
	it('rounds a scale up', () => {
		expect(niceMax(102)).toBe(150);
		expect(niceMax(0)).toBe(30);
		expect(niceMax(2900)).toBe(3000);
	});

	it('pads a series from the left', () => {
		expect(padSeries([1, 2], 4)).toEqual([0, 0, 1, 2]);
		expect(padSeries([1, 2, 3], 2)).toEqual([2, 3]);
	});

	it('drops the leading source', () => {
		expect(logMessage('slot', 'slot launch_slot_: id 0')).toBe('launch_slot_: id 0');
		expect(logMessage('srv', 'load_tensors: x')).toBe('load_tensors: x');
	});
});
