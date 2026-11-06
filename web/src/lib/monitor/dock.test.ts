import { describe, expect, it } from 'vitest';
import type { Gpu, LocalModel, Profile, RouterModel, TelemetryModel } from '$lib/api/client';
import { diskRows, dockCells, freeMemory, isResident, roleOf, type FreeMemory } from './dock';

const GiB = 1024 ** 3;

function model(id: string, status: string, gib = 0, busy = 0, slots = 1): TelemetryModel {
	return {
		id,
		status,
		memory: { total: gib * GiB, devices: [] },
		slots: Array.from(
			{ length: slots },
			(_, i) => ({ id: i, state: i < busy ? 'decode' : 'idle' }) as TelemetryModel['slots'][number]
		)
	};
}

const profile = (alias: string, extra: Partial<Profile> = {}) =>
	({
		id: alias,
		name: alias,
		alias,
		model_file: `${alias}-Q4_K_M.gguf`,
		ctx_size: 4096,
		...extra
	}) as Profile;

function file(path: string, gib: number, verdict = 'fits_gpu'): LocalModel {
	return {
		id: path,
		name: path,
		path,
		size_bytes: gib * GiB,
		quant: 'Q4_K_M',
		files: [path],
		shards: 1,
		mod_time: '',
		profile: null,
		status: 'unloaded',
		fit: { total_bytes: gib * GiB, verdict } as LocalModel['fit']
	};
}

function estimate(id: string, gib: number, extra: Partial<RouterModel> = {}): RouterModel {
	const devices = [{ kind: 'device', name: 'CUDA0', model: gib * GiB, context: 0, compute: 0 }];
	const memory = { n_ctx: 4096, devices } as RouterModel['memory']['max'];
	return {
		id,
		status: 'unloaded',
		memory: { min: memory, max: memory, effective: memory },
		...extra
	};
}

const vram = (gib: number): FreeMemory => ({ bytes: gib * GiB, kind: 'VRAM' });
const base = { estimates: [], profiles: [], local: [], free: vram(10), headroom: 0 };

describe('resident models', () => {
	it('counts what holds memory', () => {
		expect(['loaded', 'loading', 'sleeping'].every(isResident)).toBe(true);
		expect(isResident('unloaded')).toBe(false);
	});

	it('lists them largest first, with their slots in use', () => {
		const got = dockCells(
			[model('small', 'loaded', 1), model('big', 'loaded', 9, 2, 4), model('off', 'unloaded')],
			[profile('big')],
			[]
		);
		expect(got.map((c) => c.id)).toEqual(['big', 'small']);
		expect(got[0]).toMatchObject({ busy: 2, slots: 4, quant: 'Q4_K_M', loading: false });
	});

	it('shows a model as loading the moment it is asked for', () => {
		const got = dockCells([model('m', 'unloaded')], [], [], new Set(['m']));
		expect(got).toHaveLength(1);
		expect(got[0].loading).toBe(true);
	});

	it('gives a loading model its size on disk', () => {
		const got = dockCells(
			[model('m', 'loading')],
			[profile('m', { model_file: 'dir/m.gguf' })],
			[file('dir', 8)]
		);
		expect(got[0].fileBytes).toBe(8 * GiB);
	});
});

describe('roles', () => {
	it.each([
		[undefined, 'model'],
		[profile('a'), 'chat'],
		[profile('a', { mmproj: 'x.gguf' }), 'vision'],
		[profile('a', { extra_args: ['--embeddings'] }), 'embeddings'],
		[profile('a', { extra_args: ['--pooling', 'mean', '--embedding'] }), 'embeddings'],
		[profile('a', { extra_args: ['--rerank'] }), 'reranking']
	])('%#', (p, want) => {
		expect(roleOf(p)).toBe(want);
	});
});

describe('free memory', () => {
	const gpu = (total: number | null, used: number | null) =>
		({ memory_total: total, memory_used: used }) as Gpu;

	it('sums the GPUs', () => {
		expect(freeMemory([gpu(10, 4), gpu(10, 1)], 99)).toEqual({ bytes: 15, kind: 'VRAM' });
	});

	it('falls back to RAM when no GPU reports memory', () => {
		expect(freeMemory([], 99)).toEqual({ bytes: 99, kind: 'RAM' });
		expect(freeMemory([gpu(null, null)], 99)).toEqual({ bytes: 99, kind: 'RAM' });
	});
});

describe('disk rows', () => {
	it('says a model fits, leaving out what the router keeps free', () => {
		const [row] = diskRows({
			...base,
			models: [model('m', 'unloaded')],
			estimates: [estimate('m', 4)],
			headroom: 2 * GiB
		});
		expect(row).toMatchObject({ level: 'ok', load: 'm', profile: 'm', disabled: false });
		expect(row.verdict).toBe('Fits · needs 4.0 GB of 8.0 GB free');
	});

	it('offers to swap out the smallest idle model that makes room', () => {
		const [row] = diskRows({
			...base,
			free: vram(1),
			models: [
				model('m', 'unloaded'),
				model('tiny', 'loaded', 1),
				model('mid', 'loaded', 6),
				model('huge', 'loaded', 20)
			],
			estimates: [estimate('m', 5)]
		});
		expect(row).toMatchObject({ level: 'warn', unloadFirst: 'mid' });
		expect(row.verdict).toBe('Needs 5.0 GB · will unload mid');
	});

	it('never swaps out a busy model', () => {
		const [row] = diskRows({
			...base,
			free: vram(1),
			models: [model('m', 'unloaded'), model('busy', 'loaded', 20, 1)],
			estimates: [estimate('m', 5)],
			local: [],
			profiles: []
		});
		expect(row.unloadFirst).toBeUndefined();
	});

	it('blocks a model that needs the GPU when nothing can make room', () => {
		const [row] = diskRows({
			...base,
			free: vram(1),
			models: [model('m', 'unloaded')],
			profiles: [profile('m', { model_file: 'm.gguf' })],
			local: [file('m.gguf', 5)]
		});
		expect(row).toMatchObject({ level: 'error', disabled: true });
		expect(row.verdict).toBe('Needs 5.0 GB · not enough VRAM right now');
	});

	it('allows a model that can run partly on the CPU', () => {
		const [row] = diskRows({
			...base,
			free: vram(1),
			models: [model('m', 'unloaded')],
			profiles: [profile('m', { model_file: 'm.gguf' })],
			local: [file('m.gguf', 5, 'partial_offload')]
		});
		expect(row).toMatchObject({ level: 'warn', disabled: false, load: 'm' });
	});

	it('does not block when the need is unknown', () => {
		const [row] = diskRows({ ...base, models: [model('m', 'unloaded')] });
		expect(row).toMatchObject({
			level: 'ok',
			disabled: false,
			verdict: 'Memory need not known yet'
		});
	});

	it('shows a failed load and lets it be retried', () => {
		const [row] = diskRows({
			...base,
			models: [model('m', 'unloaded')],
			estimates: [estimate('m', 4, { failed: true, exit_code: 137 })]
		});
		expect(row).toMatchObject({ level: 'error', disabled: false });
		expect(row.verdict).toBe('Failed to load last time, exit 137');
	});

	it('leaves out what is loaded or was just asked for', () => {
		const got = diskRows({
			...base,
			models: [model('a', 'loaded', 1), model('b', 'loading'), model('c', 'unloaded')],
			pending: new Set(['c'])
		});
		expect(got).toEqual([]);
	});

	it('lists files without a profile, which need one to load', () => {
		const got = diskRows({
			...base,
			models: [model('used', 'unloaded')],
			profiles: [profile('used', { model_file: 'used.gguf' })],
			local: [file('used.gguf', 1), file('spare.gguf', 1)]
		});
		const spare = got.find((r) => r.path);
		expect(spare).toMatchObject({ name: 'spare.gguf', path: 'spare.gguf' });
		expect(spare?.load).toBeUndefined();
		expect(got).toHaveLength(2);
	});

	it('puts what can be loaded first', () => {
		const got = diskRows({
			...base,
			free: vram(2),
			models: [model('a-big', 'unloaded'), model('b-small', 'unloaded')],
			estimates: [estimate('a-big', 5), estimate('b-small', 1)],
			profiles: [profile('a-big', { model_file: 'a.gguf' })],
			local: [file('a.gguf', 5)]
		});
		expect(got.map((r) => r.profile)).toEqual(['b-small', 'a-big']);
	});
});

describe('speculation in the dock', () => {
	it('names it with how much was kept', () => {
		const m = model('m', 'loaded', 5);
		m.speculation = { types: 'none,draft-mtp', drafted: 9952, accepted: 7639 };
		expect(dockCells([m], [], [])[0]).toMatchObject({ speculation: 'MTP', acceptance: '77%' });
	});

	it('shows nothing without it', () => {
		const cell = dockCells([model('m', 'loaded', 5)], [], [])[0];
		expect(cell.speculation).toBeUndefined();
		expect(cell.acceptance).toBeUndefined();
	});
});
