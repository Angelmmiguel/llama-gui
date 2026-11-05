import {
	stream,
	TELEMETRY_STREAM,
	type StreamOptions,
	type TelemetrySnapshot
} from '$lib/api/client';

type Connect = (
	path: string,
	onMessage: (snapshot: TelemetrySnapshot) => void,
	options: StreamOptions
) => () => void;

/**
 * The latest snapshot from the 1 Hz telemetry stream. Pages share one connection:
 * call `subscribe()` from an `$effect`, whose cleanup is the function it returns.
 * While the route is missing it stays disconnected and keeps retrying quietly.
 */
export class Telemetry {
	latest = $state<TelemetrySnapshot | null>(null);
	/** Bind to LiveToggle. Paused keeps the connection but freezes `latest`. */
	live = $state(true);
	connected = $state(false);

	readonly #connect: Connect;
	#subscribers = 0;
	#close: (() => void) | undefined;

	constructor(connect: Connect = stream) {
		this.#connect = connect;
	}

	subscribe(): () => void {
		if (this.#subscribers++ === 0) {
			this.#close = this.#connect(
				TELEMETRY_STREAM,
				(snapshot) => {
					if (this.live) this.latest = snapshot;
				},
				{ onStatus: (connected) => (this.connected = connected) }
			);
		}
		let released = false;
		return () => {
			if (released) return;
			released = true;
			if (--this.#subscribers === 0) {
				this.#close?.();
				this.#close = undefined;
				this.connected = false;
			}
		};
	}
}

export const telemetry = new Telemetry();
