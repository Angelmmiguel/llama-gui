import { getStatus, type ServerStatus } from '$lib/api/client';

const POLL_MS = 5000;
const TIMEOUT_MS = 4000;

/** Server status polled from `/status`. Failures show as `unreachable`, never as an exception. */
export class ServerState {
	/** The last good response; kept while unreachable. */
	status = $state<ServerStatus | null>(null);
	/** Whether the last poll failed. */
	unreachable = $state(false);
	/** Counts up between polls so the clock in the nav ticks; null until known or when lost. */
	uptimeSeconds = $state<number | null>(null);

	readonly #fetch: (signal: AbortSignal) => Promise<ServerStatus>;

	constructor(fetchStatus: (signal: AbortSignal) => Promise<ServerStatus> = getStatus) {
		this.#fetch = fetchStatus;
	}

	/** Router state, or the client's own `connecting` and `unreachable`. */
	get state(): string {
		if (this.unreachable) return 'unreachable';
		return this.status?.state || 'connecting';
	}

	/** Fetches once now. Exposed for an immediate refresh after a server action. */
	async poll(): Promise<void> {
		try {
			const status = await this.#fetch(AbortSignal.timeout(TIMEOUT_MS));
			this.status = status;
			this.unreachable = false;
			this.uptimeSeconds = status.uptime_seconds;
		} catch {
			this.unreachable = true;
			this.uptimeSeconds = null;
		}
	}

	/** Polls until the returned function is called. */
	start(): () => void {
		let stopped = false;
		let timer: ReturnType<typeof setTimeout>;
		// Chained rather than an interval, so a slow server never sees overlapping polls.
		const loop = async () => {
			await this.poll();
			if (!stopped) timer = setTimeout(loop, POLL_MS);
		};
		void loop();
		const ticker = setInterval(() => {
			if (this.uptimeSeconds !== null) this.uptimeSeconds += 1;
		}, 1000);
		return () => {
			stopped = true;
			clearTimeout(timer);
			clearInterval(ticker);
		};
	}
}

export const server = new ServerState();
