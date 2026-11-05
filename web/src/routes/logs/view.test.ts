import { describe, expect, it } from 'vitest';
import type { LogRecord, ServerEvent } from '$lib/api/client';
import {
	appendCapped,
	atBottom,
	cleanText,
	groupEvents,
	lineText,
	logQuery,
	matchesView,
	type LogView
} from './view';

const line = (over: Partial<LogRecord> = {}): LogRecord => ({
	id: 1,
	ts: '2026-01-01T00:00:00Z',
	level: 'info',
	source: 'slot',
	model: 'm',
	message: 'Hello World',
	raw: 'raw text',
	...over
});
const view = (over: Partial<LogView> = {}): LogView => ({ q: '', level: 'all', off: [], ...over });

describe('logQuery', () => {
	const known = ['srv', 'slot', 'cmn'];

	it('sends nothing for the default view', () => {
		expect(logQuery(view(), known)).toEqual({ q: undefined, level: undefined, source: undefined });
	});

	it('expands Warn+ and lists the sources still on', () => {
		expect(logQuery(view({ q: ' x ', level: 'warn', off: ['slot'] }), known)).toEqual({
			q: 'x',
			level: 'warn,error',
			source: 'srv,cmn'
		});
	});
});

describe('matchesView', () => {
	it('applies level, source and text like the server', () => {
		expect(matchesView(view(), line())).toBe(true);
		expect(matchesView(view({ level: 'warn' }), line())).toBe(false);
		expect(matchesView(view({ level: 'warn' }), line({ level: 'error' }))).toBe(true);
		expect(matchesView(view({ level: 'error' }), line({ level: 'warn' }))).toBe(false);
		expect(matchesView(view({ off: ['slot'] }), line())).toBe(false);
		expect(matchesView(view({ q: 'hello' }), line())).toBe(true);
		expect(matchesView(view({ q: 'RAW' }), line())).toBe(true);
		expect(matchesView(view({ q: 'nope' }), line())).toBe(false);
	});

	it('shows a source nobody has toggled off', () => {
		expect(matchesView(view({ off: ['srv'] }), line({ source: 'new' }))).toBe(true);
	});
});

describe('appendCapped', () => {
	it('keeps the newest', () => {
		expect(appendCapped([1, 2, 3], [4, 5], 4)).toEqual([2, 3, 4, 5]);
		expect(appendCapped([1], [2], 4)).toEqual([1, 2]);
	});
});

describe('atBottom', () => {
	it('allows a sliver and notices scrolling up', () => {
		expect(atBottom(900, 100, 1000)).toBe(true);
		expect(atBottom(880, 100, 1000)).toBe(true);
		expect(atBottom(700, 100, 1000)).toBe(false);
	});
});

describe('cleanText', () => {
	it('strips escape sequences and control characters', () => {
		expect(cleanText('\u001b[31mred\u001b[0m')).toBe('red');
		expect(cleanText('\u001b]0;title\u0007after')).toBe('after');
		expect(cleanText('a\u0000b\u0007c\td\ne')).toBe('abc\td\ne');
	});

	it('leaves markup as text', () => {
		expect(cleanText('<img src=x onerror=alert(1)>')).toBe('<img src=x onerror=alert(1)>');
	});

	it('falls back to the raw line', () => {
		expect(lineText(line({ message: '', raw: 'only raw' }))).toBe('only raw');
	});
});

describe('groupEvents', () => {
	const event = (id: number, title: string, meta?: Record<string, string>): ServerEvent => ({
		id,
		ts: `2026-01-01T00:00:0${id}Z`,
		kind: 'k',
		severity: 'warn',
		title,
		meta
	});

	it('folds only neighbouring repeats', () => {
		const groups = groupEvents([event(4, 'a'), event(3, 'a'), event(2, 'b'), event(1, 'a')]);
		expect(groups.map((g) => [g.id, g.title, g.count])).toEqual([
			[4, 'a', 2],
			[2, 'b', 1],
			[1, 'a', 1]
		]);
		expect(groups[0].oldest).toBe('2026-01-01T00:00:03Z');
	});

	it('keeps different models apart and drops long meta', () => {
		const groups = groupEvents([
			event(2, 'a', { model: 'x' }),
			event(1, 'a', { model: 'y', build: '/a/very/long/path/to/some/build/llama-server' })
		]);
		expect(groups.map((g) => g.meta)).toEqual(['x', 'y']);
	});
});
