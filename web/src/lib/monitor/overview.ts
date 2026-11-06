import type {
	Gpu,
	HistoryPoint,
	InflightRequest,
	MemoryDevice,
	Profile,
	RequestRecord,
	RouterModel,
	Slot,
	TelemetryModel,
	TelemetrySnapshot
} from '$lib/api/client';
import { tones } from '$lib/components/tones';
import { formatBytes, formatDuration, formatTokens } from '$lib/format';

const GiB = 1024 ** 3;

/** A slot with the model it belongs to, flattened for the lists on both pages. */
export interface ModelSlot extends Slot {
	model: string;
}

/** Slots of every model, the largest model first: a stable order, so rows keep their place. */
export function allSlots(models: readonly TelemetryModel[]): ModelSlot[] {
	return models
		.filter((m) => m.slots.length > 0)
		.toSorted(
			(a, b) =>
				b.memory.total - a.memory.total ||
				b.slots.length - a.slots.length ||
				a.id.localeCompare(b.id)
		)
		.flatMap((m) => m.slots.map((s) => ({ ...s, model: m.id })));
}

/** CSS color of a slot state. */
export function stateTone(state: string): string {
	if (state === 'decode') return tones.decode;
	if (state === 'prefill') return tones.prefill;
	return tones.idle;
}

/** How much of a slot's context each band of the bar and the map fills. */
export interface SlotFill {
	cached: number;
	prompt: number;
	generated: number;
}

/** Tokens by kind, trusting the total: an idle slot's whole `total` stays cached. */
export function slotFill(s: Slot): SlotFill {
	if (s.state === 'idle') return { cached: s.total, prompt: 0, generated: 0 };
	return { cached: s.cached, prompt: s.prompt, generated: s.generated };
}

/** The share of the prompt already in the slot while it prefills, 0 to 100. */
export function prefillPercent(s: Slot): number {
	if (s.total <= 0) return 0;
	return Math.min(100, Math.round(((s.cached + s.prompt) / s.total) * 100));
}

/** The line under a slot's bar. */
export function slotActivity(s: Slot): string {
	const task = s.task === null ? '' : ` · task ${s.task}`;
	if (s.state === 'decode') return `${s.speed.toFixed(1)} tok/s${task}`;
	if (s.state === 'prefill') return `${prefillPercent(s)}% of ${formatTokens(s.total)} prompt`;
	return s.total > 0 ? 'prompt cached' : 'empty';
}

/** `16k tokens per slot`, or a range when models differ. */
export function contextSummary(slots: readonly Slot[]): string {
	const sizes = slots.map((s) => s.n_ctx);
	if (sizes.length === 0) return '';
	const lo = Math.min(...sizes);
	const hi = Math.max(...sizes);
	return lo === hi
		? `${formatTokens(lo)} tokens per slot`
		: `${formatTokens(lo)} to ${formatTokens(hi)} tokens per slot`;
}

/** Index of the GPU a router device means: `CUDA1` is 1. Null for the host. */
export function deviceIndex(device: Pick<MemoryDevice, 'kind' | 'name'>): number | null {
	if (device.kind === 'host') return null;
	const match = /(\d+)$/.exec(device.name);
	return match ? Number(match[1]) : null;
}

/** `RTX 4090` from `NVIDIA GeForce RTX 4090`. */
export function shortGpuName(name: string): string {
	return (
		name.replace(/^(NVIDIA\s+)?(GeForce\s+)?/i, '').replace(/^AMD\s+(Radeon\s+)?/i, '') || name
	);
}

/** `2× RTX 4090`, one entry per distinct card; `CPU only` without any. */
export function gpuSummary(gpus: readonly Pick<Gpu, 'name'>[]): string {
	if (gpus.length === 0) return 'CPU only';
	const counts = new Map<string, number>();
	for (const g of gpus) {
		const name = shortGpuName(g.name);
		counts.set(name, (counts.get(name) ?? 0) + 1);
	}
	return [...counts].map(([name, n]) => (n > 1 ? `${n}× ${name}` : name)).join(' + ');
}

export function backendLabel(backend: string): string {
	const names: Record<string, string> = { vulkan: 'Vulkan', rocm: 'ROCm', openvino: 'OpenVINO' };
	return names[backend] ?? backend.toUpperCase();
}

export type CellKind = 'weights' | 'kv' | 'compute' | 'other' | 'free';

/** One labelled run of memory cells. */
export interface MemorySegment {
	key: string;
	label: string;
	/** Runs of one kind within the segment, in order. */
	runs: { kind: CellKind; cells: number; breathe?: boolean }[];
	cells: number;
}

export interface MemoryLane {
	segments: MemorySegment[];
	/** `qwen3-coder 14.0 G weights + 3.0 G KV · compute 1.5 G`. */
	note: string;
	usedCells: number;
	totalCells: number;
}

const CELL_SIZES = [0.25, 0.5, 1, 2, 4, 8, 16].map((g) => g * GiB);
const MAX_CELLS = 64;

/** The smallest usual cell size that keeps the largest memory to a readable row of cells. */
export function cellSize(largestTotal: number): number {
	return (
		CELL_SIZES.find((size) => largestTotal / size <= MAX_CELLS) ?? CELL_SIZES[CELL_SIZES.length - 1]
	);
}

export interface Resident {
	id: string;
	device: Pick<MemoryDevice, 'model' | 'context' | 'compute'>;
	busy: boolean;
}

const gb = (n: number) => (n / GiB).toFixed(1);

/**
 * Lays one memory out as cells: each model's weights and KV, then compute buffers of them
 * all, what the system reports beyond those, and free. A model too small for a cell still
 * gets one, so it never disappears from the row.
 */
export function memoryLane(
	residents: readonly Resident[],
	total: number,
	used: number | null,
	cell: number
): MemoryLane {
	const toCells = (bytes: number) => Math.round(bytes / cell);
	const segments: MemorySegment[] = [];
	const notes: string[] = [];
	let known = 0;
	let compute = 0;
	for (const r of [...residents].sort((a, b) => b.device.model - a.device.model)) {
		const { model, context } = r.device;
		known += model + context + r.device.compute;
		compute += r.device.compute;
		const kv = toCells(context);
		const weights = Math.max(toCells(model), kv === 0 ? 1 : 0);
		segments.push({
			key: `model:${r.id}`,
			label: r.id,
			cells: weights + kv,
			runs: [
				{ kind: 'weights', cells: weights },
				{ kind: 'kv', cells: kv, breathe: r.busy }
			]
		});
		notes.push(
			context >= 0.05 * GiB
				? `${r.id} ${gb(model)} G weights + ${gb(context)} G KV`
				: `${r.id} ${gb(model + context)} G`
		);
	}
	const computeCells = toCells(compute);
	if (computeCells > 0) {
		segments.push({
			key: 'compute',
			label: '',
			cells: computeCells,
			runs: [{ kind: 'compute', cells: computeCells }]
		});
	}
	if (compute >= 0.05 * GiB) notes.push(`compute ${gb(compute)} G`);
	const other = used === null ? 0 : toCells(Math.max(0, used - known));
	if (other > 0) {
		segments.push({
			key: 'other',
			label: 'other',
			cells: other,
			runs: [{ kind: 'other', cells: other }]
		});
		notes.push(`other ${gb(used! - known)} G`);
	}
	const totalCells = toCells(total);
	const usedCells = segments.reduce((sum, s) => sum + s.cells, 0);
	const freeCells = Math.max(0, totalCells - usedCells);
	if (freeCells > 0) {
		segments.push({
			key: 'free',
			label: 'free',
			cells: freeCells,
			runs: [{ kind: 'free', cells: freeCells }]
		});
	}
	return { segments, note: notes.join(' · '), usedCells, totalCells };
}

/** Who sits on the memory matching `want`: a GPU's index, or null for the host. */
export function residentsOn(models: readonly TelemetryModel[], want: number | null): Resident[] {
	const out: Resident[] = [];
	for (const m of models) {
		const busy = m.slots.some((s) => s.state !== 'idle');
		for (const device of m.memory.devices) {
			if (deviceIndex(device) === want) out.push({ id: m.id, device, busy });
		}
	}
	return out;
}

export interface WaterfallRow {
	key: string;
	/** `#16`, or `live` while in flight, whose id is not yet the stored one. */
	id: string;
	endpoint: string;
	model: string;
	/** Position in the window, percent. */
	left: number;
	width: number;
	/** Shares of the bar. */
	queue: number;
	prefill: number;
	decode: number;
	live: boolean;
	io: string;
	status: string;
	tone: 'prefill' | 'decode' | 'queued' | 'ok' | 'warn';
}

export const WATERFALL_SECONDS = 60;
export const WATERFALL_ROWS = 9;

function place(startMs: number, endMs: number, nowMs: number): { left: number; width: number } {
	const windowMs = WATERFALL_SECONDS * 1000;
	const start = Math.max(startMs, nowMs - windowMs);
	const end = Math.min(endMs, nowMs);
	return {
		left: ((start - (nowMs - windowMs)) / windowMs) * 100,
		width: (Math.max(0, end - start) / windowMs) * 100
	};
}

function tokensIn(prompt: number, cached: number): string {
	const total = formatTokens(prompt + cached);
	return cached > 0 ? `${total} (${formatTokens(cached)} cached)` : total;
}

function liveRow(r: InflightRequest, slot: Slot | undefined, nowMs: number): WaterfallRow {
	const state = slot?.state ?? 'idle';
	const waiting = r.phase === 'waiting' && state !== 'prefill' && state !== 'decode';
	// Only the current phase is known until the request ends and reports its timings,
	// so the whole bar shows that phase rather than an invented split.
	const prefilling = !waiting && state === 'prefill';
	const decoding = !waiting && !prefilling;
	const io = slot
		? `${tokensIn(slot.prompt, slot.cached)} → ${decoding ? formatTokens(slot.generated) : '…'}`
		: '… → …';
	return {
		key: `live:${r.id}`,
		id: 'live',
		// The proxy only measures POSTs.
		endpoint: `POST ${r.endpoint}`,
		model: r.model,
		...place(Date.parse(r.started_at), nowMs, nowMs),
		queue: waiting ? 1 : 0,
		prefill: prefilling ? 1 : 0,
		decode: decoding ? 1 : 0,
		live: true,
		io,
		status: waiting ? 'queued' : prefilling ? 'prefill' : 'stream',
		tone: waiting ? 'queued' : prefilling ? 'prefill' : 'decode'
	};
}

function doneRow(r: RequestRecord, nowMs: number): WaterfallRow {
	const start = Date.parse(r.started_at);
	const failed = r.status >= 400 || r.error !== '';
	// Refused before any work: its small wait would be invisible in grey, so the bar shows the failure.
	const refused = failed && r.prompt_ms + r.predicted_ms === 0;
	return {
		key: `done:${r.id}`,
		id: `#${r.id}`,
		endpoint: `${r.method} ${r.endpoint}`,
		model: r.model,
		...place(start, start + r.total_ms, nowMs),
		queue: refused ? 0 : r.queue_ms,
		prefill: r.prompt_ms,
		// With no timings at all there is still a bar to draw.
		decode: refused || r.queue_ms + r.prompt_ms + r.predicted_ms === 0 ? 1 : r.predicted_ms,
		live: false,
		io: `${tokensIn(r.prompt_n, r.cache_n)} → ${r.predicted_n > 0 ? formatTokens(r.predicted_n) : '—'}`,
		status: r.error || String(r.status),
		tone: failed ? 'warn' : 'ok'
	};
}

/** In-flight requests, then what finished inside the window, newest first. */
export function waterfallRows(
	inFlight: readonly InflightRequest[],
	recent: readonly RequestRecord[],
	models: readonly TelemetryModel[],
	nowMs: number,
	limit = WATERFALL_ROWS
): WaterfallRow[] {
	const slotOf = (r: InflightRequest) =>
		models.find((m) => m.id === r.model)?.slots.find((s) => s.id === r.slot);
	const live = inFlight
		.toSorted((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at))
		.map((r) => liveRow(r, slotOf(r), nowMs));
	const windowStart = nowMs - WATERFALL_SECONDS * 1000;
	const done = recent
		.filter((r) => Date.parse(r.started_at) + r.total_ms >= windowStart)
		.toSorted((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at))
		.map((r) => doneRow(r, nowMs));
	return [...live, ...done].slice(0, limit);
}

/** The profile of the model doing the most: busy slots, then loaded, then simply first. */
export function busiestProfile(
	profiles: readonly Profile[],
	models: readonly TelemetryModel[]
): Profile | undefined {
	const score = (p: Profile) => {
		const m = models.find((model) => model.id === p.alias);
		if (!m) return 0;
		const busy = m.slots.filter((s) => s.state !== 'idle').length;
		return busy * 100 + m.slots.length + (m.status === 'loaded' ? 1 : 0);
	};
	let best: Profile | undefined;
	for (const p of profiles) {
		if (!best || score(p) > score(best)) best = p;
	}
	return best;
}

export interface ProfileFlag {
	k: string;
	v?: string | number;
}

/** A profile's settings under the headings the Overview shows; unset ones are left out. */
export function profileGroups(p: Profile): { group: string; flags: ProfileFlag[] }[] {
	const flag = (k: string, v: string | number | undefined): ProfileFlag[] =>
		v === undefined || v === '' ? [] : [{ k, v }];
	const groups = [
		{
			group: 'Context',
			flags: [
				...flag('-c', p.ctx_size),
				...flag('-np', p.parallel),
				...(p.jinja ? [{ k: '--jinja' }] : [])
			]
		},
		{
			group: 'GPU',
			flags: [
				...flag('-ngl', p.ngl),
				...flag('-sm', p.split_mode),
				...flag('-ts', p.tensor_split),
				...flag('-fa', p.flash_attn)
			]
		},
		{
			group: 'KV cache',
			flags: [
				...flag('-ctk', p.cache_type_k),
				...flag('-ctv', p.cache_type_v),
				...flag('--cache-reuse', p.cache_reuse)
			]
		},
		{
			group: 'Batching',
			flags: [...flag('-b', p.batch), ...flag('-ub', p.ubatch), ...flag('-t', p.threads)]
		}
	];
	return groups.filter((g) => g.flags.length > 0);
}

/** `Q4_K_M` from a GGUF file name, or undefined. */
export function quantOf(file: string | undefined): string | undefined {
	if (!file) return undefined;
	const match = /(?:^|[-._])(I?Q\d[A-Z0-9_]*|BF16|F16|F32|MXFP\d)(?:[-.]|$)/i.exec(
		file.replace(/\.gguf$/i, '')
	);
	return match?.[1].toUpperCase();
}

/** `layer · 4,1`, from what the profile sets. */
export function tensorSplitLabel(p: Profile | undefined): string {
	if (!p) return '—';
	return [p.split_mode, p.tensor_split].filter(Boolean).join(' · ') || 'default';
}

/** Round top of a chart scale that divides into `steps` even labels. */
export function niceMax(value: number, steps = 3): number {
	const need = Math.max(value, 30) / steps;
	const magnitude = 10 ** Math.floor(Math.log10(need));
	const step = [1, 2, 2.5, 5, 10].find((m) => m * magnitude >= need) ?? 10;
	return step * magnitude * steps;
}

/** Left-pads a series to `length` with zeros: nothing ran before the sampler started. */
export function padSeries(values: readonly number[], length: number): number[] {
	const missing = Math.max(0, length - values.length);
	return [...new Array<number>(missing).fill(0), ...values.slice(-length)];
}

/** Drops the source tag a log message starts with, as the source has its own column. */
export function logMessage(source: string, message: string): string {
	return message.startsWith(`${source} `) ? message.slice(source.length).trimStart() : message;
}

/** `14:32:07` in local time, the way logs are read. */
export function clock(iso: string): string {
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return '—';
	return d.toLocaleTimeString('en-GB', { hour12: false });
}

export interface ModelMetaInput {
	model: TelemetryModel;
	/** The router's own entry, with estimates for a model that is not loaded. */
	estimate?: RouterModel;
	profile?: Profile;
	gpus: readonly Gpu[];
	/** Seconds since this model's last finished request, when one is recent. */
	lastRequestAgo?: number;
}

/** The mono line under a router model's name. */
export function modelMeta({
	model,
	estimate,
	profile,
	gpus,
	lastRequestAgo
}: ModelMetaInput): string {
	const parts: string[] = [];
	if (model.status === 'loaded') {
		if (model.memory.total > 0) parts.push(formatBytes(model.memory.total));
	} else {
		const devices = estimate?.memory.max.devices ?? [];
		const bytes = devices.reduce((sum, d) => sum + d.model + d.context + d.compute, 0);
		if (bytes > 0) {
			parts.push(`~${formatBytes(bytes)}`);
			const fits = gpus.find(
				(g) =>
					g.memory_total !== null &&
					g.memory_used !== null &&
					g.memory_total - g.memory_used >= bytes
			);
			if (fits) parts.push(`would fit on GPU ${fits.index}`);
		}
	}
	if (profile) parts.push(`ctx ${formatTokens(profile.ctx_size)}`);
	if (profile?.ngl) parts.push(`ngl ${profile.ngl}`);
	const gpuCount = model.memory.devices.filter((d) => deviceIndex(d) !== null).length;
	if (gpuCount > 1) parts.push(`split ${gpuCount} GPUs`);
	if (model.status === 'loaded' && lastRequestAgo !== undefined) {
		parts.push(`last request ${formatDuration(lastRequestAgo)} ago`);
	}
	if (estimate?.failed) parts.push(`failed, exit ${estimate.exit_code ?? '?'}`);
	return parts.join(' · ');
}

/** Loaded models first, largest on top, so the list reads like the memory panel. */
export function sortedModels(models: readonly TelemetryModel[]): TelemetryModel[] {
	return models.toSorted(
		(a, b) =>
			Number(b.status === 'loaded') - Number(a.status === 'loaded') ||
			b.memory.total - a.memory.total ||
			a.id.localeCompare(b.id)
	);
}

/** The chart ranges. `1m` is the live stream, and the others come from the stored history. */
export const CHART_RANGES = [
	{ value: '1m', label: '1m', note: 'last 60 s', ticks: ['−60s', '−45s', '−30s', '−15s', 'now'] },
	{
		value: '5m',
		label: '5m',
		note: 'last 5 min',
		ticks: ['−5m', '−4m', '−3m', '−2m', '−1m', 'now']
	},
	{ value: '15m', label: '15m', note: 'last 15 min', ticks: ['−15m', '−10m', '−5m', 'now'] },
	{ value: '1h', label: '1h', note: 'last hour', ticks: ['−60m', '−45m', '−30m', '−15m', 'now'] },
	{ value: '6h', label: '6h', note: 'last 6 hours', ticks: ['−6h', '−4h', '−2h', 'now'] },
	{
		value: '24h',
		label: '24h',
		note: 'last 24 hours',
		ticks: ['−24h', '−18h', '−12h', '−6h', 'now']
	}
] as const;
export type ChartRange = (typeof CHART_RANGES)[number]['value'];

export interface ChartData {
	decode: number[];
	/** Averaged points hide bursts, so the peak comes from what was stored per second. */
	peak: number;
	spark: { prefill: number[]; ttft: number[]; rpm: number[]; kv: number[] };
}

/** The same shape from either source, so the page draws one chart. */
export function chartData(
	range: ChartRange,
	series: TelemetrySnapshot['series'] | undefined,
	history: readonly HistoryPoint[] | null
): ChartData {
	if (range === '1m' || !history) {
		const decode = series?.decode ?? [];
		return {
			decode: padSeries(decode, 60),
			peak: Math.max(0, ...decode),
			spark: {
				prefill: series?.spark.prefill ?? [],
				ttft: series?.spark.ttft ?? [],
				rpm: series?.spark.rpm ?? [],
				kv: series?.spark.kv ?? []
			}
		};
	}
	return {
		decode: history.map((p) => p.decode),
		peak: Math.max(0, ...history.map((p) => p.decode_peak)),
		spark: {
			prefill: history.map((p) => p.prefill),
			ttft: history.map((p) => p.ttft),
			rpm: history.map((p) => p.rpm),
			kv: history.map((p) => p.kv)
		}
	};
}
