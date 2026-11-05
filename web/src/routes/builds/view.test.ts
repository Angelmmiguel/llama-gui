import { describe, expect, it } from 'vitest';
import {
	ApiError,
	type AvailableRelease,
	type BuildManifest,
	type InstallJob
} from '$lib/api/client';
import {
	affectKeywords,
	ago,
	binaryTag,
	buildTimeline,
	checkLabel,
	compareTarget,
	defaultBackend,
	dotPosition,
	downloadMeta,
	hardwareLine,
	installedMeta,
	installLabel,
	isRunning,
	jobPercent,
	matchedKeywords,
	normalizeTag,
	plainNotes,
	rateLimitReset,
	rollbackId,
	updateSteps,
	upstreamProblem
} from './view';

const NOW = Date.parse('2026-11-06T12:00:00Z');
const MB = 1024 ** 2;

const release = (
	tag: string,
	date: string,
	over: Partial<AvailableRelease> = {}
): AvailableRelease => ({
	tag,
	channel: tag.startsWith('v') ? 'stable' : 'nightly',
	published_at: date,
	body: '',
	...over
});

const job = (over: Partial<InstallJob> = {}): InstallJob => ({
	id: 'b2-cuda',
	version: 'b2',
	backend: 'cuda',
	state: 'downloading',
	step: 'downloading x.tar.gz',
	done: 50 * MB,
	total: 100 * MB,
	bytes_per_sec: 10 * MB,
	started_at: '',
	...over
});

describe('ago', () => {
	it.each([
		['2026-11-06T11:59:50Z', 'just now'],
		['2026-11-06T11:56:00Z', '4 min ago'],
		['2026-11-06T11:00:00Z', '1 hour ago'],
		['2026-11-03T12:00:00Z', '3 days ago'],
		['nonsense', '']
	])('%s', (iso, want) => {
		expect(ago(iso, NOW)).toBe(want);
	});

	it('labels the check button', () => {
		expect(checkLabel(undefined, NOW)).toBe('Check for updates');
		expect(checkLabel({ available: false, checked_at: '2026-11-06T11:56:00Z' }, NOW)).toBe(
			'Check for updates · 4 min ago'
		);
	});
});

describe('target', () => {
	it('finds the binaries', () => {
		expect(binaryTag({ tag: 'b5' })).toBe('b5');
		expect(binaryTag({ tag: 'v1.0', binary_tag: 'b9' })).toBe('b9');
		expect(binaryTag({ tag: 'v1.0', binary_tag: null })).toBeNull();
	});

	it.each([
		['b10', release('b12', ''), 'newer'],
		['b10', release('b10', ''), 'same'],
		['b10', release('b9', ''), 'older'],
		['b10', release('v1', '', { binary_tag: 'b12' }), 'newer'],
		['b10', release('v1', ''), 'unknown'],
		[undefined, release('b12', ''), 'unknown'],
		['b10', null, 'none']
	] as const)('%s against %j', (running, target, want) => {
		expect(compareTarget(running, target)).toBe(want);
	});

	it.each([
		[' b8105 ', 'b8105'],
		['v0.9.0', 'v0.9.0'],
		['v1-rc1', 'v1-rc1'],
		['stable', null],
		['b12; rm -rf', null],
		['../b1', null]
	])('normalizes %j', (text, want) => {
		expect(normalizeTag(text)).toBe(want);
	});

	it('writes the install action', () => {
		expect(installLabel('b12132', { backend: 'cuda', asset: 'a', size: 1, variant: '12.8' })).toBe(
			'Install b12132 · cuda 12.8'
		);
		expect(installLabel('b1', { backend: 'cpu', asset: 'a', size: 1 })).toBe('Install b1 · cpu');
		expect(installLabel('b1', undefined)).toBe('Install b1');
	});

	it('chooses a backend the hardware can use', () => {
		const options = [
			{ backend: 'cpu', asset: '', size: 1 },
			{ backend: 'vulkan', asset: '', size: 1 },
			{ backend: 'rocm', asset: '', size: 1, reason: 'no AMD GPU' }
		];
		expect(defaultBackend(options, 'cpu')).toBe('cpu');
		expect(defaultBackend(options, 'rocm')).toBe('vulkan');
		expect(defaultBackend(options, undefined)).toBe('vulkan');
		expect(defaultBackend(options.slice(0, 1), undefined)).toBe('cpu');
		expect(defaultBackend([], undefined)).toBe('');
	});
});

describe('upstream problems', () => {
	it('tells a rate limit from the rest', () => {
		const limited = new ApiError(503, 'github rate limit exceeded until 2026-11-06T13:05:00Z');
		const got = upstreamProblem(limited);
		expect(got.kind).toBe('rate-limited');
		expect(got.message).toContain('rate limit');
		expect(got.message).toContain('Installed builds keep working');
		expect(upstreamProblem(new ApiError(500, 'dial tcp: no route')).kind).toBe('unreachable');
		expect(upstreamProblem(new TypeError('Failed to fetch')).kind).toBe('unreachable');
	});

	it('reads the reset time, if any', () => {
		expect(rateLimitReset('limit exceeded until nonsense')).toBe('');
		expect(rateLimitReset('limit exceeded')).toBe('');
		expect(rateLimitReset('limit until 2026-11-06T13:05:00Z')).toMatch(/^\d\d:05$/);
	});
});

describe('steps', () => {
	const states = (
		j: InstallJob | null,
		swap = 'idle' as 'idle' | 'switching' | 'done' | 'failed'
	) => updateSteps(j, { state: swap }).map((s) => s.state);

	it('waits with no job', () => {
		expect(states(null)).toEqual(['todo', 'todo', 'todo', 'todo', 'todo']);
	});

	it('follows the job', () => {
		expect(states(job())).toEqual(['active', 'todo', 'todo', 'todo', 'todo']);
		expect(states(job({ state: 'extracting' }))).toEqual([
			'done',
			'done',
			'active',
			'todo',
			'todo'
		]);
		expect(states(job({ state: 'done' }))).toEqual(['done', 'done', 'done', 'done', 'todo']);
		expect(states(job({ state: 'queued' }))).toEqual(['todo', 'todo', 'todo', 'todo', 'todo']);
	});

	it('marks where a job failed', () => {
		const failed = job({ state: 'failed', step: 'verifying x.tar.gz' });
		expect(states(failed)).toEqual(['done', 'failed', 'todo', 'todo', 'todo']);
	});

	it('marks a cancel', () => {
		const got = updateSteps(job({ state: 'canceled', step: '' }), { state: 'idle' });
		expect(got[0].state).toBe('failed');
		expect(got[0].meta).toBe('canceled');
	});

	it('follows the swap', () => {
		expect(states(job({ state: 'done' }), 'switching')[4]).toBe('active');
		expect(states(job({ state: 'done' }), 'failed')[4]).toBe('failed');
		expect(states(job({ state: 'done' }), 'done')[4]).toBe('done');
	});

	it('shows bytes, speed and ETA', () => {
		expect(downloadMeta(job())).toBe('50.0 MB / 100 MB · 10.0 MB/s · ETA 5 s');
		expect(downloadMeta(job({ bytes_per_sec: 0 }))).toBe('50.0 MB / 100 MB');
		expect(downloadMeta(job({ total: 0, bytes_per_sec: 0 }))).toBe('50.0 MB');
		expect(downloadMeta(job({ bytes_per_sec: MB / 2 }))).toContain('ETA 2 min');
	});

	it('measures a running job', () => {
		expect(jobPercent(job())).toBe(45);
		expect(jobPercent(job({ state: 'queued' }))).toBe(0);
		expect(jobPercent(job({ state: 'probing' }))).toBe(98);
		expect(isRunning(job())).toBe(true);
		expect(isRunning(job({ state: 'failed' }))).toBe(false);
		expect(isRunning(null)).toBe(false);
	});
});

describe('installed', () => {
	it('writes what is known and leaves out the rest', () => {
		const b = {
			size_bytes: 25 * MB,
			installed_at: '2026-11-03T12:00:00Z',
			probe: { commit: '2ada83079' }
		} as BuildManifest;
		expect(installedMeta(b, NOW, true)).toBe(
			'25.0 MB · installed 3 days ago · 2ada830 · kept for rollback'
		);
		expect(installedMeta({ ...b, size_bytes: 0 }, NOW, false)).toBe(
			'installed 3 days ago · 2ada830'
		);
	});
});

describe('rollbackId', () => {
	const b = (tag: string, active = false) => ({ id: tag, tag, active }) as BuildManifest;

	it('picks the newest older than what runs', () => {
		expect(rollbackId([b('b9'), b('b5', true), b('b4'), b('b3')])).toBe('b4');
		expect(rollbackId([b('b9'), b('b5', true)])).toBeUndefined();
		expect(rollbackId([b('b4')])).toBeUndefined();
	});
});

describe('notes', () => {
	const nightly =
		'<details open>\n\nserver : fix leak (#1)\r\n\n---------\n\nCo-authored-by: A <a@b.c>\n\n</details>\n\n**Linux:**\n- [Ubuntu x64 (CUDA 12)](https://x)\n';

	it('keeps the release text only', () => {
		expect(plainNotes(nightly)).toBe('server : fix leak (#1)');
	});

	it('cuts the download list of a body without details', () => {
		expect(plainNotes('Text\n\n**Linux:**\n- cuda')).toBe('Text');
	});

	it('removes markup, leaving text that looks like it to the renderer', () => {
		expect(plainNotes('a <script>alert(1)</script> b 32 <= K < 64')).toBe(
			'a alert(1) b 32 <= K < 64'
		);
		expect(plainNotes('')).toBe('');
	});

	it('uses the active backend and the server', () => {
		expect(affectKeywords('cuda')).toEqual(['cuda', 'nvidia', 'server']);
		expect(affectKeywords('sycl-fp16')).toEqual(['sycl', 'server']);
		expect(affectKeywords('other')).toEqual(['other', 'server']);
		expect(affectKeywords(undefined)).toEqual(['server']);
	});

	it.each([
		['server : fix leak', ['server']],
		['ggml-cuda: faster', ['cuda']],
		['CUDA and server', ['cuda', 'server']],
		['webserver cudazzle', []]
	])('matches whole words in %j', (text, want) => {
		expect(matchedKeywords(text, ['cuda', 'server'])).toEqual(want);
	});
});

describe('buildTimeline', () => {
	const nightlies = Array.from({ length: 40 }, (_, i) =>
		release(`b${100 + i}`, new Date(Date.UTC(2026, 9, 1 + i)).toISOString(), {
			body: i === 20 ? 'server : fix' : 'metal : fix'
		})
	);
	const stable = release('v1', '2026-10-21T12:00:00Z', { binary_tag: 'b120' });
	const old = release('v0', '2026-09-01T00:00:00Z', { binary_tag: 'b50' });

	it('keeps only what is between the two', () => {
		const got = buildTimeline(
			nightlies,
			[old, stable],
			'b135',
			nightlies[39],
			null,
			['server'],
			24
		);
		const tags = got.items.map((i) => i.tag);
		expect(tags).toHaveLength(4);
		expect(tags).not.toContain('v1');
		expect(tags).not.toContain('b135');
		expect(tags).toContain('b139');
	});

	it('thins nightlies but no stable release or the ends', () => {
		const got = buildTimeline(nightlies, [stable], 'b100', nightlies[39], null, ['server'], 10);
		const tags = got.items.map((i) => i.tag);
		expect(tags).toHaveLength(10);
		expect(got.hidden).toBe(30);
		expect(tags).toContain('v1');
		expect(tags).toContain('b101');
		expect(tags).toContain('b139');
		expect(got.items.find((i) => i.tag === 'v1')?.stable).toBe(true);
	});

	it('places a stable release of unknown binaries by its date', () => {
		const unknown = release('v2', '2026-10-15T12:00:00Z');
		const since = Date.parse('2026-10-01T00:00:00Z');
		const got = buildTimeline(nightlies, [unknown], 'b100', nightlies[39], since, [], 24);
		expect(got.items.some((i) => i.tag === 'v2')).toBe(true);
	});

	it('records keyword matches', () => {
		const got = buildTimeline(nightlies, [], 'b119', nightlies[21], null, ['server'], 24);
		expect(got.items.find((i) => i.tag === 'b120')?.matches).toEqual(['server']);
		expect(got.items.find((i) => i.tag === 'b121')?.matches).toEqual([]);
	});

	it('positions dots on the track', () => {
		expect(dotPosition(0, 1)).toBe(50);
		expect(dotPosition(0, 5)).toBe(2);
		expect(dotPosition(4, 5)).toBe(98);
	});
});

describe('hardwareLine', () => {
	const cpu = { features: ['avx', 'avx2', 'sse4_2'] };

	it('groups GPUs and lists what matters', () => {
		const g = { name: 'RTX', driver_version: '575' };
		expect(hardwareLine({ gpus: [g, g], cuda_version: '12.8', render_nodes: [], cpu })).toBe(
			'2 × RTX · driver 575 · CUDA 12.8 · AVX + AVX2'
		);
	});

	it('names a GPU it only knows from /dev/dri', () => {
		expect(
			hardwareLine({ gpus: [], cuda_version: '', render_nodes: [{ vendor: 'Intel' }], cpu })
		).toBe('Intel GPU · AVX + AVX2');
	});

	it('says when it found nothing', () => {
		expect(
			hardwareLine({ gpus: [], cuda_version: '', render_nodes: [], cpu: { features: [] } })
		).toBe('No GPU detected');
	});
});
