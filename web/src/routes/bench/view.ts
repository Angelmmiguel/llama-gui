import type {
	BenchBuildPoint,
	BenchLine,
	BenchResult,
	BenchRun,
	BuildManifest,
	LocalModelFile
} from '$lib/api/client';
import type { LegendItem } from '$lib/components/Legend.svelte';
import { tones } from '$lib/components/tones';
import { formatInt } from '$lib/format';
import { backendLabel, niceMax } from '$lib/monitor/overview';

export const FLASH_OPTIONS = ['on', 'off', 'auto'] as const;
export const KV_OPTIONS = ['f16', 'q8_0', 'q4_0'] as const;
export const DEPTH_OPTIONS = [0, 4096, 8192, 16384, 32768] as const;
export const REPETITION_OPTIONS = [3, 5, 10] as const;

/** What a model is benchmarked by: its file, or a directory's first shard. */
export function modelFile(m: LocalModelFile): string {
	if (m.files.length === 1 && m.files[0] === m.path) return m.path;
	const names = m.files.filter((f) => f !== m.mmproj && !/mmproj/i.test(f)).toSorted();
	const main = names.find((f) => /-0*1-of-\d+\.gguf$/i.test(f)) ?? names[0] ?? m.files[0] ?? '';
	return main ? `${m.path}/${main}` : m.path;
}

export function modelLabel(m: LocalModelFile): string {
	return m.quant && !m.name.includes(m.quant) ? `${m.name} · ${m.quant}` : m.name;
}

/** The selection with one option flipped, in the options' order, and never left empty. */
export function toggled<T>(selected: readonly T[], option: T, order: readonly T[]): T[] {
	if (!selected.includes(option)) return order.filter((o) => o === option || selected.includes(o));
	return selected.length > 1 ? selected.filter((o) => o !== option) : [...selected];
}

export function depthLabel(depth: number): string {
	return depth >= 1024 && depth % 1024 === 0 ? `${depth / 1024}k` : String(depth);
}

const LINE_STYLES: readonly { color: string; width: number; dash?: string }[] = [
	{ color: tones.decode, width: 2.5 },
	{ color: tones.prefill, width: 2 },
	{ color: tones.muted, width: 2, dash: '5 4' },
	{ color: tones.cached, width: 2 },
	{ color: tones.ok, width: 2 },
	{ color: tones.warn, width: 2 },
	{ color: tones.weights, width: 2, dash: '2 3' },
	{ color: tones.errorSoft, width: 2, dash: '5 4' }
];

export interface DecodeChart {
	depths: number[];
	series: {
		data: number[];
		color: string;
		width: number;
		dash?: string;
		slots: number;
	}[];
	max: number;
	yLabels: string[];
	xLabels: string[];
	legend: LegendItem[];
}

/**
 * Lines over the depths evenly spaced, as in the design. A line stops where its
 * measurements do, so a running benchmark never stretches one across the chart.
 */
export function decodeChart(lines: readonly BenchLine[], planned: readonly number[]): DecodeChart {
	const depths = [
		...new Set([...planned, ...lines.flatMap((l) => l.points.map((p) => p.depth))])
	].toSorted((a, b) => a - b);
	const top = niceMax(
		Math.max(0, ...lines.flatMap((l) => l.points.map((p) => p.tokens_per_sec))),
		4
	);
	const series = lines.map((line, i) => {
		const style = LINE_STYLES[i % LINE_STYLES.length];
		const at = new Map(line.points.map((p) => [p.depth, p.tokens_per_sec]));
		const data = depths.map((d) => at.get(d) ?? NaN);
		while (data.length > 0 && !Number.isFinite(data[data.length - 1])) data.pop();
		return { ...style, data, slots: depths.length };
	});
	return {
		depths,
		series,
		max: top,
		yLabels: [4, 3, 2, 1, 0].map((i) => formatInt((top / 4) * i)),
		xLabels: depths.map((d, i) => (i === 0 ? `d ${depthLabel(d)}` : depthLabel(d))),
		legend: lines.map((line, i) => {
			const style = LINE_STYLES[i % LINE_STYLES.length];
			const last = line.points.at(-1);
			return {
				label: line.label,
				color: style.color,
				shape: 'line' as const,
				dashed: style.dash !== undefined,
				value: last ? `${last.tokens_per_sec.toFixed(1)} t/s @${depthLabel(last.depth)}` : undefined
			};
		})
	};
}

export interface PrefillBar {
	label: string;
	/** Empty for the first bar, `@16k` for the deepest. */
	note: string;
	value: string;
	/** Percent of the row. The widest stops short, so its figure fits beside it. */
	width: number;
	faded: boolean;
}

/** Each configuration at its shallowest and deepest measured depth. */
export function prefillBars(lines: readonly BenchLine[]): { label: string; bars: PrefillBar[] }[] {
	const top = Math.max(1, ...lines.flatMap((l) => l.points.map((p) => p.tokens_per_sec)));
	return lines.map((line) => {
		const first = line.points[0];
		const last = line.points.at(-1);
		const points = first === last ? [first] : [first, last];
		return {
			label: line.label,
			bars: points.flatMap((p, i) =>
				p
					? [
							{
								label: line.label,
								note: p.depth > 0 ? `@${depthLabel(p.depth)}` : '',
								value: formatInt(p.tokens_per_sec),
								width: (p.tokens_per_sec / top) * 80,
								faded: i > 0
							}
						]
					: []
			)
		};
	});
}

export function prefillSubtitle(lines: readonly BenchLine[]): string {
	const all = lines.flatMap((l) => l.points.map((p) => p.depth));
	if (all.length === 0) return 'tok/s';
	const lo = Math.min(...all);
	const hi = Math.max(...all);
	const name = (d: number) => (d === 0 ? 'an empty context' : depthLabel(d));
	return lo === hi ? `tok/s at ${name(lo)}` : `tok/s at ${name(lo)} and at ${name(hi)}`;
}

export interface ResultRow {
	key: string;
	backend: string;
	ngl: string;
	flash: string;
	kv: string;
	test: string;
	speed: string;
	relative: number;
	color: string;
}

/** Bars are relative within a kind: prompt processing dwarfs decoding. */
export function resultRows(results: readonly BenchResult[]): ResultRow[] {
	const top = new Map<string, number>();
	for (const r of results) top.set(r.kind, Math.max(top.get(r.kind) ?? 0, r.tokens_per_sec));
	return results.map((r, i) => {
		const digits = r.kind === 'pp' ? 0 : 2;
		const speed = r.kind === 'pp' ? formatInt(r.tokens_per_sec) : r.tokens_per_sec.toFixed(2);
		return {
			key: `${i}`,
			backend: r.backend,
			ngl: r.ngl,
			flash: r.flash_attn,
			kv: `${r.cache_k} / ${r.cache_v}`,
			test: r.test,
			speed: `${speed} ± ${r.stddev.toFixed(digits)}`,
			relative: ((r.tokens_per_sec || 0) / (top.get(r.kind) || 1)) * 100,
			color: r.kind === 'pp' ? tones.prefill : r.kind === 'tg' ? tones.decode : tones.cached
		};
	});
}

export interface HistoryRow {
	key: string;
	tag: string;
	meta: string;
	value: string;
	width: number;
	color: string;
	delta: string;
	deltaColor: string;
	runId: number | null;
	/** This build has no run yet and can have one now. */
	canRun: boolean;
}

function buildMeta(m: BuildManifest | undefined, active: boolean): string {
	const backend = m ? backendLabel(m.backend) : '';
	return [backend, active ? 'current' : m ? '' : 'removed'].filter(Boolean).join(' · ');
}

function deltaText(d: number | null): { delta: string; deltaColor: string } {
	if (d === null) return { delta: '', deltaColor: tones.muted };
	if (Math.abs(d) < 0.05) return { delta: '0.0%', deltaColor: tones.muted };
	return d > 0
		? { delta: `+${d.toFixed(1)}%`, deltaColor: tones.ok }
		: { delta: `−${Math.abs(d).toFixed(1)}%`, deltaColor: tones.error };
}

/**
 * The builds a model has run on, oldest first. The active build follows when it has
 * no run yet, which is the moment to re-run after an update, then a known update.
 */
export function historyRows(
	points: readonly BenchBuildPoint[],
	builds: readonly BuildManifest[],
	activeId: string | undefined,
	updateTag: string | undefined
): HistoryRow[] {
	const top = niceMax(Math.max(0, ...points.map((p) => p.tokens_per_sec)), 4);
	const manifest = (id: string) => builds.find((b) => b.id === id);
	const rows: HistoryRow[] = points.map((p, i) => {
		const active = p.build === activeId;
		const newest = i === points.length - 1;
		return {
			key: p.build,
			tag: manifest(p.build)?.tag ?? p.build,
			meta: buildMeta(manifest(p.build), active),
			value: p.tokens_per_sec.toFixed(1),
			width: (p.tokens_per_sec / top) * 100,
			color: active ? tones.decode : newest ? tones.prefill : tones.compute,
			...deltaText(p.delta_percent),
			runId: p.run_id,
			canRun: false
		};
	});
	if (points.length > 0 && activeId && !points.some((p) => p.build === activeId)) {
		rows.push({
			key: activeId,
			tag: manifest(activeId)?.tag ?? activeId,
			meta: buildMeta(manifest(activeId), true),
			value: '—',
			width: 0,
			color: tones.decode,
			delta: 'not run yet',
			deltaColor: tones.muted,
			runId: null,
			canRun: true
		});
	}
	if (points.length > 0 && updateTag && !builds.some((b) => b.tag === updateTag)) {
		rows.push({
			key: `update-${updateTag}`,
			tag: updateTag,
			meta: 'not installed yet',
			value: '—',
			width: 0,
			color: tones.decode,
			delta: 'run after update',
			deltaColor: tones.muted,
			runId: null,
			canRun: false
		});
	}
	return rows;
}

export function estimateNote(tests: number, repetitions: number, seconds: number): string {
	const plural = tests === 1 ? 'test' : 'tests';
	const reps = repetitions === 1 ? 'repetition' : 'repetitions';
	const time = seconds < 90 ? `${Math.max(1, seconds)} s` : `${Math.round(seconds / 60)} min`;
	return `${tests} ${plural} × ${repetitions} ${reps} ≈ ${time}.`;
}

export function agoText(iso: string, now: number): string {
	const seconds = Math.max(0, (now - Date.parse(iso)) / 1000);
	if (!Number.isFinite(seconds)) return '';
	if (seconds < 60) return 'just now';
	if (seconds < 3600) return `${Math.round(seconds / 60)} min ago`;
	if (seconds < 86_400) return `${Math.round(seconds / 3600)} h ago`;
	return `${Math.round(seconds / 86_400)} d ago`;
}

export function runOptionLabel(run: BenchRun, tag: string, now: number): string {
	const state = run.state === 'done' ? '' : ` · ${run.state}`;
	return `#${run.id} · ${tag} · ${agoText(run.started_at, now)}${state}`;
}

/** The API's validation messages, without the package prefix. */
export function setupProblem(message: string): string {
	return message.replace(/^bench: (invalid setup: )?/, '');
}

export function runPercent(run: Pick<BenchRun, 'done' | 'total'>): number {
	return run.total > 0 ? Math.min(100, (run.done / run.total) * 100) : 0;
}
