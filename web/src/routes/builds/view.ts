import type {
	AvailableRelease,
	BuildManifest,
	BuildOption,
	InstallJob,
	UpdateInfo
} from '$lib/api/client';
import { ApiError } from '$lib/api/client';
import { formatBytes } from '$lib/format';

export type Channel = 'stable' | 'nightly';

/** `4 min ago`, `3 days ago`, `just now`. */
export function ago(iso: string | null | undefined, now: number): string {
	const then = Date.parse(iso ?? '');
	if (!Number.isFinite(then)) return '';
	const s = Math.max(0, Math.round((now - then) / 1000));
	if (s < 45) return 'just now';
	const unit = (n: number, name: string) => `${n} ${name}${n === 1 ? '' : 's'} ago`;
	if (s < 3600) return `${Math.max(1, Math.round(s / 60))} min ago`;
	if (s < 86400) return unit(Math.round(s / 3600), 'hour');
	return unit(Math.round(s / 86400), 'day');
}

/** The header button's text. */
export function checkLabel(info: UpdateInfo | undefined, now: number): string {
	const when = ago(info?.checked_at, now);
	return when ? `Check for updates · ${when}` : 'Check for updates';
}

/** The number of a `b*` tag, or null for a `v*` one. */
export function buildNumber(tag: string | null | undefined): number | null {
	const m = /^b(\d+)$/.exec(tag ?? '');
	return m ? Number(m[1]) : null;
}

/** The tag holding a release's binaries, when known. */
export function binaryTag(r: Pick<AvailableRelease, 'tag' | 'binary_tag'>): string | null {
	if (r.binary_tag) return r.binary_tag;
	return buildNumber(r.tag) !== null ? r.tag : null;
}

export type TargetState = 'none' | 'newer' | 'same' | 'older' | 'unknown';

/** Where a target stands against what runs. */
export function compareTarget(
	running: string | null | undefined,
	target: AvailableRelease | null
): TargetState {
	if (!target) return 'none';
	const from = buildNumber(running);
	const to = buildNumber(binaryTag(target));
	if (from === null || to === null) return 'unknown';
	return to > from ? 'newer' : to === from ? 'same' : 'older';
}

/** A valid tag, or stable or nightly, as the API takes it. Rejected before it costs a call. */
export function normalizeTag(text: string): string | null {
	const t = text.trim();
	return /^(b\d+|v\d+(?:[.-][0-9A-Za-z]+)*)$/.test(t) ? t : null;
}

/** The hero's button: `Install b12132 · cuda 12.8`. */
export function installLabel(tag: string, option: BuildOption | undefined): string {
	if (!option) return `Install ${tag}`;
	return `Install ${tag} · ${option.backend}${option.variant ? ` ${option.variant}` : ''}`;
}

/** The backend to offer first: the active one if usable, otherwise the first usable non-CPU. */
export function defaultBackend(
	options: readonly BuildOption[],
	active: string | undefined
): string {
	const usable = options.filter((o) => !o.reason);
	if (active && usable.some((o) => o.backend === active)) return active;
	return (usable.find((o) => o.backend !== 'cpu') ?? usable[0])?.backend ?? '';
}

export function isRateLimited(e: unknown): boolean {
	return e instanceof ApiError && e.status === 503 && /rate limit/i.test(e.message);
}

/** When a rate limit message says it lifts, as a local clock time. */
export function rateLimitReset(message: string): string {
	const m = /until (\S+)/.exec(message);
	const when = m ? new Date(m[1]) : null;
	if (!when || Number.isNaN(when.getTime())) return '';
	return `${String(when.getHours()).padStart(2, '0')}:${String(when.getMinutes()).padStart(2, '0')}`;
}

export type UpstreamProblem = { kind: 'rate-limited' | 'unreachable'; message: string };

/** Why the release list is missing, in words for the page. */
export function upstreamProblem(e: unknown): UpstreamProblem {
	const text = e instanceof Error ? e.message : String(e);
	if (isRateLimited(e)) {
		const reset = rateLimitReset(text);
		return {
			kind: 'rate-limited',
			message: `GitHub's rate limit was hit${reset ? ` until ${reset}` : ''}, so releases and notes are hidden for now. Installed builds keep working.`
		};
	}
	return {
		kind: 'unreachable',
		message: `Cannot read releases from GitHub: ${text}. Installed builds keep working.`
	};
}

export type StepState = 'todo' | 'active' | 'done' | 'failed';

export interface Step {
	key: string;
	label: string;
	state: StepState;
	meta: string;
}

const JOB_ORDER = ['downloading', 'verifying', 'extracting', 'probing'];

/** What `bytes_per_sec` and the remainder say about the download. */
export function downloadMeta(job: Pick<InstallJob, 'done' | 'total' | 'bytes_per_sec'>): string {
	const parts: string[] = [];
	if (job.total > 0) parts.push(`${formatBytes(job.done)} / ${formatBytes(job.total)}`);
	else if (job.done > 0) parts.push(formatBytes(job.done));
	if (job.bytes_per_sec > 0) {
		parts.push(`${formatBytes(job.bytes_per_sec)}/s`);
		if (job.total > job.done) {
			const left = Math.ceil((job.total - job.done) / job.bytes_per_sec);
			parts.push(`ETA ${left < 60 ? `${left} s` : `${Math.round(left / 60)} min`}`);
		}
	}
	return parts.join(' · ');
}

/** The update steps, given the install job and how far the swap has come. */
export function updateSteps(
	job: InstallJob | null,
	swap: { state: 'idle' | 'switching' | 'done' | 'failed'; meta?: string }
): Step[] {
	const base = [
		{ key: 'downloading', label: 'Download' },
		{ key: 'verifying', label: 'Verify digest' },
		{ key: 'extracting', label: 'Extract' },
		{ key: 'probing', label: 'Probe the binary' }
	];
	const stateIdx =
		job?.state === 'done' ? JOB_ORDER.length : job ? JOB_ORDER.indexOf(job.state) : -1;
	const failedAt = job?.state === 'failed' || job?.state === 'canceled' ? lastStep(job) : -1;
	const steps: Step[] = base.map((s, i) => {
		let state: StepState = 'todo';
		if (failedAt >= 0) state = i < failedAt ? 'done' : i === failedAt ? 'failed' : 'todo';
		else if (stateIdx > i) state = 'done';
		else if (stateIdx === i) state = 'active';
		let meta = '';
		if (i === 0 && job && (state === 'active' || state === 'done' || state === 'failed')) {
			meta = downloadMeta(job);
		}
		if (state === 'failed' && job?.state === 'canceled') meta = 'canceled';
		return { key: s.key, label: s.label, state, meta };
	});
	steps.push({
		key: 'swap',
		label: 'Swap and restart',
		state: swap.state === 'switching' ? 'active' : swap.state === 'idle' ? 'todo' : swap.state,
		meta: swap.meta ?? 'when you switch'
	});
	return steps;
}

/** A finished job only keeps a `step` while failed: `downloading <asset>`, and so on. */
function lastStep(job: InstallJob): number {
	const word = job.step.split(' ')[0];
	const i = JOB_ORDER.indexOf(word);
	return i >= 0 ? i : 0;
}

export function isRunning(job: InstallJob | null | undefined): boolean {
	return !!job && !['done', 'failed', 'canceled'].includes(job.state);
}

/** Overall progress of a running job: the download's share, then the short steps. */
export function jobPercent(job: InstallJob): number {
	if (job.state === 'queued') return 0;
	if (job.state === 'downloading') {
		return job.total > 0 ? Math.min(100, Math.round((job.done / job.total) * 90)) : 0;
	}
	if (job.state === 'verifying') return 92;
	if (job.state === 'extracting') return 95;
	if (job.state === 'probing') return 98;
	return 100;
}

export function jobLine(job: InstallJob): string {
	if (job.state === 'queued') return `Waiting to install ${job.id}…`;
	return job.step ? `${job.step[0].toUpperCase()}${job.step.slice(1)}…` : `${job.state}…`;
}

/** What to say about a build in the installed list. */
export function installedMeta(b: BuildManifest, now: number, rollback: boolean): string {
	return [
		b.size_bytes ? formatBytes(b.size_bytes) : '',
		`installed ${ago(b.installed_at, now)}`,
		b.probe.commit ? b.probe.commit.slice(0, 7) : '',
		rollback ? 'kept for rollback' : ''
	]
		.filter(Boolean)
		.join(' · ');
}

const KEYWORDS: Record<string, string[]> = {
	cuda: ['cuda', 'nvidia'],
	vulkan: ['vulkan'],
	rocm: ['rocm', 'hip', 'amd'],
	'sycl-fp16': ['sycl'],
	'sycl-fp32': ['sycl'],
	openvino: ['openvino'],
	cpu: ['cpu', 'avx']
};

/** The words "affects you" looks for: the active backend's names, and the server. */
export function affectKeywords(backend: string | undefined): string[] {
	return [...(backend ? (KEYWORDS[backend] ?? [backend]) : []), 'server'];
}

/**
 * The release's own text, as plain text: a nightly's body ends in a list of download
 * links that mention every backend, so it would match anything. Tags are stripped for
 * reading; the page renders the result as text either way, never as HTML.
 */
export function plainNotes(body: string): string {
	let text = body.replace(/\r/g, '');
	const details = /<details[^>]*>([\s\S]*?)<\/details>/i.exec(text);
	if (details) text = details[1];
	text = text.split(/\n\s*\*\*(?:Website|Attestations|macOS\/iOS|Linux|Windows|Android):\*\*/)[0];
	return text
		.replace(/<\/?[a-z][^<>]*>/gi, '')
		.replace(/^-{3,}$/gm, '')
		.replace(/^Co-authored-by:.*$/gim, '')
		.replace(/\n{3,}/g, '\n\n')
		.trim();
}

/** The keywords a release's notes contain, as whole words, ignoring case. */
export function matchedKeywords(notes: string, keywords: readonly string[]): string[] {
	const lower = notes.toLowerCase();
	return keywords.filter((k) => new RegExp(`(^|[^a-z0-9])${k}([^a-z0-9]|$)`).test(lower));
}

export interface TimelineItem {
	tag: string;
	release: AvailableRelease;
	stable: boolean;
	matches: string[];
}

export interface Timeline {
	items: TimelineItem[];
	/** Nightlies in range that were thinned away, to say so. */
	hidden: number;
}

const published = (r: AvailableRelease) => Date.parse(r.published_at) || 0;

/**
 * Oldest first: the releases after `fromDate` up to the target. Every stable release and the
 * first and last nightlies stay, and the other nightlies are thinned evenly down to `max`,
 * keeping any that match first because those are what the panel is for.
 */
export function buildTimeline(
	nightlies: readonly AvailableRelease[],
	stables: readonly AvailableRelease[],
	running: string,
	target: AvailableRelease,
	fromDate: number | null,
	keywords: readonly string[],
	max: number
): Timeline {
	const from = buildNumber(running);
	const to = buildNumber(binaryTag(target));
	const until = published(target);
	const since = fromDate ?? -Infinity;
	const inRange = nightlies.filter((r) => {
		const n = buildNumber(r.tag);
		return n !== null && from !== null && to !== null && n > from && n <= to;
	});
	const anchors = stables.filter((r) => {
		const when = published(r);
		const n = buildNumber(r.binary_tag);
		if (n !== null && from !== null) return n > from && when <= until;
		return when > since && when <= until;
	});
	const item = (r: AvailableRelease, stable: boolean): TimelineItem => ({
		tag: r.tag,
		release: r,
		stable,
		matches: matchedKeywords(plainNotes(r.body), keywords)
	});
	const room = Math.max(max - anchors.length - 2, 0);
	const kept = new Set<AvailableRelease>();
	const sorted = inRange.toSorted((a, b) => (buildNumber(a.tag) ?? 0) - (buildNumber(b.tag) ?? 0));
	if (sorted.length > 0) kept.add(sorted[0]).add(sorted[sorted.length - 1]);
	const middle = sorted.slice(1, -1);
	if (middle.length <= room) {
		for (const r of middle) kept.add(r);
	} else if (room > 0) {
		const step = middle.length / room;
		for (let i = 0; i < room; i++) kept.add(middle[Math.floor(i * step)]);
	}
	const items = [
		...sorted.filter((r) => kept.has(r)).map((r) => item(r, false)),
		...anchors.map((r) => item(r, true))
	].toSorted((a, b) => published(a.release) - published(b.release));
	return { items, hidden: sorted.length - kept.size };
}

/** A dot's left edge in percent, keeping its label on the track. */
export function dotPosition(index: number, count: number): number {
	return count <= 1 ? 50 : (index / (count - 1)) * 96 + 2;
}

/** The detected hardware, in one line for the picker. */
export function hardwareLine(hw: {
	gpus: { name: string; driver_version: string }[];
	cuda_version: string;
	render_nodes: { vendor: string }[];
	cpu: { features: string[] };
}): string {
	const parts: string[] = [];
	const names = [...new Set(hw.gpus.map((g) => g.name))];
	for (const name of names) {
		const n = hw.gpus.filter((g) => g.name === name).length;
		parts.push(n > 1 ? `${n} × ${name}` : name);
	}
	if (hw.gpus[0]?.driver_version) parts.push(`driver ${hw.gpus[0].driver_version}`);
	if (hw.cuda_version) parts.push(`CUDA ${hw.cuda_version}`);
	if (hw.gpus.length === 0) {
		for (const v of new Set(hw.render_nodes.map((r) => r.vendor))) parts.push(`${v} GPU`);
	}
	const simd = hw.cpu.features.filter((f) => /^avx/i.test(f)).map((f) => f.toUpperCase());
	if (simd.length > 0) parts.push(simd.join(' + '));
	return parts.length > 0 ? parts.join(' · ') : 'No GPU detected';
}

/** The build to go back to: the newest installed one older than what runs. */
export function rollbackId(builds: readonly BuildManifest[]): string | undefined {
	const current = buildNumber(builds.find((b) => b.active)?.tag);
	if (current === null) return undefined;
	return builds
		.filter((b) => !b.active && (buildNumber(b.tag) ?? Infinity) < current)
		.toSorted((a, b) => (buildNumber(b.tag) ?? 0) - (buildNumber(a.tag) ?? 0))[0]?.id;
}
