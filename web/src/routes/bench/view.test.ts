import { describe, expect, it } from 'vitest';
import type { BenchLine, BenchResult, BuildManifest } from '$lib/api/client';
import { tones } from '$lib/components/tones';
import {
	agoText,
	decodeChart,
	depthLabel,
	estimateNote,
	historyRows,
	modelFile,
	modelLabel,
	prefillBars,
	prefillSubtitle,
	resultRows,
	runOptionLabel,
	runPercent,
	setupProblem,
	toggled
} from './view';

const line = (label: string, points: [number, number][]): BenchLine => ({
	label,
	flash_attn: 'on',
	cache_k: 'f16',
	cache_v: 'f16',
	backend: 'CUDA',
	ngl: '99',
	points: points.map(([depth, tokens_per_sec]) => ({ depth, tokens_per_sec, stddev: 0 }))
});

const build = (id: string, tag: string): BuildManifest =>
	({ id, tag, backend: 'cuda' }) as BuildManifest;

describe('modelFile', () => {
	it('is the path of a single file', () => {
		expect(modelFile({ id: 'a', name: 'a', path: 'a.gguf', files: ['a.gguf'] })).toBe('a.gguf');
	});

	it('is the first shard of a directory, never the projector', () => {
		expect(
			modelFile({
				id: 'd',
				name: 'd',
				path: 'd',
				mmproj: 'mmproj.gguf',
				files: ['mmproj.gguf', 'x-00002-of-00002.gguf', 'x-00001-of-00002.gguf']
			})
		).toBe('d/x-00001-of-00002.gguf');
	});
});

it('modelLabel adds the quantisation when known', () => {
	const base = { id: 'a', name: 'a', path: 'a', files: [] };
	expect(modelLabel({ ...base, quant: 'Q4' })).toBe('a · Q4');
	expect(modelLabel(base)).toBe('a');
	expect(modelLabel({ ...base, name: 'a-Q4', quant: 'Q4' })).toBe('a-Q4');
});

describe('toggled', () => {
	const order = ['a', 'b', 'c'];
	it('adds in the order of the options', () => {
		expect(toggled(['c'], 'a', order)).toEqual(['a', 'c']);
	});
	it('removes, but never the last one', () => {
		expect(toggled(['a', 'b'], 'a', order)).toEqual(['b']);
		expect(toggled(['a'], 'a', order)).toEqual(['a']);
	});
});

it('depthLabel shortens whole kibitokens', () => {
	expect(depthLabel(0)).toBe('0');
	expect(depthLabel(4096)).toBe('4k');
	expect(depthLabel(5000)).toBe('5000');
});

describe('decodeChart', () => {
	it('aligns lines on the union of depths and labels the axes', () => {
		const c = decodeChart(
			[
				line('a', [
					[0, 100],
					[4096, 80]
				]),
				line('b', [[0, 50]])
			],
			[0, 4096, 8192]
		);
		expect(c.depths).toEqual([0, 4096, 8192]);
		expect(c.xLabels).toEqual(['d 0', '4k', '8k']);
		expect(c.series[0].data).toEqual([100, 80]);
		expect(c.series[1].data).toEqual([50]);
		expect(c.series[0].slots).toBe(3);
		expect(c.yLabels[0]).toBe(String(c.max));
		expect(c.yLabels.at(-1)).toBe('0');
	});

	it('keeps a gap inside a line for the renderer to skip', () => {
		const c = decodeChart(
			[
				line('a', [
					[0, 1],
					[8192, 2]
				])
			],
			[0, 4096, 8192]
		);
		expect(c.series[0].data).toEqual([1, NaN, 2]);
	});

	it('tells lines apart past the palette and dashes the muted one', () => {
		const lines = Array.from({ length: 9 }, (_, i) => line(`l${i}`, [[0, 1]]));
		const c = decodeChart(lines, [0]);
		expect(c.series[8].color).toBe(c.series[0].color);
		expect(c.legend[2].dashed).toBe(true);
		expect(c.legend[0].value).toBe('1.0 t/s @0');
	});

	it('survives no lines', () => {
		const c = decodeChart([], []);
		expect(c.series).toEqual([]);
		expect(c.legend).toEqual([]);
	});
});

describe('prefillBars', () => {
	it('shows the shallowest and the deepest, scaled to the fastest', () => {
		const [row] = prefillBars([
			line('a', [
				[0, 5000],
				[4096, 4000],
				[8192, 2500]
			])
		]);
		expect(row.bars.map((b) => b.note)).toEqual(['', '@8k']);
		expect(row.bars[0].width).toBe(80);
		expect(row.bars[1].width).toBe(40);
		expect(row.bars[1].faded).toBe(true);
	});

	it('shows one bar for one depth', () => {
		expect(prefillBars([line('a', [[0, 10]])])[0].bars).toHaveLength(1);
	});
});

it('prefillSubtitle only claims the depths measured', () => {
	expect(
		prefillSubtitle([
			line('a', [
				[0, 1],
				[16384, 1]
			])
		])
	).toBe('tok/s at an empty context and at 16k');
	expect(prefillSubtitle([line('a', [[0, 1]])])).toBe('tok/s at an empty context');
	expect(prefillSubtitle([])).toBe('tok/s');
});

describe('resultRows', () => {
	const result = (kind: string, tokens_per_sec: number): BenchResult => ({
		backend: 'CUDA',
		ngl: '99',
		flash_attn: 'on',
		cache_k: 'f16',
		cache_v: 'q8_0',
		test: kind,
		kind,
		n: 1,
		depth: 0,
		tokens_per_sec,
		stddev: 1.5
	});

	it('scales bars within a kind', () => {
		const rows = resultRows([result('pp', 5000), result('pp', 2500), result('tg', 100)]);
		expect(rows.map((r) => r.relative)).toEqual([100, 50, 100]);
		expect(rows[0].color).toBe(tones.prefill);
		expect(rows[2].color).toBe(tones.decode);
		expect(rows[0].kv).toBe('f16 / q8_0');
		expect(rows[2].speed).toBe('100.00 ± 1.50');
	});

	it('does not divide by zero', () => {
		expect(resultRows([result('tg', 0)])[0].relative).toBe(0);
	});
});

describe('historyRows', () => {
	const point = (b: string, tps: number, delta: number | null, run_id = 1) => ({
		build: b,
		run_id,
		started_at: '2026-01-01T00:00:00Z',
		tokens_per_sec: tps,
		delta_percent: delta
	});

	it('shows deltas and marks the current build', () => {
		const rows = historyRows(
			[point('b1-cuda', 100, null), point('b2-cuda', 90, -10)],
			[build('b1-cuda', 'b1'), build('b2-cuda', 'b2')],
			'b2-cuda',
			undefined
		);
		expect(rows.map((r) => r.tag)).toEqual(['b1', 'b2']);
		expect(rows[0].delta).toBe('');
		expect(rows[1].delta).toBe('−10.0%');
		expect(rows[1].deltaColor).toBe(tones.error);
		expect(rows[1].meta).toBe('CUDA · current');
		expect(rows[1].color).toBe(tones.decode);
	});

	it('offers a run on an active build without one', () => {
		const rows = historyRows(
			[point('b1-cuda', 100, null)],
			[build('b1-cuda', 'b1'), build('b2-cuda', 'b2')],
			'b2-cuda',
			undefined
		);
		expect(rows[1]).toMatchObject({ tag: 'b2', canRun: true, delta: 'not run yet', runId: null });
	});

	it('announces an update that is not installed, and only then', () => {
		const builds = [build('b1-cuda', 'b1')];
		const points = [point('b1-cuda', 100, null)];
		expect(historyRows(points, builds, 'b1-cuda', 'b9').at(-1)?.delta).toBe('run after update');
		expect(historyRows(points, builds, 'b1-cuda', 'b1')).toHaveLength(1);
	});

	it('names a build that is gone by its id, and is empty before any run', () => {
		expect(historyRows([point('b0-cpu', 1, null)], [], undefined, undefined)[0]).toMatchObject({
			tag: 'b0-cpu',
			meta: 'removed'
		});
		expect(historyRows([], [], 'b1', 'b9')).toEqual([]);
	});
});

it('estimateNote switches to minutes', () => {
	expect(estimateNote(1, 1, 3)).toBe('1 test × 1 repetition ≈ 3 s.');
	expect(estimateNote(80, 5, 960)).toBe('80 tests × 5 repetitions ≈ 16 min.');
});

it('agoText rounds to the largest unit', () => {
	const now = Date.parse('2026-01-02T00:00:00Z');
	expect(agoText('2026-01-01T23:59:50Z', now)).toBe('just now');
	expect(agoText('2026-01-01T23:50:00Z', now)).toBe('10 min ago');
	expect(agoText('2026-01-01T22:00:00Z', now)).toBe('2 h ago');
	expect(agoText('2025-12-30T00:00:00Z', now)).toBe('3 d ago');
});

it('runOptionLabel names unfinished states', () => {
	const run = { id: 3, started_at: '2026-01-01T00:00:00Z', state: 'failed' };
	const now = Date.parse('2026-01-01T00:00:10Z');
	expect(runOptionLabel(run as never, 'b1', now)).toBe('#3 · b1 · just now · failed');
});

it('setupProblem drops the package prefix', () => {
	expect(setupProblem('bench: invalid setup: 270 tests, at most 256')).toBe(
		'270 tests, at most 256'
	);
	expect(setupProblem('other')).toBe('other');
});

it('runPercent is bounded', () => {
	expect(runPercent({ done: 5, total: 10 })).toBe(50);
	expect(runPercent({ done: 12, total: 10 })).toBe(100);
	expect(runPercent({ done: 0, total: 0 })).toBe(0);
});
