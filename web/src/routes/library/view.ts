import type {
	DownloadJob,
	Hardware,
	LibraryFit,
	LibraryHardware,
	LocalModel,
	GgufMetadata,
	RepoRow
} from '$lib/api/client';
import type { BarSegment } from '$lib/components/StackedBar.svelte';
import { tones } from '$lib/components/tones';
import { formatBytes, formatDuration, formatGB } from '$lib/format';

export const CTX_OPTIONS = [
	{ value: 8192, label: '8k' },
	{ value: 32768, label: '32k' },
	{ value: 65536, label: '64k' },
	{ value: 131072, label: '128k' }
] as const;

export const KV_OPTIONS = [{ value: 'f16' }, { value: 'q8_0' }, { value: 'q4_0' }] as const;

export type ListFilter = 'all' | 'fits' | 'moe' | 'chat' | 'vision' | 'embedding';

const GIB = 1024 ** 3;

/** The API narrows these on the hub; `fits` and `moe` depend on what the row turns out to be. */
export function serverFilter(filter: ListFilter): string | undefined {
	return filter === 'chat' || filter === 'vision' || filter === 'embedding' ? filter : undefined;
}

export function filterOptions(hostOnly: boolean): { value: ListFilter; label: string }[] {
	return [
		{ value: 'all', label: 'All' },
		{ value: 'fits', label: hostOnly ? 'Fits in RAM' : 'Fits on GPU' },
		{ value: 'moe', label: 'MoE' },
		{ value: 'chat', label: 'Chat' },
		{ value: 'vision', label: 'Vision' },
		{ value: 'embedding', label: 'Embedding' }
	];
}

export const isHostOnly = (hw: LibraryHardware | null | undefined) => !!hw && hw.vram_bytes <= 0;

/** Whether the search box holds a pasted repository rather than search words. */
export function looksLikeRepo(q: string): boolean {
	return /^(https:\/\/huggingface\.co\/)?[A-Za-z0-9][\w.-]*\/[A-Za-z0-9][\w.-]*$/.test(q.trim());
}

function niceCeil(gib: number): number {
	if (gib <= 0) return 1;
	const power = 10 ** Math.floor(Math.log10(gib));
	const step = [1, 1.5, 2, 3, 4, 5, 6, 8, 10].find((s) => s * power >= gib) ?? 10;
	return step * power;
}

export interface FitScale {
	bytes: number;
	/** Where the machine's limit sits on the scale, in percent. */
	marker: number;
	/** The legend's entry: `48 GB VRAM (scale 0–100 GB)`. */
	label: string;
}

/**
 * The bars' shared scale, sized to the machine: room for about twice the VRAM, as models past
 * that are too large anyway, or a little over the RAM without a GPU.
 */
export function fitScale(hw: LibraryHardware | null | undefined): FitScale | null {
	if (!hw || (hw.vram_bytes <= 0 && hw.ram_bytes <= 0)) return null;
	const hostOnly = hw.vram_bytes <= 0;
	const limit = hostOnly ? hw.ram_bytes : hw.vram_bytes;
	const top = niceCeil((limit * (hostOnly ? 1.25 : 2)) / GIB);
	return {
		bytes: top * GIB,
		marker: (limit / (top * GIB)) * 100,
		label: `${formatGB(limit, 0)} GB ${hostOnly ? 'RAM' : 'VRAM'} (scale 0–${top} GB)`
	};
}

/** The bar's three parts as percent of the scale, cut off where the track ends. */
export function fitSegments(fit: LibraryFit, scale: FitScale | null): BarSegment[] {
	if (!scale) return [];
	let room = 100;
	const take = (bytes: number) => {
		const value = Math.min(room, (bytes / scale.bytes) * 100);
		room -= value;
		return value;
	};
	return [
		{ value: take(fit.weights_bytes), color: tones.weights },
		{ value: take(fit.kv_bytes), color: tones.cached },
		{ value: take(fit.compute_bytes), color: tones.compute }
	];
}

export function verdictLabel(fit: LibraryFit): string {
	switch (fit.verdict) {
		case 'fits_gpu':
			return 'Fits on GPU';
		case 'fits_ram':
			return 'Fits in RAM';
		case 'partial_offload':
			return `Partial offload ${fit.offload_percent}%`;
		case 'offload_experts':
			return 'Offload experts';
		case 'too_large':
			return 'Too large';
		default:
			return fit.verdict;
	}
}

export function verdictColor(fit: LibraryFit): string {
	switch (fit.verdict) {
		case 'fits_gpu':
		case 'fits_ram':
			return tones.ok;
		case 'partial_offload':
		case 'offload_experts':
			return tones.warn;
		case 'too_large':
			return tones.errorSoft;
		default:
			return tones.muted;
	}
}

/** The total, flagged when it is a rule of thumb or the router's own measurement. */
export function fitTotal(fit: LibraryFit): string {
	const total = `${formatGB(fit.total_bytes)} GB`;
	if (fit.calibrated) return `${total} measured`;
	return fit.estimated ? `≈ ${total}` : total;
}

export const fitsInMemory = (fit: LibraryFit) =>
	fit.verdict === 'fits_gpu' || fit.verdict === 'fits_ram';

/** `32k` for a context size in tokens. */
export function contextLabel(n: number): string {
	return n >= 1024 ? `${Math.round(n / 1024)}k` : String(n);
}

export function paramsLabel(meta: GgufMetadata | null | undefined): string {
	if (!meta) return '—';
	const moe = meta.is_moe || meta.expert_count > 1;
	if (meta.param_count > 0) {
		const n = meta.param_count;
		const size = n >= 1e9 ? `${(n / 1e9).toFixed(1)}B` : `${Math.round(n / 1e6)}M`;
		return `${size} ${moe ? 'MoE' : 'dense'}`;
	}
	const parts = [
		meta.architecture,
		moe
			? 'MoE'
			: meta.block_count
				? `${meta.block_count} layer${meta.block_count === 1 ? '' : 's'}`
				: ''
	];
	return parts.filter(Boolean).join(' · ') || '—';
}

export function nativeContext(meta: GgufMetadata | null | undefined): string {
	return meta?.context_length ? `${contextLabel(meta.context_length)} native` : '';
}

const TAG_NAMES: Record<string, string> = {
	chat: 'Chat',
	conversational: 'Chat',
	'text-generation': 'Chat',
	code: 'Code',
	vision: 'Vision',
	'image-text-to-text': 'Vision',
	embedding: 'Embedding',
	'feature-extraction': 'Embedding',
	'sentence-similarity': 'Embedding',
	moe: 'MoE'
};

/** The few tags worth showing, out of the hub's long list of licences and base models. */
export function displayTags(tags: readonly string[], meta?: GgufMetadata | null): string[] {
	const out = new Set<string>();
	for (const tag of tags) {
		const name = TAG_NAMES[tag];
		if (name) out.add(name);
	}
	if (meta?.is_moe) out.add('MoE');
	if (meta?.mtp_layers) out.add('MTP');
	return [...out].slice(0, 4);
}

export function repoDescription(row: RepoRow): string {
	if (row.description) return row.description;
	const parts: string[] = [];
	if (row.metadata?.architecture) parts.push(`${row.metadata.architecture} architecture`);
	if (row.metadata?.block_count) parts.push(`${row.metadata.block_count} layers`);
	if (row.downloads) parts.push(`${row.downloads.toLocaleString('en-US')} downloads`);
	if (row.likes) parts.push(`${row.likes.toLocaleString('en-US')} likes`);
	return parts.length ? `${parts.join(' · ')}.` : 'A GGUF repository on Hugging Face.';
}

export function repoHint(row: RepoRow, modelsPath: string): string {
	const parts: string[] = [];
	const best = bestVariant(row);
	if (best) parts.push(`Best fit at this context: ${best.quant}.`);
	if (row.mmproj?.length) parts.push('Multimodal: downloads include the mmproj file.');
	if (row.gated) parts.push('Gated: this repository may need a Hugging Face token.');
	parts.push(
		`Files come from huggingface.co and are saved to ${modelsPath || 'the models directory'}.`
	);
	return parts.join(' ');
}

export function localDescription(m: LocalModel): string {
	const what = [
		m.metadata?.architecture,
		m.shards > 1 ? `${m.shards} shards` : '',
		m.mmproj ? 'with mmproj' : ''
	]
		.filter(Boolean)
		.join(', ');
	const lead = what ? `${what[0].toUpperCase()}${what.slice(1)}. ` : '';
	if (m.status === 'loaded')
		return `${lead}Loaded as ${m.router_id}. The bar shows what the router measured.`;
	if (m.status === 'loading') return `${lead}Loading as ${m.router_id}.`;
	if (m.profile) return `${lead}On disk, with the profile ${m.profile.alias}.`;
	return `${lead}On disk, with no profile yet: a profile is what makes it loadable.`;
}

/** The context a profile asks for: -c is the total, split across the slots. */
export function profileLabel(m: LocalModel): string {
	if (!m.profile) return nativeContext(m.metadata);
	const slots = m.profile.parallel || 1;
	return `profile: ${contextLabel(m.profile.ctx_size)} · ${slots} slot${slots === 1 ? '' : 's'}`;
}

export type LocalAction =
	| { kind: 'unload' | 'load'; id: string }
	| { kind: 'busy'; label: string }
	| { kind: 'profile'; path: string };

export function localAction(m: LocalModel): LocalAction {
	if (m.status === 'loaded' || m.status === 'sleeping')
		return { kind: 'unload', id: m.router_id ?? m.id };
	if (m.status === 'loading') return { kind: 'busy', label: 'Loading' };
	return m.profile ? { kind: 'load', id: m.profile.alias } : { kind: 'profile', path: m.path };
}

export const bestVariant = (row: RepoRow) => row.variants?.find((v) => v.id === row.best);

export function matchesFilter(row: RepoRow, filter: ListFilter): boolean {
	switch (filter) {
		case 'fits':
			return !!row.variants?.some((v) => fitsInMemory(v.fit));
		case 'moe':
			return (
				row.tags.includes('moe') || !!row.metadata?.is_moe || !!row.variants?.some((v) => v.fit.moe)
			);
		default:
			return true;
	}
}

export function matchesLocal(m: LocalModel, q: string): boolean {
	const needle = q.trim().toLowerCase();
	return !needle || m.name.toLowerCase().includes(needle) || m.path.toLowerCase().includes(needle);
}

const baseName = (file: string) => file.slice(file.lastIndexOf('/') + 1);
const isMMProj = (file: string) => baseName(file).toLowerCase().startsWith('mmproj');

export type DownloadState =
	{ kind: 'available' } | { kind: 'on-disk' } | { kind: 'active'; job: DownloadJob };

export function downloadState(
	repo: string,
	files: readonly string[],
	jobs: readonly DownloadJob[],
	local: readonly LocalModel[]
): DownloadState {
	const main = files.find((f) => !isMMProj(f)) ?? files[0];
	if (main === undefined) return { kind: 'available' };
	const job = jobs.find((j) => j.repo === repo && isActive(j) && j.files.some((f) => f === main));
	if (job) return { kind: 'active', job };
	const name = baseName(main);
	return local.some((m) => m.files.includes(name)) ? { kind: 'on-disk' } : { kind: 'available' };
}

export const isActive = (j: DownloadJob) => j.state === 'queued' || j.state === 'downloading';

export const jobPercent = (j: DownloadJob) =>
	j.total > 0 ? Math.min(100, Math.round((j.done / j.total) * 100)) : 0;

export const jobName = (j: DownloadJob) => baseName(j.repo).replace(/-GGUF$/i, '');

/** What is running first, then what waits, then the latest of what ended. */
export function orderJobs(jobs: readonly DownloadJob[], keepFinished = 5): DownloadJob[] {
	const by = (state: string) => jobs.filter((j) => j.state === state);
	const finished = jobs
		.filter((j) => !isActive(j))
		.reverse()
		.slice(0, keepFinished);
	return [...by('downloading'), ...by('queued'), ...finished];
}

/** `2× RTX 4090 · 48 GB VRAM · 64 GB RAM`, or the CPU and RAM without a GPU. */
export function machineLabel(detected: Hardware | null, hw: LibraryHardware | null): string {
	if (!hw) return '';
	const ram = `${formatGB(hw.ram_bytes, 0)} GB RAM`;
	if (hw.vram_bytes <= 0) return [detected?.cpu.model, ram].filter(Boolean).join(' · ');
	const counts = new Map<string, number>();
	for (const g of detected?.gpus ?? []) {
		const name = g.name.replace(/^NVIDIA\s+(GeForce\s+)?/i, '');
		counts.set(name, (counts.get(name) ?? 0) + 1);
	}
	const gpus = [...counts].map(([name, n]) => (n > 1 ? `${n}× ${name}` : name)).join(' + ');
	return [gpus, `${formatGB(hw.vram_bytes, 0)} GB VRAM`, ram].filter(Boolean).join(' · ');
}

/** A Hugging Face failure as a sentence that says what still works. */
export function hubProblem(message: string): string {
	const text = message.charAt(0).toUpperCase() + message.slice(1);
	return `${text}${/[.)]$/.test(text) ? '' : '.'} Your local models keep working.`;
}

export function headroomLabel(hw: LibraryHardware | null): string {
	return hw ? `${formatBytes(hw.headroom_bytes, 0)} headroom kept free.` : '';
}

/** The local model made from these downloaded files, if it is on disk. */
export function localModelFor(
	files: readonly string[],
	local: readonly LocalModel[]
): LocalModel | undefined {
	const main = files.find((f) => !isMMProj(f)) ?? files[0];
	if (main === undefined) return undefined;
	const name = baseName(main);
	return local.find((m) => m.files.includes(name));
}

/** The newest job for these files among those being followed. */
export function trackedJob(
	repo: string,
	files: readonly string[],
	jobs: readonly DownloadJob[],
	tracked: ReadonlySet<string>
): DownloadJob | undefined {
	const main = files.find((f) => !isMMProj(f)) ?? files[0];
	return jobs.findLast(
		(j) => tracked.has(j.id) && j.repo === repo && j.files.includes(main) && j.state !== 'canceled'
	);
}

export interface JobStep {
	label: string;
	state: 'done' | 'now' | 'later';
}

export interface JobView {
	icon: 'spin' | 'done' | 'failed';
	stage: string;
	right: string;
	percent: number;
	/** `cells` when there is no progress to count. */
	bar: 'progress' | 'cells' | 'none';
	steps: JobStep[];
}

const steps = (labels: readonly string[], now: number): JobStep[] =>
	labels.map((label, i) => ({
		label,
		state: i < now ? 'done' : i === now ? 'now' : 'later'
	}));

const DOWNLOAD_STEPS = ['queued', 'download', 'saved'];

export function jobView(job: DownloadJob): JobView {
	const percent = jobPercent(job);
	switch (job.state) {
		case 'queued':
			return {
				icon: 'spin',
				stage: 'Waiting for its turn',
				right: job.total > 0 ? formatBytes(job.total) : '',
				percent: 0,
				bar: 'progress',
				steps: steps(DOWNLOAD_STEPS, 0)
			};
		case 'downloading': {
			const speed = job.bytes_per_sec > 0 ? ` · ${formatBytes(job.bytes_per_sec)}/s` : '';
			const left = job.eta_seconds > 0 ? ` · ${formatDuration(job.eta_seconds)} left` : '';
			return {
				icon: 'spin',
				stage: `${formatBytes(job.done)} / ${formatBytes(job.total)}${speed}`,
				right: `${percent}%${left}`,
				percent,
				bar: 'progress',
				steps: steps(DOWNLOAD_STEPS, 1)
			};
		}
		case 'done':
			return {
				icon: 'done',
				stage: `Saved · ${formatBytes(job.total)}`,
				right: 'done',
				percent: 100,
				bar: 'progress',
				steps: steps(DOWNLOAD_STEPS, 3)
			};
		default:
			return {
				icon: 'failed',
				stage: job.error || `The download ended: ${job.state}`,
				right: '',
				percent,
				bar: 'none',
				steps: []
			};
	}
}

/** llama.cpp reports no progress while loading, so this says what is happening and not how far. */
export function loadView(m: LocalModel): JobView {
	return {
		icon: 'spin',
		stage: `Loading weights · ${formatBytes(m.size_bytes)}`,
		right: 'no progress reported',
		percent: 0,
		bar: 'cells',
		steps: []
	};
}

/** What a Hugging Face list answers: rows change with these, and only their fit with the rest. */
export const hubKey = (query: string, filter: string | undefined) => `${query}|${filter ?? ''}`;
