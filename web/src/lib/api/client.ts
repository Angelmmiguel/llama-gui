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
	update?: UpdateInfo;
}

/** What the last look at upstream's releases found. */
export interface UpdateInfo {
	available: boolean;
	/** The newer tag on the active build's channel. */
	latest?: string;
	stable?: string;
	nightly?: string;
	checked_at?: string;
	error?: string;
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
		flags: string[] | null;
		router: boolean;
		devices: string[] | null;
	};
	active?: boolean;
	/** Says the build can run as a router. */
	supported?: boolean;
	/** On disk. */
	size_bytes?: number;
}

/** A backend a release can be installed with. */
export interface BuildOption {
	backend: string;
	asset: string;
	/** The download in bytes, including the CUDA runtime. */
	size: number;
	variant?: string;
	/** Why the hardware cannot use it, if it cannot. */
	reason?: string;
}

export interface AvailableRelease {
	tag: string;
	/** The b* tag holding the binaries; null until the release is resolved. */
	binary_tag?: string | null;
	channel: string;
	published_at: string;
	body: string;
	/** Only on each channel's newest and on an exact version. */
	options?: BuildOption[] | null;
}

export interface BuildComparison {
	ahead_by: number;
	/** Oldest first, and possibly fewer than `ahead_by`. */
	commits: { sha: string; title: string }[] | null;
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

/** Something the preset left out. `flag` is empty when the whole profile was dropped. */
export interface SkippedFlag {
	alias: string;
	flag?: string;
	reason: string;
}

export interface ProfileMemoryUse {
	capacity: number;
	/** Other loaded models for VRAM, everything else in use for RAM. */
	other: number;
	weights: number;
	kv: number;
	compute: number;
}

export interface ProfileEstimate {
	/** `measured` by the router, `scaled` from its measurement, or a plain `estimate`. */
	source: string;
	/** The file's metadata was incomplete, so KV is a rule of thumb. */
	rough: boolean;
	/** The model's layer count, 0 when unknown. */
	layers: number;
	/** False when other loaded models are unknown rather than none. */
	router_up: boolean;
	/** Null when the model would run on the CPU. */
	gpu: ProfileMemoryUse | null;
	ram: ProfileMemoryUse;
	/** fits, tight or over. */
	verdict: string;
	/** gpu or ram. */
	verdict_on: string;
	free_bytes: number;
}

export interface LocalModelFile {
	id: string;
	name: string;
	/** A file or a directory, relative to the models directory. */
	path: string;
	quant?: string;
	/** Names of the files; a directory's are relative to it. */
	files: string[];
	mmproj?: string;
	shards?: number;
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

/** What verdicts are judged against. */
export interface LibraryHardware {
	/** Zero without a GPU, which makes verdicts about RAM. */
	vram_bytes: number;
	ram_bytes: number;
	headroom_bytes: number;
}

export interface LibraryFit {
	weights_bytes: number;
	kv_bytes: number;
	compute_bytes: number;
	total_bytes: number;
	/** fits_gpu, partial_offload, offload_experts, fits_ram or too_large; tolerate others. */
	verdict: string;
	/** Share of the total that spills to RAM. */
	offload_percent: number;
	moe: boolean;
	/** The KV size is a rule of thumb, as the header was incomplete or unreadable. */
	estimated: boolean;
	/** The numbers are the router's measurements of the loaded model. */
	calibrated: boolean;
}

export interface GgufMetadata {
	architecture: string;
	name: string;
	param_count: number;
	block_count: number;
	context_length: number;
	expert_count: number;
	is_moe: boolean;
	[key: string]: unknown;
}

export interface LocalModel {
	id: string;
	name: string;
	/** Relative to the models directory. */
	path: string;
	size_bytes: number;
	quant: string;
	files: string[];
	mmproj?: string;
	shards: number;
	metadata?: GgufMetadata;
	mod_time: string;
	/** What makes the model loadable. */
	profile: { id: string; alias: string; ctx_size: number; parallel: number } | null;
	/** Its name at the router. */
	router_id?: string;
	/** unloaded, loading, loaded or sleeping; tolerate others. */
	status: string;
	fit: LibraryFit;
}

export interface RepoFile {
	name: string;
	size: number;
	sha256?: string;
}

export interface RepoVariant {
	id: string;
	quant: string;
	files: RepoFile[];
	size_bytes: number;
	shards: number;
	fit: LibraryFit;
	/** The variant's files plus the mmproj a multimodal model needs. */
	download_files: string[];
	download_bytes: number;
}

export interface RepoRow {
	repo: string;
	name: string;
	author: string;
	downloads: number;
	likes: number;
	last_modified: string;
	gated: boolean;
	tags: string[];
	description?: string;
	catalog: boolean;
	/** Absent from a plain search, as they cost hub calls per repo: see getLibraryRepo. */
	variants?: RepoVariant[];
	mmproj?: RepoFile[];
	metadata?: GgufMetadata;
	metadata_error?: string;
	/** The id of the largest variant that fits, or else the smallest. */
	best?: string;
}

export interface LibrarySuggestion {
	/** largest_dense, fast_long_context or most_capable_offload; tolerate others. */
	category: string;
	repo: string;
	name: string;
	quant: string;
	fit: LibraryFit;
	tags: string[];
	note: string;
	metadata: GgufMetadata;
	download_files: string[];
	download_bytes: number;
}

export interface DownloadJob {
	id: string;
	repo: string;
	variant: string;
	files: string[];
	/** queued, downloading, done, failed or canceled; tolerate others. */
	state: string;
	done: number;
	total: number;
	bytes_per_sec: number;
	/** Zero when unknown. */
	eta_seconds: number;
	error?: string;
}

export interface LibraryDisk {
	path: string;
	gguf_bytes: number;
	free_bytes: number;
	total_bytes: number;
}

export interface FitQuery extends Query {
	/** Total context, -c. */
	ctx?: number;
	kv?: string;
	parallel?: number;
}

export interface BenchSetup {
	/** Relative to the models directory. */
	model_file: string;
	/** on, off or auto. */
	flash_attn: string[];
	cache_types: string[];
	depths: number[];
	repetitions: number;
	n_prompt?: number;
	n_gen?: number;
	ngl?: string;
}

export interface BenchEstimate {
	tests: number;
	seconds: number;
	command: string;
}

export interface BenchResult {
	backend: string;
	ngl: string;
	flash_attn: string;
	cache_k: string;
	cache_v: string;
	/** pp512, tg128 @ d4096. */
	test: string;
	/** pp, tg or pg; tolerate others. */
	kind: string;
	n: number;
	depth: number;
	tokens_per_sec: number;
	stddev: number;
}

export interface BenchLine {
	label: string;
	flash_attn: string;
	cache_k: string;
	cache_v: string;
	backend: string;
	ngl: string;
	points: { depth: number; tokens_per_sec: number; stddev: number }[];
}

export interface BenchRun {
	id: number;
	/** running, done, failed or canceled. */
	state: string;
	setup: BenchSetup;
	/** The build id. */
	build: string;
	started_at: string;
	finished_at?: string;
	done: number;
	total: number;
	/** The last finished test, or the phase before any has. */
	current: string;
	results: BenchResult[] | null;
	error?: string;
}

export interface BenchRunDetail extends BenchRun {
	series: { decode: BenchLine[]; prefill: BenchLine[] };
	/** Only when the data supports one. */
	takeaway?: string;
	markdown: string;
}

export interface BenchBuildPoint {
	build: string;
	run_id: number;
	started_at: string;
	tokens_per_sec: number;
	/** Null for the first build. */
	delta_percent: number | null;
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

export const listBuilds = () =>
	request<{ builds: BuildManifest[]; active: string }>('GET', '/builds');
export const listAvailableBuilds = (channel?: string, limit?: number) =>
	request<{ releases: AvailableRelease[] }>('GET', '/builds/available', {
		query: { channel, limit }
	}).then((r) => r.releases);
/** One release by tag, with its backends. */
export const getAvailableBuild = (version: string) =>
	request<{ releases: AvailableRelease[] }>('GET', '/builds/available', {
		query: { version }
	}).then((r) => r.releases[0]);
export const compareBuilds = (from: string, to: string) =>
	request<BuildComparison>('GET', '/builds/compare', { query: { from, to } });
export const checkForUpdates = () => request<UpdateInfo>('POST', '/builds/check');
export const installBuild = (body: { version: string; backend: string }) =>
	request<InstallJob>('POST', '/builds/install', { body });
export const listBuildJobs = () =>
	request<{ jobs: InstallJob[] }>('GET', '/builds/jobs').then((r) => r.jobs);
export const cancelBuildJob = (id: string) => request<unknown>('DELETE', `/builds/jobs/${enc(id)}`);
export const activateBuild = (id: string) =>
	request<unknown>('POST', `/builds/${enc(id)}/activate`);
export const deleteBuild = (id: string) => request<unknown>('DELETE', `/builds/${enc(id)}`);

export const listProfiles = () =>
	request<{ profiles: Profile[] }>('GET', '/profiles').then((r) => r.profiles);
/** The list with what the last render of the preset left out. */
export const listProfilesDetail = () =>
	request<{ profiles: Profile[]; skipped: SkippedFlag[] }>('GET', '/profiles');
export const previewProfile = (body: Profile) =>
	request<{ command: string; error?: string }>('POST', '/profiles/preview', { body });
export const estimateProfile = (body: Profile) =>
	request<ProfileEstimate>('POST', '/profiles/estimate', { body });
/** The models on disk, as far as the profile form needs them. */
export const listLocalModelFiles = () =>
	request<{ models: LocalModelFile[] }>('GET', '/library/local').then((r) => r.models);
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

export const getLibraryLocal = (query: FitQuery, signal?: AbortSignal) =>
	request<{ models: LocalModel[]; hardware: LibraryHardware }>('GET', '/library/local', {
		query,
		signal
	});
export const deleteLocalModel = (path: string) =>
	request<unknown>('DELETE', '/library/local', { query: { path } });
export const searchLibrary = (
	query: FitQuery & { q?: string; filter?: string },
	signal?: AbortSignal
) =>
	request<{ source: string; results: RepoRow[]; hardware: LibraryHardware }>(
		'GET',
		'/library/search',
		{ query, signal }
	);
export const getLibraryRepo = (repo: string, query: FitQuery, signal?: AbortSignal) =>
	request<{ repo: RepoRow; hardware: LibraryHardware }>('GET', '/library/repo', {
		query: { ...query, repo },
		signal
	});
export const getLibrarySuggestions = (query: FitQuery, signal?: AbortSignal) =>
	request<{ suggestions: LibrarySuggestion[]; hardware: LibraryHardware }>(
		'GET',
		'/library/suggestions',
		{ query, signal }
	);
export const listDownloads = (signal?: AbortSignal) =>
	request<{ jobs: DownloadJob[] }>('GET', '/library/downloads', { signal }).then((r) => r.jobs);
export const startDownload = (repo: string, files: readonly string[]) =>
	request<DownloadJob>('POST', '/library/downloads', { body: { repo, files } });
export const cancelDownload = (id: string, removePartials: boolean) =>
	request<unknown>('DELETE', `/library/downloads/${enc(id)}`, {
		query: { remove_partials: removePartials }
	});
export const getLibraryDisk = (signal?: AbortSignal) =>
	request<LibraryDisk>('GET', '/library/disk', { signal });

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

export const listBenchRuns = (model: string) =>
	request<{ runs: BenchRun[] }>('GET', '/bench/runs', { query: { model } }).then((r) => r.runs);
export const getBenchRun = (id: number) => request<BenchRunDetail>('GET', `/bench/runs/${id}`);
export const startBenchRun = (body: BenchSetup) =>
	request<BenchRunDetail>('POST', '/bench/runs', { body });
export const getCurrentBenchRun = (signal?: AbortSignal) =>
	request<{ run: BenchRunDetail | null }>('GET', '/bench/current', { signal }).then((r) => r.run);
export const cancelBenchRun = () => request<unknown>('DELETE', '/bench/current');
export const estimateBench = (body: BenchSetup, signal?: AbortSignal) =>
	request<BenchEstimate>('POST', '/bench/estimate', { body, signal });
export const getBenchHistory = (model: string) =>
	request<{ history: BenchBuildPoint[] }>('GET', '/bench/history', { query: { model } }).then(
		(r) => r.history
	);

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
