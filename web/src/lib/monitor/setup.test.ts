import { describe, expect, it } from 'vitest';
import type { Gpu, Hardware, InstallJob } from '$lib/api/client';
import { currentJob, hardwareLines, jobEnded, jobPercent } from './setup';

const job = (id: string, state: string, done = 0, total = 0) =>
	({ id, state, done, total }) as InstallJob;

describe('currentJob', () => {
	it('prefers one under way over a newer one that ended', () => {
		expect(currentJob([job('a', 'downloading'), job('b', 'failed')])?.id).toBe('a');
	});
	it('falls back to the newest, and to nothing', () => {
		expect(currentJob([job('a', 'failed'), job('b', 'done')])?.id).toBe('b');
		expect(currentJob([])).toBeNull();
	});
});

it('jobEnded tolerates states it does not know as under way', () => {
	expect(jobEnded(job('a', 'done'))).toBe(true);
	expect(jobEnded(job('a', 'someday'))).toBe(false);
});

it('jobPercent is bounded and survives an unknown total', () => {
	expect(jobPercent(job('a', 'x', 5, 10))).toBe(50);
	expect(jobPercent(job('a', 'x', 5, 0))).toBe(0);
	expect(jobPercent(job('a', 'x', 11, 10))).toBe(100);
});

describe('hardwareLines', () => {
	const gib = 2 ** 30;
	const base: Hardware = {
		gpus: [],
		cuda_version: '',
		render_nodes: [],
		cpu: { model: 'EPYC', cores: 8, threads: 16, features: [] },
		memory: { total: 32 * gib, available: gib },
		disks: []
	};
	const gpu = { name: 'RTX 4090', memory_total: 24 * gib } as Gpu;

	it('counts identical GPUs', () => {
		const lines = hardwareLines({ ...base, gpus: [gpu, gpu], cuda_version: '12.8' });
		expect(lines[0]).toMatch(/^2 × RTX 4090 · 2\d GB$/);
		expect(lines).toContain('CUDA 12.8');
	});

	it('says when there is no GPU', () => {
		const lines = hardwareLines(base);
		expect(lines[0]).toBe('No GPU: models run on the CPU');
		expect(lines).toContain('EPYC · 16 threads');
	});

	it('names the vendor of a GPU known only by its render node', () => {
		const nodes = [{ path: '/dev/dri/renderD128', vendor: 'Intel', vendor_id: '1' }];
		expect(hardwareLines({ ...base, render_nodes: nodes })[0]).toBe('Intel GPU');
	});
});
