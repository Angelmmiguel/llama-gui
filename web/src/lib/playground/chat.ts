import { formatInt, formatMs } from '$lib/format';

/** What a request sets. An empty field is left out, so the model's profile decides. */
export interface Settings {
	system: string;
	/** Empty until the slider is touched. */
	temperature: string;
	top_p: string;
	top_k: string;
	min_p: string;
	max_tokens: string;
	repeat_penalty: string;
	seed: string;
	/** Empty for any free slot. */
	slot: string;
	stream: boolean;
	cachePrompt: boolean;
	/** Empty for the template's default. Only offered when the model's template takes one. */
	reasoningEffort: string;
}

export const NUMBER_FIELDS = [
	'top_p',
	'top_k',
	'min_p',
	'max_tokens',
	'repeat_penalty',
	'seed'
] as const;

export const emptySettings = (): Settings => ({
	system: '',
	temperature: '',
	top_p: '',
	top_k: '',
	min_p: '',
	max_tokens: '',
	repeat_penalty: '',
	seed: '',
	slot: '',
	stream: true,
	cachePrompt: true,
	reasoningEffort: ''
});

/** Clears what falls back to the profile, and keeps what is not a sampling setting. */
export const withProfileDefaults = (s: Settings): Settings => ({
	...emptySettings(),
	system: s.system,
	slot: s.slot,
	stream: s.stream,
	cachePrompt: s.cachePrompt
});

export interface Timings {
	cache_n?: number;
	prompt_n?: number;
	prompt_ms?: number;
	predicted_n?: number;
	predicted_ms?: number;
	predicted_per_second?: number;
	/** Speculative decoding's tokens, absent without it. */
	draft_n?: number;
	draft_n_accepted?: number;
}

export interface ChatMessage {
	role: 'user' | 'assistant';
	text: string;
	model?: string;
	reasoning?: string;
	/** Each piece as it arrived, close to one token each while streaming. */
	chunks?: string[];
	/** Milliseconds before each piece, as seen by this browser. */
	gaps?: number[];
	ttftMs?: number;
	/** How long it reasoned before it began answering. */
	thoughtMs?: number;
	timings?: Timings;
	finish?: string;
	slot?: number;
	/** The last chunk as the server sent it. */
	raw?: unknown;
	error?: string;
	done?: boolean;
	stopped?: boolean;
}

export interface ChatRequest {
	model: string;
	messages: { role: string; content: string }[];
	stream: boolean;
	cache_prompt: boolean;
	timings_per_token?: boolean;
	[setting: string]: unknown;
}

const number = (v: string): number | undefined => {
	const n = Number(v.trim());
	return v.trim() === '' || !Number.isFinite(n) ? undefined : n;
};

export function buildRequest(
	settings: Settings,
	model: string,
	messages: readonly ChatMessage[]
): ChatRequest {
	const history = messages
		.filter((m) => m.text !== '' && !m.error)
		.map((m) => ({ role: m.role, content: m.text }));
	const system = settings.system.trim();
	const body: ChatRequest = {
		model,
		messages: system ? [{ role: 'system', content: system }, ...history] : history,
		stream: settings.stream,
		cache_prompt: settings.cachePrompt
	};
	for (const key of ['temperature', ...NUMBER_FIELDS] as const) {
		const value = number(settings[key]);
		if (value !== undefined) body[key] = value;
	}
	const slot = number(settings.slot);
	if (slot !== undefined) body.id_slot = slot;
	if (settings.reasoningEffort) body.reasoning_effort = settings.reasoningEffort;
	return body;
}

/** When a template takes an effort but names no levels: the ones every such template understands. */
const COMMON_EFFORTS = ['low', 'medium', 'high'];

/**
 * The reasoning efforts a model's chat template takes, from its /props, or null if it takes none.
 * The server accepts any word without complaint, so only what is listed here should be offered.
 */
export function reasoningEfforts(props: unknown): string[] | null {
	const caps = (props as { chat_template_caps?: Record<string, unknown> } | null)
		?.chat_template_caps;
	if (caps?.supports_reasoning_effort !== true) return null;
	const levels = caps.reasoning_effort_levels;
	const named = Array.isArray(levels)
		? levels.filter((l): l is string => typeof l === 'string' && l !== '')
		: [];
	return named.length > 0 ? named : COMMON_EFFORTS;
}

export function curlFor(origin: string, body: ChatRequest): string {
	const json = JSON.stringify(body).replaceAll("'", "'\\''");
	const stream = body.stream ? '-N ' : '';
	return `curl ${stream}${origin}/v1/chat/completions -H 'Content-Type: application/json' -d '${json}'`;
}

/** Splits server-sent events into their data, whatever way the bytes were cut. */
export class SseParser {
	#buffer = '';

	feed(text: string): string[] {
		this.#buffer += text;
		const lines = this.#buffer.split(/\r?\n/);
		this.#buffer = lines.pop() ?? '';
		return lines.filter((l) => l.startsWith('data:')).map((l) => l.slice(5).trim());
	}
}

interface Chunk {
	choices?: {
		delta?: { content?: string | null; reasoning_content?: string | null };
		message?: { content?: string | null; reasoning_content?: string | null };
		finish_reason?: string | null;
	}[];
	timings?: Timings;
	id_slot?: number | null;
}

/** One chunk of a stream, or the whole of a reply when not streaming. Unknown shapes add nothing. */
export function addChunk(
	reply: ChatMessage,
	raw: unknown,
	now: number,
	track: { started: number; last: number }
): ChatMessage {
	if (typeof raw !== 'object' || raw === null) return reply;
	const chunk = raw as Chunk;
	const choice = chunk.choices?.[0];
	const part = choice?.delta ?? choice?.message;
	const content = part?.content ?? '';
	const reasoning = part?.reasoning_content ?? '';
	const next: ChatMessage = {
		...reply,
		raw,
		timings: chunk.timings ?? reply.timings,
		finish: choice?.finish_reason ?? reply.finish,
		slot: chunk.id_slot ?? reply.slot
	};
	if (!content && !reasoning) return next;

	next.ttftMs ??= now - track.started;
	if (reasoning) next.reasoning = (reply.reasoning ?? '') + reasoning;
	if (content) {
		if (reply.reasoning && !reply.text) next.thoughtMs = now - track.started - (next.ttftMs ?? 0);
		next.text = reply.text + content;
		next.chunks = [...(reply.chunks ?? []), content];
		next.gaps = [...(reply.gaps ?? []), now - track.last];
	}
	track.last = now;
	return next;
}

/** Tokens per second, from llama.cpp's timings once it has sent them, else counted here. */
export function speedOf(m: ChatMessage): number {
	if (m.timings?.predicted_per_second) return m.timings.predicted_per_second;
	const pieces = m.chunks?.length ?? 0;
	const spent = (m.gaps ?? []).slice(1).reduce((a, b) => a + b, 0);
	return pieces > 1 && spent > 0 ? ((pieces - 1) / spent) * 1000 : 0;
}

export const tokensOf = (m: ChatMessage) => m.timings?.predicted_n ?? m.chunks?.length ?? 0;

/** The share of drafted tokens the model kept, when it drafted any. */
export function draftsKept(m: ChatMessage | undefined): string | undefined {
	const t = m?.timings;
	if (!t?.draft_n) return undefined;
	const kept = t.draft_n_accepted ?? 0;
	return `${Math.round((kept / t.draft_n) * 100)}% · ${formatInt(kept)} / ${formatInt(t.draft_n)}`;
}

export function statsLine(m: ChatMessage): string {
	const parts: string[] = [];
	if (m.ttftMs !== undefined) parts.push(`TTFT ${formatMs(m.ttftMs)}`);
	parts.push(`${formatInt(tokensOf(m))} tokens`);
	const speed = speedOf(m);
	if (speed > 0) parts.push(`${speed.toFixed(1)} tok/s`);
	if (m.timings?.predicted_ms) parts.push(formatMs(m.timings.predicted_ms));
	const kept = draftsKept(m);
	if (kept) parts.push(`${kept.split(' ')[0]} of drafts kept`);
	if (m.stopped) parts.push('stopped');
	return parts.join(' · ');
}

export function timingsGrid(m: ChatMessage | undefined): { k: string; v: string }[] {
	const num = (n: number | undefined) => (n === undefined ? '—' : formatInt(n));
	const t = m?.timings;
	return [
		{ k: 'ttft', v: m?.ttftMs === undefined ? '—' : formatMs(m.ttftMs) },
		{ k: 'tok/s', v: m && speedOf(m) > 0 ? speedOf(m).toFixed(1) : '—' },
		{ k: 'prompt_n', v: num(t?.prompt_n) },
		{ k: 'cache_n', v: num(t?.cache_n) },
		{ k: 'predicted_n', v: num(t?.predicted_n) },
		{ k: 'predicted_ms', v: t?.predicted_ms === undefined ? '—' : formatInt(t.predicted_ms) },
		...(draftsKept(m) ? [{ k: 'drafts kept', v: draftsKept(m) ?? '' }] : []),
		{ k: 'slot', v: num(m?.slot) },
		{ k: 'finish', v: m?.finish ?? (m && !m.done ? '…' : '—') }
	];
}

export function cacheView(m: ChatMessage | undefined) {
	const cached = m?.timings?.cache_n ?? 0;
	const fresh = m?.timings?.prompt_n ?? 0;
	const total = cached + fresh;
	if (!m?.timings) {
		return {
			cached: 0,
			fresh: 0,
			note: 'Send a message to see how much of the prompt was cached.'
		};
	}
	return {
		cached: total ? (cached / total) * 100 : 0,
		fresh: total ? (fresh / total) * 100 : 0,
		note: cached
			? `${formatInt(cached)} of ${formatInt(total)} prompt tokens came from the cache, so only ${formatInt(fresh)} were processed.`
			: 'Nothing reused from the cache: the full prompt was processed.'
	};
}

const BARS = 60;
const SPIKE_FACTOR = 3;

/** The latest delays as bar heights, marking the ones far above the usual. */
export function delayBars(
	gaps: readonly number[] | undefined
): { height: number; spike: boolean }[] {
	const shown = (gaps ?? []).slice(1).slice(-BARS);
	if (shown.length === 0) return [];
	const sorted = shown.toSorted((a, b) => a - b);
	const median = sorted[Math.floor(sorted.length / 2)];
	// Even delays should read as low and steady, so the scale always leaves room for a spike.
	const top = Math.max(...shown, median * SPIKE_FACTOR) || 1;
	return shown.map((g) => ({
		height: Math.max(4, (g / top) * 100),
		spike: median > 0 && g > median * SPIKE_FACTOR
	}));
}
