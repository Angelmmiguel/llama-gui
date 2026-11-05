export const MAP_CELLS = 192;

export type MapCell = 'cached' | 'prompt' | 'generated' | 'empty';

/**
 * The context map of a slot: cells in token order, each the kind of token at its middle.
 * Strings, so a cell whose kind did not change costs Svelte nothing at 1 Hz.
 */
export function contextCells(
	fill: { cached: number; prompt: number; generated: number },
	nCtx: number,
	cells = MAP_CELLS
): MapCell[] {
	const per = nCtx > 0 ? nCtx / cells : 0;
	const { cached, prompt, generated } = fill;
	return Array.from({ length: cells }, (_, i) => {
		const token = i * per + per / 2;
		if (per === 0) return 'empty';
		if (token < cached) return 'cached';
		if (token < cached + prompt) return 'prompt';
		if (token < cached + prompt + generated) return 'generated';
		return 'empty';
	});
}

/** Index of the cell holding the newest token, for the decode caret; -1 when empty. */
export function headCell(used: number, nCtx: number, cells = MAP_CELLS): number {
	if (used <= 0 || nCtx <= 0) return -1;
	return Math.min(cells - 1, Math.floor((used - 1) / (nCtx / cells)));
}

export const HEATMAP_CELLS = 90;

export interface HeatCell {
	/** The second this cell is, so a tick moves one node per lane instead of rewriting all. */
	key: number;
	state: 'prefill' | 'decode' | 'idle';
	opacity: number;
}

/** Brightness of a decode cell: never so dim it reads as idle, in steps that spare DOM writes. */
function decodeOpacity(speed: number, peak: number): number {
	const share = peak > 0 ? Math.min(1, speed / peak) : 1;
	return Math.round((0.45 + share * 0.55) * 20) / 20;
}

/** The fastest decode across lanes, the scale all of them share. */
export function peakSpeed(
	lanes: readonly { samples: readonly { state: string; speed: number }[] }[]
): number {
	let peak = 0;
	for (const lane of lanes) {
		for (const s of lane.samples) {
			if (s.state === 'decode' && s.speed > peak) peak = s.speed;
		}
	}
	return peak;
}

/**
 * Always `HEATMAP_CELLS` cells ending at `tick`, the current second: what the sampler has
 * not seen yet is idle on the left. Keys count seconds so the row scrolls.
 */
export function heatCells(
	samples: readonly { state: string; speed: number }[],
	tick: number,
	peak: number
): HeatCell[] {
	const recent = samples.slice(-HEATMAP_CELLS);
	const pad = HEATMAP_CELLS - recent.length;
	return Array.from({ length: HEATMAP_CELLS }, (_, i) => {
		const key = tick - HEATMAP_CELLS + 1 + i;
		const s = i < pad ? undefined : recent[i - pad];
		if (s?.state === 'prefill') return { key, state: 'prefill', opacity: 1 };
		if (s?.state === 'decode') {
			return { key, state: 'decode', opacity: decodeOpacity(s.speed, peak) };
		}
		return { key, state: 'idle', opacity: 1 };
	});
}

/** Share of the seconds seen across lanes with a slot at work, 0 to 100. */
export function busyPercent(lanes: readonly { samples: readonly { state: string }[] }[]): number {
	let busy = 0;
	let total = 0;
	for (const lane of lanes) {
		for (const s of lane.samples.slice(-HEATMAP_CELLS)) {
			total++;
			if (s.state !== 'idle') busy++;
		}
	}
	return total > 0 ? Math.round((busy / total) * 100) : 0;
}

const SAMPLING: [string, string][] = [
	['temperature', 'temp'],
	['top_p', 'top_p'],
	['top_k', 'top_k'],
	['min_p', 'min_p'],
	['repeat_penalty', 'repeat_penalty'],
	['n_predict', 'n_predict'],
	['max_tokens', 'max_tokens']
];

/** The params worth showing, under the design's names. Floats lose their float32 noise. */
export function samplingParams(
	params: Record<string, unknown> | undefined
): { k: string; v: string }[] {
	const out: { k: string; v: string }[] = [];
	for (const [key, label] of SAMPLING) {
		const v = params?.[key];
		if (typeof v === 'number') out.push({ k: label, v: String(Number(v.toFixed(3))) });
		else if (typeof v === 'string' || typeof v === 'boolean') out.push({ k: label, v: String(v) });
	}
	return out;
}

/** Save and restore share one predictable file per slot; the API takes a single path element. */
export function slotFilename(model: string, slot: number): string {
	const name = model.replace(/[^A-Za-z0-9._-]/g, '-').replace(/^[^A-Za-z0-9]+/, '');
	return `${name.slice(0, 100) || 'model'}-slot${slot}.bin`;
}

const PROMPT_LIMIT = 4000;

function textOf(content: unknown): string {
	if (typeof content === 'string') return content;
	if (Array.isArray(content)) {
		return content
			.map((part: unknown) =>
				part && typeof part === 'object' && 'text' in part ? String(part.text) : '[attachment]'
			)
			.join(' ');
	}
	return '';
}

/**
 * The prompt out of a captured request body, as `role: text` lines. A body cut short by the
 * capture limit is not valid JSON, so it is shown as it is.
 */
export function promptText(body: string): string {
	let parsed: unknown;
	try {
		parsed = JSON.parse(body);
	} catch {
		return body.slice(0, PROMPT_LIMIT);
	}
	if (!parsed || typeof parsed !== 'object') return body.slice(0, PROMPT_LIMIT);
	const o = parsed as Record<string, unknown>;
	let text: string;
	if (Array.isArray(o.messages)) {
		text = (o.messages as { role?: unknown; content?: unknown }[])
			.map((m) => `${String(m.role ?? 'message')}: ${textOf(m.content)}`)
			.join('\n');
	} else if (o.prompt !== undefined) {
		text = textOf(o.prompt);
	} else if (o.input_prefix !== undefined) {
		text = `${textOf(o.input_prefix)}…${textOf(o.input_suffix)}`;
	} else if (o.input !== undefined) {
		text = textOf(o.input);
	} else {
		text = body;
	}
	return text.length > PROMPT_LIMIT ? `${text.slice(0, PROMPT_LIMIT)}…` : text;
}
