// Mirrors the /api/v1 routes in docs/ARCHITECTURE.md one to one. Types follow the
// Go JSON tags where those exist. Unknown fields are always possible, so nothing
// here should be treated as exhaustive.

const BASE = '/api/v1';

export class ApiError extends Error {
	readonly status: number;

	constructor(status: number, message: string) {
		super(message);
		this.name = 'ApiError';
		this.status = status;
	}
}

type Query = Record<string, string | number | boolean | undefined>;

function url(path: string, query?: Query): string {
	const params = new URLSearchParams();
	for (const [k, v] of Object.entries(query ?? {})) {
		if (v !== undefined && v !== '') params.set(k, String(v));
	}
	const qs = params.toString();
	return `${BASE}${path}${qs ? `?${qs}` : ''}`;
}

async function request<T>(
	method: string,
	path: string,
	options: { body?: unknown; query?: Query; signal?: AbortSignal } = {}
): Promise<T> {
	const res = await fetch(url(path, options.query), {
		method,
		signal: options.signal,
		headers: options.body === undefined ? undefined : { 'Content-Type': 'application/json' },
		body: options.body === undefined ? undefined : JSON.stringify(options.body)
	});
	const text = await res.text();
	if (!res.ok) {
		throw new ApiError(res.status, errorMessage(text) ?? (res.statusText || `HTTP ${res.status}`));
	}
	return (text ? JSON.parse(text) : undefined) as T;
}

function errorMessage(body: string): string | undefined {
	try {
		const parsed: unknown = JSON.parse(body);
		if (parsed && typeof parsed === 'object' && 'error' in parsed) {
			return String(parsed.error);
		}
	} catch {
		// Not JSON, such as a proxy's error page; the caller falls back to the status text.
	}
	return undefined;
}

export interface ServerStatus {
	/** stopped, starting, running, stopping or crashed; tolerate others. */
	state: string;
	version: string;
	uptime_seconds: number;
	/** meta or bodies. */
	capture: string;
	active_build?: string;
}

export interface Gpu {
	index: number;
	name: string;
	uuid: string;
	driver_version: string;
	memory_total: number | null;
	memory_used: number | null;
	utilization: number | null;
	temperature: number | null;
}

export interface Hardware {
	gpus: Gpu[];
	cuda_version: string;
	render_nodes: { path: string; vendor: string; vendor_id: string }[];
	cpu: { model: string; cores: number; threads: number; features: string[] };
	memory: { total: number; available: number };
	disks: { path: string; total: number; free: number; error?: string }[];
	warnings?: string[];
}

export interface ReleaseAsset {
	name: string;
	size: number;
	url: string;
	digest: string;
}

export interface BuildManifest {
	id: string;
	tag: string;
	release_tag: string;
	channel: string;
	backend: string;
	variant?: string;
	arch: string;
	installed_at: string;
	assets: ReleaseAsset[];
	probe: {
		version: string;
		build: string;
		commit: string;
		version_output: string;
		flags: string[];
		router: boolean;
		devices: string[];
	};
	active?: boolean;
}

export interface AvailableRelease {
	tag: string;
	binary_tag: string;
	channel: string;
	published_at: string;
	body: string;
	assets: ReleaseAsset[];
	[key: string]: unknown;
}

export interface InstallJob {
	id: string;
	version: string;
	backend: string;
	state: string;
	step: string;
	done: number;
	total: number;
	bytes_per_sec: number;
	error?: string;
	started_at: string;
	finished_at?: string;
}

export interface Profile {
	id: string;
	name: string;
	alias: string;
	model_file: string;
	mmproj?: string;
	jinja?: boolean;
	mlock?: boolean;
	ctx_size: number;
	parallel?: number;
	ngl?: string;
	split_mode?: string;
	tensor_split?: string;
	flash_attn?: string;
	cache_type_k?: string;
	cache_type_v?: string;
	cache_reuse?: number;
	batch?: number;
	ubatch?: number;
	threads?: number;
	temp?: number;
	top_p?: number;
	top_k?: number;
	min_p?: number;
	repeat_penalty?: number;
	load_at_start?: boolean;
	idle_unload_seconds?: number;
	on_demand?: boolean;
	extra_args?: string[];
}

export interface RouterModel {
	id: string;
	status?: { value?: string; exit_code?: number; args?: string[] };
	[key: string]: unknown;
}

export interface Slot {
	id: number;
	model?: string;
	n_ctx: number;
	is_processing: boolean;
	id_task: number | null;
	n_prompt_tokens: number;
	n_prompt_tokens_processed: number;
	n_prompt_tokens_cache: number;
	params?: Record<string, unknown>;
	next_token?: { n_decoded: number }[];
	[key: string]: unknown;
}

export type SlotAction = 'save' | 'restore' | 'erase' | 'cancel';

export interface RequestFilters extends Query {
	model?: string;
	endpoint?: string;
	status?: string;
	since?: string;
	limit?: number;
}

export type RequestRecord = Record<string, unknown> & { id: string | number };
export type RequestSummary = Record<string, unknown>;

export interface LogFilters extends Query {
	q?: string;
	level?: string;
	source?: string;
	limit?: number;
}

export type LogRecord = Record<string, unknown>;
export type ServerEvent = Record<string, unknown>;
export type TelemetrySnapshot = Record<string, unknown>;

const enc = encodeURIComponent;

export const healthz = () => request<unknown>('GET', '/healthz');
export const getStatus = (signal?: AbortSignal) =>
	request<ServerStatus>('GET', '/status', { signal });
export const getHardware = () => request<Hardware>('GET', '/hardware');

export const listBuilds = () => request<BuildManifest[]>('GET', '/builds');
export const listAvailableBuilds = (channel?: string) =>
	request<AvailableRelease[]>('GET', '/builds/available', { query: { channel } });
export const installBuild = (body: { version: string; backend: string }) =>
	request<InstallJob>('POST', '/builds/install', { body });
export const listBuildJobs = () => request<InstallJob[]>('GET', '/builds/jobs');
export const activateBuild = (id: string) =>
	request<unknown>('POST', `/builds/${enc(id)}/activate`);
export const deleteBuild = (id: string) => request<unknown>('DELETE', `/builds/${enc(id)}`);

export const listProfiles = () => request<Profile[]>('GET', '/profiles');
export const createProfile = (body: Omit<Profile, 'id'> & { id?: string }) =>
	request<Profile>('POST', '/profiles', { body });
export const getProfile = (id: string) => request<Profile>('GET', `/profiles/${enc(id)}`);
export const updateProfile = (id: string, body: Profile) =>
	request<Profile>('PUT', `/profiles/${enc(id)}`, { body });
export const deleteProfile = (id: string) => request<unknown>('DELETE', `/profiles/${enc(id)}`);

export const listModels = () => request<RouterModel[]>('GET', '/models');
export const loadModel = (id: string) => request<unknown>('POST', `/models/${enc(id)}/load`);
export const unloadModel = (id: string) => request<unknown>('POST', `/models/${enc(id)}/unload`);

export const listSlots = () => request<Slot[]>('GET', '/slots');
export const slotAction = (model: string, slot: number, action: SlotAction) =>
	request<unknown>('POST', `/slots/${enc(model)}/${slot}/${action}`);

export const listRequests = (query?: RequestFilters) =>
	request<RequestRecord[]>('GET', '/requests', { query });
export const getRequest = (id: string | number) =>
	request<RequestRecord>('GET', `/requests/${enc(id)}`);
export const getRequestSummary = (query?: RequestFilters) =>
	request<RequestSummary>('GET', '/requests/summary', { query });

export const listLogs = (query?: LogFilters) => request<LogRecord[]>('GET', '/logs', { query });
export const listEvents = () => request<ServerEvent[]>('GET', '/events');

export const getTelemetry = () => request<TelemetrySnapshot>('GET', '/telemetry');

export const startServer = () => request<unknown>('POST', '/server/start');
export const stopServer = () => request<unknown>('POST', '/server/stop');
export const restartServer = () => request<unknown>('POST', '/server/restart');

/** The SSE routes, for `stream`. */
export const LOGS_STREAM = '/logs/stream';
export const TELEMETRY_STREAM = '/telemetry/stream';

export interface StreamOptions {
	/** Called on every open and loss of the connection. */
	onStatus?: (connected: boolean) => void;
	/** First retry delay after a refused connection, in ms. */
	retryMs?: number;
	/** Cap for the doubling delay, in ms. */
	maxRetryMs?: number;
}

/**
 * Follows an SSE route and hands each message's parsed JSON to onMessage;
 * frames that are not JSON are skipped. Returns a function that stops it.
 *
 * EventSource retries dropped connections by itself but gives up for good on an
 * HTTP error, such as a 404 from a route this server version lacks, so those are
 * retried here with a growing delay.
 */
export function stream<T>(
	path: string,
	onMessage: (message: T) => void,
	options: StreamOptions = {}
): () => void {
	const { onStatus, retryMs = 1000, maxRetryMs = 30_000 } = options;
	let source: EventSource | undefined;
	let timer: ReturnType<typeof setTimeout> | undefined;
	let delay = retryMs;
	let stopped = false;

	function connect() {
		source = new EventSource(url(path));
		source.onopen = () => {
			delay = retryMs;
			onStatus?.(true);
		};
		source.onmessage = (event: MessageEvent<string>) => {
			let message: T;
			try {
				message = JSON.parse(event.data) as T;
			} catch {
				return;
			}
			onMessage(message);
		};
		source.onerror = () => {
			onStatus?.(false);
			if (source?.readyState !== EventSource.CLOSED || stopped) return;
			timer = setTimeout(connect, delay);
			delay = Math.min(delay * 2, maxRetryMs);
		};
	}

	connect();

	return () => {
		stopped = true;
		clearTimeout(timer);
		source?.close();
	};
}
