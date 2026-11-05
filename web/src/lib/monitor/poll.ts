/**
 * Runs `fn` now and again `ms` after each run ends, so a slow server never sees overlapping
 * calls, until the returned function is called. The signal tells a late answer to be dropped.
 */
export function every(ms: number, fn: (signal: AbortSignal) => Promise<void>): () => void {
	const controller = new AbortController();
	let timer: ReturnType<typeof setTimeout>;
	const run = async () => {
		await fn(controller.signal);
		if (!controller.signal.aborted) timer = setTimeout(run, ms);
	};
	void run();
	return () => {
		controller.abort();
		clearTimeout(timer);
	};
}

/** What to show for a caught value. */
export function errorText(e: unknown): string {
	return e instanceof Error ? e.message : String(e);
}
