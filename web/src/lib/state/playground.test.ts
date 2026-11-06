import { describe, expect, it, vi } from 'vitest';
import { Playground } from './playground.svelte';

function sse(lines: string[]): Response {
	const encoder = new TextEncoder();
	return new Response(
		new ReadableStream({
			start(c) {
				for (const line of lines) c.enqueue(encoder.encode(`${line}\n\n`));
				c.close();
			}
		}),
		{ status: 200 }
	);
}

const delta = (content: string) => `data: ${JSON.stringify({ choices: [{ delta: { content } }] })}`;
const finished = `data: ${JSON.stringify({
	choices: [{ finish_reason: 'stop', delta: {} }],
	timings: { predicted_n: 2 }
})}`;

async function settled(p: Playground) {
	await vi.waitFor(() => expect(p.streaming).toBe(false));
}

describe('the playground', () => {
	it('streams a reply into the chat', async () => {
		const fetcher = vi.fn(async () => sse([delta('Hel'), delta('lo'), finished, 'data: [DONE]']));
		const p = new Playground(fetcher as unknown as typeof fetch);
		p.model = 'm';
		p.send('  hi  ');
		expect(p.streaming).toBe(true);
		await settled(p);
		expect(p.messages).toHaveLength(2);
		expect(p.messages[1]).toMatchObject({ text: 'Hello', finish: 'stop', done: true, model: 'm' });
		const [url, init] = fetcher.mock.calls[0] as unknown as [string, RequestInit];
		expect(url).toBe('/v1/chat/completions');
		expect(JSON.parse(init.body as string).messages).toEqual([{ role: 'user', content: 'hi' }]);
	});

	it('sends nothing without a model or a message, or over a running reply', async () => {
		const fetcher = vi.fn(async () => sse([finished]));
		const p = new Playground(fetcher as unknown as typeof fetch);
		p.send('hi');
		p.model = 'm';
		p.send('   ');
		expect(fetcher).not.toHaveBeenCalled();
		p.send('hi');
		p.send('again');
		await settled(p);
		expect(fetcher).toHaveBeenCalledTimes(1);
	});

	it("shows the server's reason for refusing", async () => {
		const fetcher = async () =>
			new Response(JSON.stringify({ error: { message: 'model is not loaded' } }), { status: 400 });
		const p = new Playground(fetcher as unknown as typeof fetch);
		p.model = 'm';
		p.send('hi');
		await settled(p);
		expect(p.messages[1]).toMatchObject({ error: 'model is not loaded', done: true });
	});

	it('copes with an error that is not JSON', async () => {
		const fetcher = async () => new Response('bad gateway', { status: 502 });
		const p = new Playground(fetcher as unknown as typeof fetch);
		p.model = 'm';
		p.send('hi');
		await settled(p);
		expect(p.messages[1].error).toBe('bad gateway');
	});

	it('keeps what arrived when stopped', async () => {
		const fetcher = (_: unknown, init: RequestInit) =>
			Promise.resolve(
				new Response(
					new ReadableStream({
						start(c) {
							c.enqueue(new TextEncoder().encode(`${delta('partial')}\n\n`));
							init.signal?.addEventListener('abort', () =>
								c.error(new DOMException('', 'AbortError'))
							);
						}
					})
				)
			);
		const p = new Playground(fetcher as unknown as typeof fetch);
		p.model = 'm';
		p.send('hi');
		await vi.waitFor(() => expect(p.messages[1].text).toBe('partial'));
		p.stop();
		await settled(p);
		expect(p.messages[1]).toMatchObject({ text: 'partial', stopped: true, done: true });
		expect(p.messages[1].error).toBeUndefined();
	});

	it('regenerates from a reply, and clears', async () => {
		const fetcher = vi.fn(async () => sse([delta('answer'), finished]));
		const p = new Playground(fetcher as unknown as typeof fetch);
		p.model = 'm';
		p.send('hi');
		await settled(p);
		p.regenerate(1);
		await settled(p);
		expect(fetcher).toHaveBeenCalledTimes(2);
		expect(p.messages).toHaveLength(2);
		const [, init] = fetcher.mock.calls[1] as unknown as [string, RequestInit];
		expect(JSON.parse(init.body as string).messages).toHaveLength(1);
		p.regenerate(0);
		expect(fetcher).toHaveBeenCalledTimes(2);
		p.clear();
		expect(p.messages).toEqual([]);
	});

	it('reads a whole reply when not streaming', async () => {
		const fetcher = async () =>
			new Response(
				JSON.stringify({ choices: [{ message: { content: 'done' }, finish_reason: 'stop' }] })
			);
		const p = new Playground(fetcher as unknown as typeof fetch);
		p.model = 'm';
		p.settings.stream = false;
		p.send('hi');
		await settled(p);
		expect(p.messages[1]).toMatchObject({ text: 'done', finish: 'stop' });
		expect(p.lastRequest?.stream).toBe(false);
	});
});
