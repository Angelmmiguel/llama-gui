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
	/** The `-lv` level the router runs with. */
	log_verbosity?: number;
	router_since?: string;
	pid?: number;
	restart_count?: number;
	last_error?: string;
	active_build?: { id: string; tag: string; backend: string } | null;
	models_loaded?: number;
	update_available?: boolean;
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

export interface MemoryDevice {
	/** `device` for a GPU, `host` for system memory. */
	kind: string;
	/** `CUDA0`, `Vulkan1`, `Host`. */
	name: string;
	model: number;
	context: number;
	compute: number;
}

export interface MemoryEstimate {
	n_ctx: number;
	/** Null while unloaded, for `effective`. */
	devices: MemoryDevice[] | null;
}

export interface RouterModel {
	id: string;
	/** unloaded, loading, loaded or sleeping; tolerate others. */
	status: string;
	port?: number;
	exit_code?: number;
	failed?: boolean;
	context_length?: number;
	source?: string;
	memory: { status?: string; min: MemoryEstimate; max: MemoryEstimate; effective: MemoryEstimate };
}

export interface InflightRequest {
	id: number;
	model: string;
	endpoint: string;
	started_at: string;
	slot?: number;
	task?: number;
	/** `waiting` before upstream answers (queued or in prefill), then `responding`. */
	phase: string;
}

/** A slot as sampled; see "Slot fields" in ARCHITECTURE. */
export interface Slot {
	id: number;
	/** idle, prefill or decode. */
	state: string;
	cached: number;
	prompt: number;
	generated: number;
	total: number;
	n_ctx: number;
	/** Stays set after the task finishes. */
	task: number | null;
	speed: number;
	params?: Record<string, unknown>;
}

/** A model as sampled. Memory is flattened from the router's `memory.effective`. */
export interface TelemetryModel {
	id: string;
	status: string;
	memory: { total: number; devices: MemoryDevice[] };
	slots: Slot[];
	/** Why this tick could not read the model; slots then hold the last good reading. */
	error?: string;
}

export interface SlotHistory {
	model: string;
	slot: number;
	/** Oldest first, up to 90 s. */
	samples: { state: string; speed: number }[];
}

export type SlotAction = 'save' | 'restore' | 'erase' | 'cancel';

export interface RequestFilters extends Query {
	model?: string;
	/** One path, or several separated by commas. */
	endpoint?: string;
	status?: string;
	since?: string;
	limit?: number;
	until?: string;
	cursor?: string;
}

export interface RequestRecord {
	id: number;
	started_at: string;
	model: string;
	endpoint: string;
	method: string;
	status: number;
	slot: number | null;
	task_id: number | null;
	ttft_ms: number;
	total_ms: number;
	queue_ms: number;
	prompt_n: number;
	cache_n: number;
	prompt_ms: number;
	predicted_n: number;
	predicted_ms: number;
	predicted_per_second: number;
	stream: boolean;
	/** A classification such as `400 ctx`; empty on a plain success. */
	error: string;
}

export interface RequestDetail extends RequestRecord {
	/** Milliseconds between the first 120 tokens; empty when not streamed. */
	gaps: number[];
	/** Null unless capture kept it. */
	body: { request: string; response: string } | null;
}

export interface Percentiles {
	p50: number;
	p95: number;
	p99: number;
}

export interface RequestSummaryModel {
	model: string;
	count: number;
	/** Medians in ms, over the requests that reported each. */
	queue_ms: number;
	prefill_ms: number;
	decode_ms: number;
	total_ms: number;
}

export interface ScatterPoint {
	id: number;
	started_at: string;
	model: string;
	status: number;
	prompt_n: number;
	cache_n: number;
	predicted_n: number;
	predicted_ms: number;
	ttft_ms: number;
	total_ms: number;
	stream: boolean;
}

export interface RequestSummary {
	since: string;
	until: string;
	/** Only the newest rows of the window were looked at. */
	truncated: boolean;
	kpis: {
		requests: number;
		errors: number;
		per_minute: number;
		prompt_tokens: number;
		cached_tokens: number;
		predicted_tokens: number;
		decode_speed: number;
	};
	ttft: Percentiles;
	total: Percentiles;
	/** `status` is a code, or a classified error such as `400 ctx`. */
	statuses: { status: string; count: number }[];
	models: RequestSummaryModel[];
	/** The newest 500. */
	scatter: ScatterPoint[];
}

export interface LogFilters extends Query {
	q?: string;
	/** One value, or several separated by commas. */
	level?: string;
	/** One value, or several separated by commas. */
	source?: string;
	model?: string;
	since?: string;
	until?: string;
	limit?: number;
	cursor?: string;
}

export interface LogRecord {
	id: number;
	ts: string;
	level: string;
	source: string;
	model: string;
	message: string;
	raw?: string;
}

export interface ServerEvent {
	id: number;
	ts: string;
	kind: string;
	/** info, warn or error; tolerate others. */
	severity: string;
	title: string;
	meta?: Record<string, string>;
}

export interface LogVolumeMinute {
	minute: string;
	debug: number;
	info: number;
	warn: number;
	error: number;
	other: number;
}

export interface LogVolume {
	/** Oldest first, always 60. */
	minutes: LogVolumeMinute[];
	/** Counted since local midnight. */
	today: { warn: number; error: number };
}

export interface TelemetrySnapshot {
	time: string;
	server: { state: string; error?: string };
	models: TelemetryModel[];
	throughput: { decode: number; prefill: number };
	requests: {
		in_flight: number;
		processing: number;
		deferred: number;
		per_minute: number;
		ttft_p50_ms: number;
		ttft_p95_ms: number;
	};
	kv: { used: number; total: number };
	gpus: Gpu[];
	ram: { total: number; available: number };
	in_flight: InflightRequest[];
	series: {
		decode: number[];
		prefill: number[];
		/** KV is a 0 to 1 ratio. */
		spark: { prefill: number[]; ttft: number[]; rpm: number[]; kv: number[] };
	};
	slot_history: SlotHistory[];
}

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

export const listProfiles = () =>
	request<{ profiles: Profile[] }>('GET', '/profiles').then((r) => r.profiles);
export const createProfile = (body: Omit<Profile, 'id'> & { id?: string }) =>
	request<Profile>('POST', '/profiles', { body });
export const getProfile = (id: string) => request<Profile>('GET', `/profiles/${enc(id)}`);
export const updateProfile = (id: string, body: Profile) =>
	request<Profile>('PUT', `/profiles/${enc(id)}`, { body });
export const deleteProfile = (id: string) => request<unknown>('DELETE', `/profiles/${enc(id)}`);

export const listModels = () =>
	request<{ models: RouterModel[] }>('GET', '/models').then((r) => r.models);
export const loadModel = (id: string) => request<unknown>('POST', `/models/${enc(id)}/load`);
export const unloadModel = (id: string) => request<unknown>('POST', `/models/${enc(id)}/unload`);

export const listSlots = () =>
	request<{ models: TelemetryModel[]; in_flight: InflightRequest[]; time: string }>(
		'GET',
		'/slots'
	);
/** `filename` is required by save and restore: a single path element. */
export const slotAction = (model: string, slot: number, action: SlotAction, filename?: string) =>
	request<unknown>('POST', `/slots/${enc(model)}/${slot}/${action}`, {
		body: filename === undefined ? undefined : { filename }
	});

export const listRequests = (query?: RequestFilters) =>
	request<{ requests: RequestRecord[]; next_cursor: string }>('GET', '/requests', { query });
export const getRequest = (id: string | number) =>
	request<RequestDetail>('GET', `/requests/${enc(id)}`);
export const getRequestSummary = (query?: RequestFilters) =>
	request<RequestSummary>('GET', '/requests/summary', { query });

export const requestsCsvUrl = (query?: RequestFilters) =>
	url('/requests', { ...query, format: 'csv' });

export const listLogs = (query?: LogFilters) =>
	request<{ lines: LogRecord[]; next_cursor: string }>('GET', '/logs', { query });
export const getLogVolume = () => request<LogVolume>('GET', '/logs/volume');
export const logsDownloadUrl = (query?: LogFilters) => url('/logs/download', query);
export const listEvents = (since?: string) =>
	request<{ events: ServerEvent[] }>('GET', '/events', { query: { since } });

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
	/** Follow this named event, as `/logs/stream`'s `log`, instead of unnamed messages. */
	event?: string;
	/** Query parameters of the route. */
	query?: Query;
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
	const { onStatus, retryMs = 1000, maxRetryMs = 30_000, event: name, query } = options;
	let source: EventSource | undefined;
	let timer: ReturnType<typeof setTimeout> | undefined;
	let delay = retryMs;
	let stopped = false;

	function connect() {
		source = new EventSource(url(path, query));
		source.onopen = () => {
			delay = retryMs;
			onStatus?.(true);
		};
		const handle = (event: MessageEvent<string>) => {
			let message: T;
			try {
				message = JSON.parse(event.data) as T;
			} catch {
				return;
			}
			onMessage(message);
		};
		if (name) source.addEventListener(name, handle);
		else source.onmessage = handle;
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
