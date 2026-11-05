import type { RequestDetail, RequestFilters, RequestRecord, ScatterPoint } from '$lib/api/client';
import { formatMs, formatTokens } from '$lib/format';

export const RANGES = {
	'1h': { label: 'Last hour', phrase: 'the last hour', ms: 3_600_000 },
	'24h': { label: 'Last 24 h', phrase: 'the last 24 hours', ms: 86_400_000 },
	'7d': { label: 'Last 7 days', phrase: 'the last 7 days', ms: 604_800_000 }
} as const;
export type Range = keyof typeof RANGES;

/** The paths behind each endpoint kind: the proxy stores the path as the client sent it. */
export const ENDPOINT_KINDS = {
	chat: ['/v1/chat/completions', '/chat/completions'],
	completions: ['/v1/completions', '/completions', '/completion'],
	embeddings: ['/v1/embeddings', '/embeddings', '/embedding'],
	infill: ['/infill']
} as const;
export type EndpointKind = keyof typeof ENDPOINT_KINDS | 'all';

export interface Filters {
	kind: EndpointKind;
	model: string;
	range: Range;
	/** The selected request. */
	id: number | null;
}

export function parseFilters(params: URLSearchParams): Filters {
	const kind = params.get('endpoint') ?? '';
	const range = params.get('range') ?? '';
	const id = Number(params.get('id'));
	return {
		kind: Object.hasOwn(ENDPOINT_KINDS, kind) ? (kind as EndpointKind) : 'all',
		model: params.get('model') ?? '',
		range: Object.hasOwn(RANGES, range) ? (range as Range) : '1h',
		id: Number.isSafeInteger(id) && id > 0 ? id : null
	};
}

/** The page's query string, leaving out defaults so a plain view has a plain URL. */
export function filtersQuery(f: Filters): string {
	const params = new URLSearchParams();
	if (f.kind !== 'all') params.set('endpoint', f.kind);
	if (f.model) params.set('model', f.model);
	if (f.range !== '1h') params.set('range', f.range);
	if (f.id !== null) params.set('id', String(f.id));
	const qs = params.toString();
	return qs ? `?${qs}` : '';
}

/** The filters as the API takes them, at the moment `now`. */
export function apiFilters(f: Filters, now: number): RequestFilters {
	return {
		since: new Date(now - RANGES[f.range].ms).toISOString(),
		model: f.model || undefined,
		endpoint: f.kind === 'all' ? undefined : ENDPOINT_KINDS[f.kind].join(',')
	};
}

type Timed = Pick<RequestRecord, 'stream' | 'ttft_ms' | 'total_ms' | 'predicted_ms'>;

/**
 * Time until the first token was ready, in ms. A non-streamed TTFT is the first
 * body byte, which is the whole answer, so decode time comes off it there.
 */
export function firstTokenMs(r: Timed): number {
	return r.stream ? r.ttft_ms : Math.max(r.total_ms - r.predicted_ms, 0);
}

/** Whether most of the prompt came from the cache. */
export function isCached(r: { prompt_n: number; cache_n: number }): boolean {
	return r.cache_n > 0 && r.cache_n >= r.prompt_n;
}

/** Smallest of 1, 2, 2.5, 5 and 10 times a power of ten that holds value. */
export function niceMax(value: number): number {
	if (!Number.isFinite(value) || value <= 0) return 1;
	const power = 10 ** Math.floor(Math.log10(value));
	const step = [1, 2, 2.5, 5, 10].find((m) => m * power >= value) ?? 10;
	return step * power;
}

/** Evenly spaced labels from `max` down or up to 0, count + 1 of them. */
export function ticks(max: number, count: number): number[] {
	return Array.from({ length: count + 1 }, (_, i) => (max * i) / count);
}

/** `0`, `500 ms`, `1.5 s`: shorter than formatMs, for axes. */
export function msTick(ms: number): string {
	if (ms === 0) return '0';
	if (ms < 1000) return `${Math.round(ms)} ms`;
	return `${Number((ms / 1000).toFixed(2))} s`;
}

export interface Dot {
	id: number;
	/** Percent of the plot, from the left. */
	x: number;
	/** Percent of the plot, from the bottom. */
	y: number;
	cached: boolean;
	title: string;
}

export interface ScatterView {
	dots: Dot[];
	maxTokens: number;
	maxMs: number;
	/** Where the selected request sits, even when it is not among the dots. */
	selected: { x: number; y: number } | null;
}

/** The API already bounds the points; this is the guard the page relies on. */
const MAX_DOTS = 500;

const failed = (r: { status: number }) => r.status >= 400;

export function scatterView(
	points: readonly ScatterPoint[],
	selected: RequestDetail | null
): ScatterView {
	const measured = points
		.filter((p) => !failed(p) && firstTokenMs(p) > 0)
		.slice(0, MAX_DOTS)
		.map((p) => ({ p, tokens: p.prompt_n + p.cache_n, ms: firstTokenMs(p) }));
	const mark = selected && !failed(selected) && firstTokenMs(selected) > 0 ? selected : null;
	const markTokens = mark ? mark.prompt_n + mark.cache_n : 0;
	const markMs = mark ? firstTokenMs(mark) : 0;

	const maxTokens = niceMax(Math.max(markTokens, ...measured.map((d) => d.tokens)));
	const maxMs = niceMax(Math.max(markMs, ...measured.map((d) => d.ms)));
	return {
		maxTokens,
		maxMs,
		dots: measured.map(({ p, tokens, ms }) => ({
			id: p.id,
			x: (tokens / maxTokens) * 100,
			y: (ms / maxMs) * 100,
			cached: isCached(p),
			title: `#${p.id} · ${formatTokens(tokens)} tokens · ${formatMs(ms)}`
		})),
		selected: mark ? { x: (markTokens / maxTokens) * 100, y: (markMs / maxMs) * 100 } : null
	};
}

export function median(values: readonly number[]): number {
	if (values.length === 0) return 0;
	const sorted = [...values].sort((a, b) => a - b);
	const mid = sorted.length >> 1;
	return sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2;
}

/** How far over the median a gap must be to count as a slow token. */
export const SLOW_FACTOR = 3;

/** Indexes of the gaps over 3× the median. */
export function slowTokens(gaps: readonly number[]): number[] {
	const limit = median(gaps) * SLOW_FACTOR;
	if (limit <= 0) return [];
	return gaps.flatMap((gap, i) => (gap > limit ? [i] : []));
}

type PrefillTimed = Pick<
	RequestRecord,
	'id' | 'started_at' | 'queue_ms' | 'prompt_ms' | 'prompt_n'
>;

/**
 * The other requests whose prefill ran during one of the slow gaps. Slots of one
 * model share a batch, so callers pass requests of the same model. Times are
 * proxy-side estimates: this shows overlap, not cause.
 */
export function overlappingPrefills(
	r: RequestDetail,
	slow: readonly number[],
	others: readonly PrefillTimed[]
): number[] {
	const windows: [number, number][] = [];
	let at = Date.parse(r.started_at) + r.ttft_ms;
	r.gaps.forEach((gap, i) => {
		if (slow.includes(i)) windows.push([at, at + gap]);
		at += gap;
	});
	return others
		.filter((o) => {
			if (o.id === r.id || o.prompt_ms <= 0 || o.prompt_n <= 0) return false;
			const start = Date.parse(o.started_at) + o.queue_ms;
			const end = start + o.prompt_ms;
			return windows.some(([from, to]) => start < to && end > from);
		})
		.map((o) => o.id);
}

const quote = (s: string) => `'${s.replaceAll("'", `'\\''`)}'`;

/** A runnable cURL of a captured request. */
export function curlCommand(
	r: Pick<RequestRecord, 'method' | 'endpoint'>,
	body: string,
	origin: string
): string {
	const method = r.method && r.method !== 'POST' ? ` -X ${r.method}` : '';
	return [
		`curl${method} ${quote(origin + r.endpoint)}`,
		`-H 'Content-Type: application/json'`,
		`--data-raw ${quote(body)}`
	].join(' \\\n  ');
}

function parse(text: string): Record<string, unknown> | null {
	try {
		const value: unknown = JSON.parse(text);
		return value && typeof value === 'object' ? (value as Record<string, unknown>) : null;
	} catch {
		return null;
	}
}

function plain(content: unknown): string {
	if (typeof content === 'string') return content;
	if (Array.isArray(content)) {
		return content
			.map((part: { text?: unknown } | string) =>
				typeof part === 'string' ? part : typeof part?.text === 'string' ? part.text : '[…]'
			)
			.join('\n');
	}
	return content === undefined || content === null ? '' : JSON.stringify(content);
}

function requestLines(text: string): string[] {
	const body = parse(text);
	if (!body) return text ? [text] : [];
	if (Array.isArray(body.messages)) {
		return body.messages.map(
			(m: { role?: unknown; content?: unknown }) => `${plain(m?.role)}: ${plain(m?.content)}`
		);
	}
	const fields = ['prompt', 'input', 'input_prefix', 'input_suffix'].filter((k) => k in body);
	if (fields.length) return fields.map((k) => `${k}: ${plain(body[k])}`);
	return [JSON.stringify(body, null, 2)];
}

/** The text of one answer object, in any of the shapes upstream emits. */
function answerText(o: Record<string, unknown>): string {
	const choice = (Array.isArray(o.choices) ? o.choices[0] : undefined) as
		{ message?: { content?: unknown }; delta?: { content?: unknown }; text?: unknown } | undefined;
	return plain(choice?.message?.content ?? choice?.delta?.content ?? choice?.text ?? o.content);
}

function responseText(text: string): string {
	const whole = parse(text);
	if (whole) return answerText(whole);
	const chunks = text
		.split('\n')
		.filter((line) => line.startsWith('data:'))
		.map((line) => parse(line.slice(5).trim()));
	return chunks.map((c) => (c ? answerText(c) : '')).join('');
}

/** A captured exchange as readable lines; anything unrecognised shows as it is. */
export function messageText(body: { request: string; response: string }): string {
	const lines = requestLines(body.request);
	const answer = responseText(body.response);
	if (answer) lines.push(`assistant: ${answer}`);
	return lines.join('\n');
}

/**
 * Puts a fresh first page on top of what is loaded. Without an overlap, requests
 * were missed in between, so the old rows and their cursor must go: `reset` says so.
 */
export function mergeNewest(
	loaded: readonly RequestRecord[],
	page: readonly RequestRecord[]
): { rows: RequestRecord[]; reset: boolean } {
	const known = new Set(loaded.map((r) => r.id));
	if (loaded.length > 0 && page.length > 0 && !page.some((r) => known.has(r.id))) {
		return { rows: [...page], reset: true };
	}
	return { rows: [...page.filter((r) => !known.has(r.id)), ...loaded], reset: false };
}

/** The tone of a status: 2xx ok, 4xx warn, 5xx error. A classified 200 is still a warning. */
export function statusLevel(label: string): 'ok' | 'warn' | 'error' {
	const code = Number.parseInt(label, 10);
	if (code >= 500) return 'error';
	if (code >= 300 || !Number.isFinite(code) || label.trim() !== String(code)) return 'warn';
	return 'ok';
}
