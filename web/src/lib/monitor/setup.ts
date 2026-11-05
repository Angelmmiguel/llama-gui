import type { Hardware, InstallJob } from '$lib/api/client';
import { formatGB } from '$lib/format';

const ENDED = ['done', 'failed', 'canceled', 'cancelled'];

export function jobEnded(job: InstallJob): boolean {
	return ENDED.includes(job.state);
}

/** The install to talk about: one under way, or else the newest. Jobs come oldest first. */
export function currentJob(jobs: readonly InstallJob[]): InstallJob | null {
	return jobs.findLast((j) => !jobEnded(j)) ?? jobs.at(-1) ?? null;
}

export function jobPercent(job: InstallJob): number {
	return job.total > 0 ? Math.min(100, (job.done / job.total) * 100) : 0;
}

/** The detected machine as lines of text, one per fact. */
export function hardwareLines(hw: Hardware): string[] {
	const groups = new Map<string, { count: number; memory: number | null }>();
	for (const g of hw.gpus ?? []) {
		const seen = groups.get(g.name);
		groups.set(g.name, { count: (seen?.count ?? 0) + 1, memory: g.memory_total });
	}
	const gpus = [...groups].map(([name, { count, memory }]) => {
		const size = memory ? ` · ${formatGB(memory, 0)} GB` : '';
		return `${count > 1 ? `${count} × ` : ''}${name}${size}`;
	});
	if (gpus.length === 0) {
		const vendors = [...new Set((hw.render_nodes ?? []).map((n) => n.vendor))];
		gpus.push(vendors.length > 0 ? `${vendors.join(', ')} GPU` : 'No GPU: models run on the CPU');
	}
	if (hw.cuda_version) gpus.push(`CUDA ${hw.cuda_version}`);
	const cpu = hw.cpu?.model
		? [`${hw.cpu.model}${hw.cpu.threads ? ` · ${hw.cpu.threads} threads` : ''}`]
		: [];
	const ram = hw.memory?.total ? [`${formatGB(hw.memory.total, 0)} GB RAM`] : [];
	return [...gpus, ...cpu, ...ram];
}
