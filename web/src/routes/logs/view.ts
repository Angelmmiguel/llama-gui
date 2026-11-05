import type { LogFilters, LogRecord, ServerEvent } from '$lib/api/client';

/** The newest lines kept in memory: rows are DOM nodes, and a noisy router is fast. */
export const MAX_LINES = 2000;
/** Sources always offered; others join the toggles as they appear. */
export const KNOWN_SOURCES = ['srv', 'slot', 'router', 'ggml'] as const;

export type LevelChoice = 'all' | 'warn' | 'error';

const LEVELS: Record<LevelChoice, readonly string[] | null> = {
	all: null,
	warn: ['warn', 'error'],
	error: ['error']
};

export interface LogView {
	q: string;
	level: LevelChoice;
	/** Sources toggled off. Off rather than on, so an unseen source is not hidden by default. */
	off: readonly string[];
}

/** The filters as `/logs` and `/logs/download` take them. */
export function logQuery(view: LogView, known: readonly string[]): LogFilters {
	const on = known.filter((s) => !view.off.includes(s));
	return {
		q: view.q.trim() || undefined,
		level: LEVELS[view.level]?.join(','),
		source: view.off.length > 0 ? on.join(',') : undefined
	};
}

/** The same filter as the server's, for lines arriving on the live tail. */
export function matchesView(view: LogView, line: LogRecord): boolean {
	const levels = LEVELS[view.level];
	if (levels && !levels.includes(line.level)) return false;
	if (view.off.includes(line.source)) return false;
	const q = view.q.trim().toLowerCase();
	if (!q) return true;
	return line.message.toLowerCase().includes(q) || (line.raw ?? '').toLowerCase().includes(q);
}

/** Start of the local day containing `now`, in ISO. */
export function localMidnight(now: number): string {
	const d = new Date(now);
	d.setHours(0, 0, 0, 0);
	return d.toISOString();
}

/** Appends and drops the oldest past the cap. */
export function appendCapped<T>(lines: readonly T[], more: readonly T[], cap = MAX_LINES): T[] {
	const all = lines.concat(more);
	return all.length > cap ? all.slice(all.length - cap) : all;
}

/** Whether the viewer counts as following: at the bottom, give or take a sliver. */
export function atBottom(scrollTop: number, clientHeight: number, scrollHeight: number): boolean {
	return scrollHeight - scrollTop - clientHeight <= 24;
}

/* eslint-disable-next-line no-control-regex -- control characters are the point. */
const ANSI = /\u001b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\u0007\u001b]*(?:\u0007|\u001b\\)?|[@-Z\\^_])/g;
/* eslint-disable-next-line no-control-regex -- control characters are the point. */
const CONTROL = /[\u0000-\u0008\u000b-\u001f\u007f]/g;

/** Log text without escape sequences or control characters, keeping tabs and newlines. */
export function cleanText(text: string): string {
	return text.replace(ANSI, '').replace(CONTROL, '');
}

/** What a row shows: the parsed message, or the raw line when nothing parsed. */
export function lineText(line: LogRecord): string {
	return cleanText(line.message || line.raw || '');
}

export interface EventGroup {
	/** Id of the newest event in the group. */
	id: number;
	severity: string;
	title: string;
	count: number;
	newest: string;
	oldest: string;
	meta: string;
}

/** Short meta values only: a build's path says nothing a timeline needs. */
function metaText(meta: ServerEvent['meta']): string {
	return Object.values(meta ?? {})
		.filter((v) => typeof v === 'string' && v.length <= 32)
		.join(' · ');
}

/** Folds neighbouring repeats of an event, given newest first. */
export function groupEvents(events: readonly ServerEvent[]): EventGroup[] {
	const groups: EventGroup[] = [];
	for (const e of events) {
		const meta = metaText(e.meta);
		const last = groups.at(-1);
		if (last && last.title === e.title && last.severity === e.severity && last.meta === meta) {
			last.count++;
			last.oldest = e.ts;
		} else {
			groups.push({
				id: e.id,
				severity: e.severity,
				title: e.title,
				count: 1,
				newest: e.ts,
				oldest: e.ts,
				meta
			});
		}
	}
	return groups;
}
