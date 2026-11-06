import type { ServerStatus } from '$lib/api/client';

export interface Banner {
	title: string;
	detail?: string;
	/** Restarts so far, shown beside the detail. */
	note?: string;
}

/**
 * What every page has to say about the service. A router retrying after a crash
 * passes through `starting`, and a poll landing there must not make the banner flicker.
 */
export function serverBanner(status: ServerStatus | null, unreachable: boolean): Banner | null {
	if (unreachable) {
		return {
			title: 'Cannot reach llama-gui',
			detail: 'What this page shows may be out of date. Retrying every few seconds.'
		};
	}
	if (!status) return null;
	const restarts = status.restart_count ?? 0;
	const retrying = status.state === 'starting' && restarts > 0 && Boolean(status.last_error);
	if (status.state !== 'crashed' && !retrying) return null;
	return {
		title: retrying ? 'The router crashed and is starting again' : 'The router crashed',
		detail: status.last_error,
		note: restarts > 0 ? `${restarts} restart${restarts === 1 ? '' : 's'} so far` : undefined
	};
}
