import { describe, expect, it } from 'vitest';
import type { RequestDetail, RequestRecord, ScatterPoint } from '$lib/api/client';
import {
	apiFilters,
	curlCommand,
	filtersQuery,
	firstTokenMs,
	isCached,
	mergeNewest,
	messageText,
	msTick,
	niceMax,
	overlappingPrefills,
	parseFilters,
	scatterView,
	slowTokens,
	statusLevel
} from './view';

const base: RequestRecord = {
	id: 1,
	started_at: '2026-01-01T00:00:00.000Z',
	model: 'a',
	endpoint: '/v1/chat/completions',
	method: 'POST',
	status: 200,
	slot: 0,
	task_id: 1,
	ttft_ms: 100,
	total_ms: 1100,
	queue_ms: 10,
	prompt_n: 100,
	cache_n: 0,
	prompt_ms: 90,
	predicted_n: 40,
	predicted_ms: 1000,
	predicted_per_second: 40,
	stream: true,
	error: ''
};

describe('filters in the URL', () => {
	it('round-trips and leaves out defaults', () => {
		const f = parseFilters(new URLSearchParams('endpoint=chat&model=m&range=7d&id=9'));
		expect(f).toEqual({ kind: 'chat', model: 'm', range: '7d', id: 9 });
		expect(parseFilters(new URLSearchParams(filtersQuery(f)))).toEqual(f);
		expect(filtersQuery({ kind: 'all', model: '', range: '1h', id: null })).toBe('');
	});

	it('falls back on nonsense', () => {
		expect(parseFilters(new URLSearchParams('endpoint=toString&range=1y&id=x'))).toEqual({
			kind: 'all',
			model: '',
			range: '1h',
			id: null
		});
	});

	it('maps a kind to its paths and a range to a since', () => {
		const q = apiFilters({ kind: 'infill', model: '', range: '1h', id: null }, 3_600_000);
		expect(q).toEqual({ since: '1970-01-01T00:00:00.000Z', model: undefined, endpoint: '/infill' });
	});
});

describe('scales', () => {
	it('rounds up to a readable maximum', () => {
		expect(niceMax(0)).toBe(1);
		expect(niceMax(3400)).toBe(5000);
		expect(niceMax(1000)).toBe(1000);
		expect(niceMax(1800)).toBe(2000);
		expect(niceMax(0.3)).toBeCloseTo(0.5);
	});

	it('labels axes briefly', () => {
		expect(msTick(0)).toBe('0');
		expect(msTick(250)).toBe('250 ms');
		expect(msTick(1500)).toBe('1.5 s');
		expect(msTick(2000)).toBe('2 s');
	});
});

describe('first token', () => {
	it('takes TTFT when streamed and removes decode otherwise', () => {
		expect(firstTokenMs(base)).toBe(100);
		expect(firstTokenMs({ ...base, stream: false })).toBe(100);
		expect(firstTokenMs({ ...base, stream: false, predicted_ms: 5000 })).toBe(0);
	});

	it('calls a prompt cached when most of it was', () => {
		expect(isCached({ prompt_n: 10, cache_n: 90 })).toBe(true);
		expect(isCached({ prompt_n: 90, cache_n: 10 })).toBe(false);
		expect(isCached({ prompt_n: 0, cache_n: 0 })).toBe(false);
	});
});

describe('scatterView', () => {
	const point = (over: Partial<ScatterPoint>): ScatterPoint => ({ ...base, ...over });

	it('scales to the data and drops failures and untimed points', () => {
		const view = scatterView(
			[
				point({ id: 1, prompt_n: 500, cache_n: 0, ttft_ms: 100 }),
				point({ id: 2, prompt_n: 100, cache_n: 900, ttft_ms: 50 }),
				point({ id: 3, status: 400, prompt_n: 90000, ttft_ms: 0 }),
				point({ id: 4, ttft_ms: 0 })
			],
			null
		);
		expect(view.maxTokens).toBe(1000);
		expect(view.maxMs).toBe(100);
		expect(view.dots.map((d) => [d.id, d.x, d.y, d.cached])).toEqual([
			[1, 50, 100, false],
			[2, 100, 50, true]
		]);
		expect(view.selected).toBeNull();
	});

	it('bounds the dots and keeps the selected request in range', () => {
		const many = Array.from({ length: 700 }, (_, i) => point({ id: i, prompt_n: 10, ttft_ms: 10 }));
		const view = scatterView(many, { ...base, prompt_n: 40, ttft_ms: 20, gaps: [], body: null });
		expect(view.dots).toHaveLength(500);
		expect(view.maxMs).toBe(20);
		expect(view.selected).toEqual({ x: 80, y: 100 });
	});
});

describe('slow tokens', () => {
	it('flags only gaps over 3× the median', () => {
		expect(slowTokens([10, 10, 10, 30, 31])).toEqual([4]);
		expect(slowTokens([])).toEqual([]);
		expect(slowTokens([0, 0, 5])).toEqual([]);
	});

	const detail: RequestDetail = { ...base, gaps: [10, 10, 100, 10], body: null };
	const other = (over: Partial<RequestRecord>) => ({ ...base, id: 2, queue_ms: 0, ...over });
	// The slow gap runs from 120 to 220 ms after the start.

	it('finds a prefill overlapping a slow gap', () => {
		const at = (ms: number) => new Date(Date.parse(base.started_at) + ms).toISOString();
		const during = other({ started_at: at(150), prompt_ms: 10 });
		const before = other({ id: 3, started_at: at(0), prompt_ms: 100 });
		const self = { ...during, id: 1 };
		const noPrefill = other({ id: 4, started_at: at(150), prompt_ms: 0 });
		expect(overlappingPrefills(detail, [2], [during, before, self, noPrefill])).toEqual([2]);
		expect(overlappingPrefills(detail, [], [during])).toEqual([]);
	});
});

describe('curlCommand', () => {
	it('quotes for the shell', () => {
		expect(curlCommand(base, `{"a":"it's"}`, 'http://h')).toBe(
			`curl 'http://h/v1/chat/completions' \\\n  -H 'Content-Type: application/json' \\\n  --data-raw '{"a":"it'\\''s"}'`
		);
	});
});

describe('messageText', () => {
	it('reads chat messages and a JSON answer', () => {
		const request = JSON.stringify({ messages: [{ role: 'user', content: 'hi' }] });
		const response = JSON.stringify({ choices: [{ message: { content: 'hello' } }] });
		expect(messageText({ request, response })).toBe('user: hi\nassistant: hello');
	});

	it('joins a streamed answer', () => {
		const chunk = (c: string) =>
			`data: ${JSON.stringify({ choices: [{ delta: { content: c } }] })}`;
		const response = [chunk('a'), '', chunk('b'), 'data: [DONE]'].join('\n');
		expect(messageText({ request: '{"prompt":"p"}', response })).toBe('prompt: p\nassistant: ab');
	});

	it('shows anything else as it is', () => {
		expect(messageText({ request: 'not json', response: '' })).toBe('not json');
	});
});

describe('mergeNewest', () => {
	const rows = (...ids: number[]) => ids.map((id) => ({ ...base, id }));

	it('puts new rows on top', () => {
		const { rows: merged, reset } = mergeNewest(rows(3, 2, 1), rows(5, 4, 3));
		expect(merged.map((r) => r.id)).toEqual([5, 4, 3, 2, 1]);
		expect(reset).toBe(false);
	});

	it('resets when a gap would open', () => {
		const { rows: merged, reset } = mergeNewest(rows(2, 1), rows(9, 8));
		expect(merged.map((r) => r.id)).toEqual([9, 8]);
		expect(reset).toBe(true);
	});
});

describe('statusLevel', () => {
	it('grades codes and classified errors', () => {
		expect(statusLevel('200')).toBe('ok');
		expect(statusLevel('200 cancelled')).toBe('warn');
		expect(statusLevel('400 ctx')).toBe('warn');
		expect(statusLevel('503 busy')).toBe('error');
	});
});
