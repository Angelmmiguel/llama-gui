import { describe, expect, it } from 'vitest';
import {
	addChunk,
	buildRequest,
	cacheView,
	curlFor,
	delayBars,
	emptySettings,
	SseParser,
	speedOf,
	statsLine,
	timingsGrid,
	withProfileDefaults,
	type ChatMessage
} from './chat';

const user: ChatMessage = { role: 'user', text: 'hi' };
const reply = (): ChatMessage => ({ role: 'assistant', text: '', chunks: [], gaps: [] });
const delta = (content: string) => ({ choices: [{ delta: { content } }] });

describe('the request', () => {
	it('leaves empty settings to the profile', () => {
		expect(buildRequest(emptySettings(), 'm', [user])).toEqual({
			model: 'm',
			messages: [{ role: 'user', content: 'hi' }],
			stream: true,
			cache_prompt: true
		});
	});

	it('sends what was set, as numbers, and ignores what is not one', () => {
		const body = buildRequest(
			{
				...emptySettings(),
				system: ' be brief ',
				temperature: '0.7',
				top_k: '20',
				top_p: 'lots',
				seed: '0',
				slot: '2',
				stream: false,
				cachePrompt: false
			},
			'm',
			[user]
		);
		expect(body.messages[0]).toEqual({ role: 'system', content: 'be brief' });
		expect(body).toMatchObject({
			temperature: 0.7,
			top_k: 20,
			seed: 0,
			id_slot: 2,
			stream: false,
			cache_prompt: false
		});
		expect(body.top_p).toBeUndefined();
	});

	it('leaves out replies that failed or never began', () => {
		const body = buildRequest(emptySettings(), 'm', [
			user,
			{ role: 'assistant', text: 'partial', error: 'boom' },
			reply()
		]);
		expect(body.messages).toHaveLength(1);
	});

	it('resets sampling but not the rest', () => {
		const got = withProfileDefaults({
			...emptySettings(),
			system: 's',
			temperature: '1',
			top_k: '3',
			stream: false
		});
		expect(got).toMatchObject({ system: 's', temperature: '', top_k: '', stream: false });
	});

	it('copies as a command that survives quotes', () => {
		const body = buildRequest(emptySettings(), 'm', [{ role: 'user', text: "it's" }]);
		const line = curlFor('http://tower:8080', body);
		expect(line).toContain('curl -N http://tower:8080/v1/chat/completions');
		expect(line).toContain("it'\\''s");
	});
});

describe('server-sent events', () => {
	it('copes with chunks cut anywhere', () => {
		const p = new SseParser();
		expect(p.feed('data: {"a"')).toEqual([]);
		expect(p.feed(':1}\n\ndata: [DO')).toEqual(['{"a":1}']);
		expect(p.feed('NE]\r\n\r\n')).toEqual(['[DONE]']);
	});

	it('ignores comments and other fields', () => {
		expect(new SseParser().feed(': ping\nevent: x\ndata: y\n')).toEqual(['y']);
	});
});

describe('adding chunks', () => {
	it('builds the text with first token time and the delay of each piece', () => {
		const track = { started: 100, last: 100 };
		let m = addChunk(reply(), delta('Hel'), 150, track);
		m = addChunk(m, delta('lo'), 170, track);
		expect(m).toMatchObject({ text: 'Hello', ttftMs: 50, chunks: ['Hel', 'lo'], gaps: [50, 20] });
	});

	it('takes the final chunk from a real capture', () => {
		const last = {
			choices: [{ finish_reason: 'length', index: 0, delta: {} }],
			timings: {
				cache_n: 0,
				prompt_n: 14,
				predicted_n: 24,
				predicted_ms: 147.4,
				predicted_per_second: 162.8
			}
		};
		const m = addChunk(reply(), last, 1, { started: 0, last: 0 });
		expect(m).toMatchObject({ finish: 'length', timings: { predicted_n: 24 }, raw: last });
		expect(speedOf(m)).toBe(162.8);
	});

	it('reads a whole reply when not streaming', () => {
		const m = addChunk(
			reply(),
			{ choices: [{ message: { content: 'All of it' }, finish_reason: 'stop' }] },
			40,
			{ started: 0, last: 0 }
		);
		expect(m).toMatchObject({ text: 'All of it', finish: 'stop', ttftMs: 40 });
	});

	it('times the reasoning before the answer', () => {
		const track = { started: 0, last: 0 };
		let m = addChunk(reply(), { choices: [{ delta: { reasoning_content: 'hmm' } }] }, 10, track);
		m = addChunk(m, delta('ok'), 1410, track);
		expect(m).toMatchObject({ reasoning: 'hmm', text: 'ok', thoughtMs: 1400 });
	});

	it('survives what it does not know', () => {
		const m = reply();
		expect(addChunk(m, null, 1, { started: 0, last: 0 })).toBe(m);
		expect(addChunk(m, { something: 'else' }, 1, { started: 0, last: 0 }).text).toBe('');
	});
});

describe('showing the numbers', () => {
	const finished: ChatMessage = {
		...reply(),
		text: 'x',
		ttftMs: 240,
		done: true,
		finish: 'stop',
		timings: {
			cache_n: 30,
			prompt_n: 10,
			predicted_n: 48,
			predicted_ms: 1000,
			predicted_per_second: 48
		}
	};

	it('sums up a reply', () => {
		expect(statsLine(finished)).toContain('48 tokens · 48.0 tok/s');
		expect(statsLine({ ...finished, stopped: true })).toContain('stopped');
	});

	it('counts speed here until the server says', () => {
		const live = { ...reply(), chunks: ['a', 'b', 'c'], gaps: [100, 50, 50] };
		expect(speedOf(live)).toBe(20);
		expect(speedOf(reply())).toBe(0);
	});

	it('fills the grid, with a dash for what it was not told', () => {
		const grid = Object.fromEntries(timingsGrid(finished).map((t) => [t.k, t.v]));
		expect(grid).toMatchObject({ prompt_n: '10', cache_n: '30', finish: 'stop', slot: '—' });
		expect(timingsGrid(undefined).every((t) => t.v === '—')).toBe(true);
		expect(Object.fromEntries(timingsGrid(reply()).map((t) => [t.k, t.v])).finish).toBe('…');
	});

	it('splits the prompt into cached and new', () => {
		expect(cacheView(finished)).toMatchObject({ cached: 75, fresh: 25 });
		expect(cacheView(finished).note).toContain('30 of 40');
		expect(cacheView({ ...finished, timings: { prompt_n: 5 } }).note).toContain('Nothing reused');
		expect(cacheView(undefined).note).toContain('Send a message');
	});

	it('draws delays without the wait for the first token, marking spikes', () => {
		const bars = delayBars([5000, 10, 10, 10, 100]);
		expect(bars).toHaveLength(4);
		expect(bars[3]).toEqual({ height: 100, spike: true });
		expect(bars[0].spike).toBe(false);
		expect(delayBars(undefined)).toEqual([]);
		expect(delayBars(Array.from({ length: 100 }, () => 1))).toHaveLength(60);
	});
});
