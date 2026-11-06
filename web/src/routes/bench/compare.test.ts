import { describe, expect, it, vi } from 'vitest';
import type { Profile, RouterModel } from '$lib/api/client';
import {
	acceptedShare,
	baselineOf,
	compareRequest,
	readRun,
	runComparison,
	verdict,
	type CompareApi,
	type CompareRun
} from './compare';

const reply = (rate: number, extra = {}) => ({
	timings: {
		predicted_per_second: rate,
		predicted_n: 500,
		prompt_ms: 90,
		predicted_ms: 5000,
		...extra
	}
});

function fakeApi(start: Record<string, string>, rates: Record<string, number>) {
	const status = { ...start };
	const calls: string[] = [];
	const api: CompareApi = {
		models: async () =>
			Object.entries(status).map(([id, s]) => ({ id, status: s }) as unknown as RouterModel),
		load: async (a) => {
			calls.push(`load ${a}`);
			status[a] = 'loaded';
		},
		unload: async (a) => {
			calls.push(`unload ${a}`);
			status[a] = 'unloaded';
		},
		chat: async (body) => {
			const { model } = body as { model: string };
			calls.push(`chat ${model}`);
			if (status[model] !== 'loaded') throw new Error(`${model} is not loaded`);
			if (rates[model] === 0) throw new Error('boom');
			return reply(rates[model]);
		},
		wait: async () => {}
	};
	return { api, calls, status };
}

const signal = () => new AbortController().signal;

describe('comparing two profiles', () => {
	it('sends the same deterministic request each way', () => {
		expect(compareRequest('m', 'hi', 100)).toMatchObject({
			model: 'm',
			stream: false,
			cache_prompt: false,
			temperature: 0,
			seed: 1,
			max_tokens: 100
		});
	});

	it('runs each with the other out of memory, and restores what was loaded', async () => {
		const { api, calls, status } = fakeApi(
			{ base: 'unloaded', mtp: 'loaded', other: 'loaded' },
			{ base: 58, mtp: 95.3 }
		);
		const runs = await runComparison(api, ['base', 'mtp'], 'p', 10, signal(), () => {});
		expect(runs.map((r) => r.tokensPerSecond)).toEqual([58, 95.3]);
		expect(calls.slice(0, 2)).toEqual(['unload mtp', 'load base']);
		expect(calls).toContain('chat base');
		expect(status).toEqual({ base: 'unloaded', mtp: 'loaded', other: 'loaded' });
		expect(calls.some((c) => c.includes('other'))).toBe(false);
	});

	it('leaves things as they were when a run fails', async () => {
		const { api, status } = fakeApi({ base: 'loaded', mtp: 'unloaded' }, { base: 0, mtp: 95 });
		await expect(runComparison(api, ['base', 'mtp'], 'p', 10, signal(), () => {})).rejects.toThrow(
			'boom'
		);
		expect(status).toEqual({ base: 'loaded', mtp: 'unloaded' });
	});

	it('says when a model fails to load', async () => {
		const wait = vi.fn(async () => {});
		const api: CompareApi = {
			models: async () => [
				{ id: 'a', status: 'unloaded', failed: true, exit_code: 137 } as RouterModel
			],
			load: async () => {},
			unload: async () => {},
			chat: async () => reply(1),
			wait
		};
		await expect(runComparison(api, ['a', 'a'], 'p', 1, signal(), () => {})).rejects.toThrow(
			'exit 137'
		);
	});

	it('stops when cancelled', async () => {
		const controller = new AbortController();
		const { api, status } = fakeApi({ a: 'unloaded', b: 'unloaded' }, { a: 1, b: 1 });
		const seen: string[] = [];
		const run = runComparison(api, ['a', 'b'], 'p', 1, controller.signal, (s) => {
			seen.push(s);
			if (s.startsWith('Loading')) controller.abort();
		});
		await expect(run).rejects.toThrow();
		expect(seen).toContain('Loading a');
		expect(status).toEqual({ a: 'unloaded', b: 'unloaded' });
	});

	it('reads the timings of a real MTP reply', () => {
		const run = readRun('m', reply(95.29, { draft_n: 5408, draft_n_accepted: 4171 }));
		expect(run).toMatchObject({ tokensPerSecond: 95.29, drafted: 5408, accepted: 4171 });
		expect(acceptedShare(run)).toBe('77%');
		expect(acceptedShare({ ...run, drafted: 0 })).toBe('—');
		expect(() => readRun('m', { nothing: true })).toThrow('without timings');
	});

	it('puts the difference in words', () => {
		const run = (alias: string, tokensPerSecond: number) =>
			({ alias, tokensPerSecond }) as CompareRun;
		expect(verdict([run('base', 58), run('mtp', 95.3)])).toBe(
			'mtp was 64% faster than base on this prompt.'
		);
		expect(verdict([run('base', 100), run('mtp', 80)])).toContain('20% slower');
		expect(verdict([run('base', 100), run('mtp', 100.2)])).toContain('same speed');
		expect(verdict([])).toBe('');
	});

	it('makes a copy without speculation under a free name', () => {
		const p = {
			id: 'x',
			name: 'Qwen',
			alias: 'qwen',
			model_file: 'q.gguf',
			ctx_size: 4096,
			spec_type: 'draft-mtp',
			spec_draft_max: 2,
			load_at_start: true
		} as Profile;
		const copy = baselineOf(p, [p, { ...p, alias: 'qwen-no-spec' }]);
		expect(copy).toMatchObject({
			alias: 'qwen-no-spec-2',
			model_file: 'q.gguf',
			load_at_start: false
		});
		expect(copy).not.toHaveProperty('spec_type');
		expect(copy).not.toHaveProperty('spec_draft_max');
		expect(copy).not.toHaveProperty('id');
		expect(baselineOf({ ...p, spec_type: undefined }, [])).toBeUndefined();
	});
});
