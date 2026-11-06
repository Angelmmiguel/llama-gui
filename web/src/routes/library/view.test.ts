import { describe, expect, it } from 'vitest';
import type { DownloadJob, GgufMetadata, LibraryFit, LocalModel } from '$lib/api/client';
import {
	displayTags,
	hubKey,
	jobView,
	loadView,
	localModelFor,
	trackedJob,
	downloadState,
	fitScale,
	fitSegments,
	fitTotal,
	hubProblem,
	localAction,
	looksLikeRepo,
	machineLabel,
	matchesFilter,
	orderJobs,
	paramsLabel,
	verdictLabel
} from './view';

const GIB = 1024 ** 3;

const fit = (over: Partial<LibraryFit> = {}): LibraryFit => ({
	weights_bytes: 10 * GIB,
	kv_bytes: 5 * GIB,
	compute_bytes: GIB,
	total_bytes: 16 * GIB,
	verdict: 'fits_gpu',
	offload_percent: 0,
	moe: false,
	estimated: false,
	calibrated: false,
	...over
});

const job = (over: Partial<DownloadJob>): DownloadJob => ({
	id: 'dl-1',
	repo: 'u/m-GGUF',
	variant: 'Q8_0',
	files: ['m-Q8_0.gguf'],
	state: 'downloading',
	done: 0,
	total: 10,
	bytes_per_sec: 0,
	eta_seconds: 0,
	...over
});

const local = (over: Partial<LocalModel>): LocalModel => ({
	id: 'm',
	name: 'm',
	path: 'm.gguf',
	size_bytes: 1,
	quant: 'Q8_0',
	files: ['m.gguf'],
	shards: 1,
	mod_time: '',
	profile: null,
	status: 'unloaded',
	fit: fit(),
	...over
});

describe('fitScale', () => {
	it('places the marker at the real VRAM on a scale fit for the machine', () => {
		const s = fitScale({ vram_bytes: 48 * GIB, ram_bytes: 64 * GIB, headroom_bytes: 0 });
		expect(s).toMatchObject({ bytes: 100 * GIB, marker: 48, label: '48 GB VRAM (scale 0–100 GB)' });
		expect(fitScale({ vram_bytes: 8 * GIB, ram_bytes: 16 * GIB, headroom_bytes: 0 })?.bytes).toBe(
			20 * GIB
		);
	});

	it('uses RAM on a machine without a GPU', () => {
		const s = fitScale({ vram_bytes: 0, ram_bytes: 64 * GIB, headroom_bytes: 0 });
		expect(s).toMatchObject({ bytes: 80 * GIB, marker: 80, label: '64 GB RAM (scale 0–80 GB)' });
	});

	it('needs a machine', () => {
		expect(fitScale(null)).toBeNull();
	});
});

describe('fitSegments', () => {
	it('stops at the end of the track', () => {
		const scale = { bytes: 12 * GIB, marker: 50, label: '' };
		const widths = fitSegments(fit(), scale).map((s) => s.value);
		expect(widths[0]).toBeCloseTo(83.33);
		expect(widths[1]).toBeCloseTo(16.67);
		expect(widths[2]).toBe(0);
	});
});

describe('labels', () => {
	it('names every verdict and keeps unknown ones', () => {
		expect(verdictLabel(fit({ verdict: 'partial_offload', offload_percent: 12 }))).toBe(
			'Partial offload 12%'
		);
		expect(verdictLabel(fit({ verdict: 'fits_ram' }))).toBe('Fits in RAM');
		expect(verdictLabel(fit({ verdict: 'new_one' }))).toBe('new_one');
	});

	it('flags measured and rule-of-thumb totals', () => {
		expect(fitTotal(fit())).toBe('16.0 GB');
		expect(fitTotal(fit({ calibrated: true }))).toBe('16.0 GB measured');
		expect(fitTotal(fit({ estimated: true }))).toBe('≈ 16.0 GB');
	});

	it('describes a model by what its header holds', () => {
		const meta = {
			architecture: 'qwen3',
			name: '',
			param_count: 0,
			block_count: 64,
			context_length: 0,
			expert_count: 0,
			is_moe: false
		};
		expect(paramsLabel(meta)).toBe('qwen3 · 64 layers');
		expect(paramsLabel({ ...meta, param_count: 32_800_000_000 })).toBe('32.8B dense');
		expect(paramsLabel({ ...meta, param_count: 137_000_000, is_moe: true })).toBe('137M MoE');
		expect(paramsLabel(undefined)).toBe('—');
	});

	it('keeps only the tags people recognise', () => {
		expect(
			displayTags(['gguf', 'license:mit', 'chat', 'conversational', 'image-text-to-text'])
		).toEqual(['Chat', 'Vision']);
		expect(displayTags([], { mtp_layers: 1 } as GgufMetadata)).toEqual(['MTP']);
	});

	it('names GPUs compactly, and the CPU when there are none', () => {
		const detected = {
			gpus: [{ name: 'NVIDIA GeForce RTX 4090' }, { name: 'NVIDIA GeForce RTX 4090' }],
			cpu: { model: 'EPYC' }
		} as Parameters<typeof machineLabel>[0];
		const hw = { vram_bytes: 48 * GIB, ram_bytes: 64 * GIB, headroom_bytes: 0 };
		expect(machineLabel(detected, hw)).toBe('2× RTX 4090 · 48 GB VRAM · 64 GB RAM');
		expect(machineLabel(detected, { ...hw, vram_bytes: 0 })).toBe('EPYC · 64 GB RAM');
	});
});

describe('hubProblem', () => {
	it('says the local side still works', () => {
		expect(hubProblem('could not reach Hugging Face')).toBe(
			'Could not reach Hugging Face. Your local models keep working.'
		);
	});
});

describe('looksLikeRepo', () => {
	it.each([
		['ggml-org/SmolVLM-256M-Instruct-GGUF', true],
		['https://huggingface.co/u/m', true],
		['gemma 3', false],
		['a/b/c', false],
		['gemma', false]
	])('%s', (q, want) => expect(looksLikeRepo(q)).toBe(want));
});

describe('localAction', () => {
	it('sends a model with no profile to make one', () => {
		expect(localAction(local({ path: 'a b.gguf' }))).toEqual({ kind: 'profile', path: 'a b.gguf' });
	});

	it('uses the router for a model with a profile', () => {
		const profile = { id: '1', alias: 'qwen', ctx_size: 8192, parallel: 1 };
		expect(localAction(local({ profile }))).toEqual({ kind: 'load', id: 'qwen' });
		expect(localAction(local({ profile, status: 'loaded', router_id: 'qwen' }))).toEqual({
			kind: 'unload',
			id: 'qwen'
		});
		expect(localAction(local({ profile, status: 'loading' }))).toEqual({
			kind: 'busy',
			label: 'Loading'
		});
	});
});

describe('downloadState', () => {
	const files = ['m-Q8_0.gguf', 'mmproj-m.gguf'];

	it('knows what is on its way and what is already here', () => {
		expect(downloadState('u/m-GGUF', files, [], [])).toEqual({ kind: 'available' });
		const running = job({ files });
		expect(downloadState('u/m-GGUF', files, [running], [])).toEqual({
			kind: 'active',
			job: running
		});
		expect(downloadState('u/m-GGUF', files, [job({ files, state: 'failed' })], [])).toEqual({
			kind: 'available'
		});
		expect(
			downloadState('u/m-GGUF', ['sub/m-Q8_0.gguf'], [], [local({ files: ['m-Q8_0.gguf'] })])
		).toEqual({ kind: 'on-disk' });
	});
});

describe('orderJobs', () => {
	it('puts running work first and only recent history', () => {
		const list = [
			job({ id: 'a', state: 'done' }),
			job({ id: 'b', state: 'queued' }),
			job({ id: 'c', state: 'failed' }),
			job({ id: 'd', state: 'downloading' })
		];
		expect(orderJobs(list).map((j) => j.id)).toEqual(['d', 'b', 'c', 'a']);
		expect(orderJobs(list, 1).map((j) => j.id)).toEqual(['d', 'b', 'c']);
	});
});

describe('matchesFilter', () => {
	const row = {
		repo: 'u/m',
		name: 'm',
		author: '',
		downloads: 0,
		likes: 0,
		last_modified: '',
		gated: false,
		tags: [],
		catalog: false
	};

	it('needs a verdict before calling something a fit', () => {
		expect(matchesFilter(row, 'all')).toBe(true);
		expect(matchesFilter(row, 'fits')).toBe(false);
		const variant = {
			id: 'v',
			quant: 'Q8_0',
			files: [],
			size_bytes: 1,
			shards: 1,
			fit: fit(),
			download_files: [],
			download_bytes: 1
		};
		expect(matchesFilter({ ...row, variants: [variant] }, 'fits')).toBe(true);
		expect(matchesFilter({ ...row, tags: ['moe'] }, 'moe')).toBe(true);
	});
});

describe('download and load panels', () => {
	const job = (over: Partial<DownloadJob>): DownloadJob => ({
		id: 'a',
		repo: 'u/m',
		variant: 'Q8_0',
		files: ['m-Q8_0.gguf', 'mmproj.gguf'],
		state: 'downloading',
		done: 512,
		total: 1024,
		bytes_per_sec: 256,
		eta_seconds: 2,
		...over
	});

	it('shows real progress while downloading', () => {
		const v = jobView(job({}));
		expect(v).toMatchObject({ icon: 'spin', percent: 50, bar: 'progress' });
		expect(v.stage).toContain('256 B/s');
		expect(v.right).toContain('50%');
		expect(v.steps.map((s) => s.state)).toEqual(['done', 'now', 'later']);
	});

	it('says when it waits, finishes or fails', () => {
		expect(jobView(job({ state: 'queued' })).steps[0].state).toBe('now');
		const done = jobView(job({ state: 'done', done: 1024 }));
		expect(done).toMatchObject({ icon: 'done', percent: 100 });
		expect(done.steps.every((s) => s.state === 'done')).toBe(true);
		const failed = jobView(job({ state: 'failed', error: 'disk full' }));
		expect(failed).toMatchObject({ icon: 'failed', stage: 'disk full', bar: 'none' });
	});

	it('copes with a state it does not know', () => {
		expect(jobView(job({ state: 'paused' })).stage).toContain('paused');
	});

	it('never claims progress for a load, which upstream does not report', () => {
		const v = loadView({ size_bytes: 1024 } as LocalModel);
		expect(v.bar).toBe('cells');
		expect(v.right).toBe('no progress reported');
	});

	it('follows only the jobs it was asked to, skipping the projector', () => {
		const jobs = [job({ id: 'old', state: 'done' }), job({ id: 'new' })];
		const files = ['mmproj.gguf', 'm-Q8_0.gguf'];
		expect(trackedJob('u/m', files, jobs, new Set(['new']))?.id).toBe('new');
		expect(trackedJob('u/m', files, jobs, new Set())).toBeUndefined();
		expect(trackedJob('u/other', files, jobs, new Set(['new']))).toBeUndefined();
	});

	it('forgets a cancelled job', () => {
		expect(trackedJob('u/m', ['m-Q8_0.gguf'], [job({ state: 'canceled' })], new Set(['a']))).toBe(
			undefined
		);
	});

	it('finds the model a download became', () => {
		const m = { files: ['m-Q8_0.gguf'] } as LocalModel;
		expect(localModelFor(['dir/m-Q8_0.gguf'], [m])).toBe(m);
		expect(localModelFor(['other.gguf'], [m])).toBeUndefined();
		expect(localModelFor([], [m])).toBeUndefined();
	});
});

describe('what a Hugging Face list answers', () => {
	it('changes with the search and the filter', () => {
		expect(hubKey('qwen', undefined)).toBe(hubKey('qwen', undefined));
		expect(hubKey('qwen', undefined)).not.toBe(hubKey('gemma', undefined));
		expect(hubKey('qwen', 'vision')).not.toBe(hubKey('qwen', undefined));
	});
});
