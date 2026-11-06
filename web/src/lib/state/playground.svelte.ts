import {
	addChunk,
	buildRequest,
	emptySettings,
	SseParser,
	type ChatMessage,
	type ChatRequest,
	type Settings
} from '$lib/playground/chat';

const ENDPOINT = '/v1/chat/completions';

async function errorOf(res: Response): Promise<string> {
	const text = await res.text().catch(() => '');
	try {
		const body = JSON.parse(text) as { error?: string | { message?: string } };
		const message = typeof body.error === 'string' ? body.error : body.error?.message;
		if (message) return message;
	} catch {
		// Not JSON, such as a proxy's error page.
	}
	return text.slice(0, 200) || `The server answered ${res.status}.`;
}

/**
 * One chat, kept here so it survives moving to another page. It stays in memory: a prompt is never
 * stored or logged by the dashboard itself.
 */
export class Playground {
	// Raw: a reply changes with every token, so replacing it beats deep proxies over its token list.
	messages = $state.raw<ChatMessage[]>([]);
	settings = $state<Settings>(emptySettings());
	draft = $state('');
	model = $state('');
	lastRequest = $state.raw<ChatRequest | null>(null);
	streaming = $state(false);

	readonly #fetch: typeof fetch;
	readonly #now: () => number;
	#controller: AbortController | undefined;

	constructor(
		fetcher: typeof fetch = (...args) => fetch(...args),
		now: () => number = () => performance.now()
	) {
		this.#fetch = fetcher;
		this.#now = now;
	}

	send(text = this.draft) {
		const content = text.trim();
		if (!content || this.streaming || !this.model) return;
		this.draft = '';
		this.messages = [...this.messages, { role: 'user', text: content }];
		void this.#ask();
	}

	/** Asks again for the reply at this position, dropping it and whatever came after. */
	regenerate(index: number) {
		if (this.streaming || this.messages[index]?.role !== 'assistant') return;
		this.messages = this.messages.slice(0, index);
		void this.#ask();
	}

	/** What arrived so far stays: a stopped reply is still worth reading. */
	stop() {
		this.#controller?.abort();
	}

	clear() {
		if (!this.streaming) this.messages = [];
	}

	#update(change: (reply: ChatMessage) => ChatMessage) {
		const last = this.messages.length - 1;
		this.messages = this.messages.with(last, change(this.messages[last]));
	}

	async #ask() {
		const body = buildRequest($state.snapshot(this.settings), this.model, this.messages);
		this.lastRequest = body;
		this.messages = [
			...this.messages,
			{ role: 'assistant', model: this.model, text: '', reasoning: '', chunks: [], gaps: [] }
		];
		this.streaming = true;
		const controller = new AbortController();
		this.#controller = controller;
		const started = this.#now();
		const track = { started, last: started };
		try {
			const res = await this.#fetch(ENDPOINT, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(body),
				signal: controller.signal
			});
			if (!res.ok) throw new Error(await errorOf(res));
			if (body.stream && res.body) {
				await this.#readStream(res.body, track);
			} else {
				const chunk: unknown = await res.json();
				this.#update((reply) => addChunk(reply, chunk, this.#now(), track));
			}
		} catch (e) {
			if (!controller.signal.aborted) {
				const error = e instanceof Error ? e.message : String(e);
				this.#update((reply) => ({ ...reply, error }));
			}
		} finally {
			this.#update((reply) => ({ ...reply, done: true, stopped: controller.signal.aborted }));
			this.streaming = false;
			this.#controller = undefined;
		}
	}

	async #readStream(stream: ReadableStream<Uint8Array>, track: { started: number; last: number }) {
		const reader = stream.getReader();
		const decoder = new TextDecoder();
		const parser = new SseParser();
		for (;;) {
			const { done, value } = await reader.read();
			if (done) return;
			for (const data of parser.feed(decoder.decode(value, { stream: true }))) {
				if (data === '[DONE]') return;
				let chunk: unknown;
				try {
					chunk = JSON.parse(data);
				} catch {
					continue;
				}
				this.#update((reply) => addChunk(reply, chunk, this.#now(), track));
			}
		}
	}
}

export const playground = new Playground();
