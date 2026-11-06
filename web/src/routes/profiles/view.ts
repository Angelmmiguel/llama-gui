import type {
	LocalModelFile,
	Profile,
	ProfileEstimate,
	ProfileMemoryUse,
	RouterModel
} from '$lib/api/client';
import { tones } from '$lib/components/tones';
import { formatGB } from '$lib/format';

/** Everything the form edits, as typed. Numbers stay strings until they are saved. */
export interface Form {
	id?: string;
	name: string;
	alias: string;
	modelFile: string;
	mmproj: string;
	specType: string;
	specDraftMax: string;
	specDraftModel: string;
	specDraftNgl: string;
	/** Undefined leaves it to upstream, whose default is on. */
	jinja: boolean | undefined;
	mlock: boolean;
	ctx: string;
	/** 0 leaves the slot count to upstream. */
	parallel: number;
	ngl: string;
	splitMode: string;
	tensorSplit: string;
	flashAttn: string;
	ctk: string;
	ctv: string;
	cacheReuse: string;
	batch: string;
	ubatch: string;
	threads: string;
	temp: string;
	topP: string;
	topK: string;
	minP: string;
	repeatPenalty: string;
	loadAtStart: boolean;
	idleUnload: boolean;
	idleMinutes: string;
	onDemand: boolean;
	extra: string;
}

export const CTX_CHOICES = [8192, 16384, 32768, 65536, 131072];
const DEFAULT_CTX = 32768;
export const DEFAULT_IDLE_MINUTES = '10';

const str = (n: number | undefined) => (n === undefined || n === 0 ? '' : String(n));
const strKeepZero = (n: number | undefined) => (n === undefined ? '' : String(n));

export function newForm(modelFile = '', ctx = DEFAULT_CTX): Form {
	const alias = aliasFromFile(modelFile);
	return fromProfile({ id: '', name: alias, alias, model_file: modelFile, ctx_size: ctx }, true);
}

export function fromProfile(p: Profile, fresh = false): Form {
	const idle = p.idle_unload_seconds ?? 0;
	return {
		id: p.id || undefined,
		name: p.name,
		alias: p.alias,
		modelFile: p.model_file,
		mmproj: p.mmproj ?? '',
		specType: p.spec_type ?? '',
		specDraftMax: str(p.spec_draft_max),
		specDraftModel: p.spec_draft_model ?? '',
		specDraftNgl: p.spec_draft_ngl ?? '',
		jinja: p.jinja,
		mlock: p.mlock ?? false,
		ctx: str(p.ctx_size),
		parallel: fresh ? 1 : (p.parallel ?? 0),
		ngl: p.ngl ?? '',
		splitMode: p.split_mode ?? '',
		tensorSplit: p.tensor_split ?? '',
		flashAttn: p.flash_attn ?? '',
		ctk: p.cache_type_k ?? '',
		ctv: p.cache_type_v ?? '',
		cacheReuse: strKeepZero(p.cache_reuse),
		batch: str(p.batch),
		ubatch: str(p.ubatch),
		threads: str(p.threads),
		temp: strKeepZero(p.temp),
		topP: strKeepZero(p.top_p),
		topK: strKeepZero(p.top_k),
		minP: strKeepZero(p.min_p),
		repeatPenalty: strKeepZero(p.repeat_penalty),
		loadAtStart: p.load_at_start ?? false,
		idleUnload: idle > 0,
		idleMinutes: idle > 0 ? String(idle / 60) : DEFAULT_IDLE_MINUTES,
		onDemand: fresh || (p.on_demand ?? false),
		extra: (p.extra_args ?? []).join(' ')
	};
}

function parseNumber(
	raw: string,
	label: string,
	errors: string[],
	whole: boolean
): number | undefined {
	const text = raw.trim();
	if (text === '') return undefined;
	const n = Number(text);
	if (!Number.isFinite(n) || n < 0 || (whole && !Number.isInteger(n))) {
		errors.push(`${label} must be a ${whole ? 'whole ' : ''}non-negative number`);
		return undefined;
	}
	return n;
}

/**
 * The profile the API takes, with what could not be read as a number left out and
 * reported. The API validates everything else.
 */
export function toProfile(f: Form): { profile: Profile; errors: string[] } {
	const errors: string[] = [];
	const whole = (raw: string, label: string) => parseNumber(raw, label, errors, true);
	const real = (raw: string, label: string) => parseNumber(raw, label, errors, false);
	const minutes = f.idleUnload ? real(f.idleMinutes, 'Idle minutes') : undefined;
	if (f.idleUnload && !minutes) errors.push('Idle minutes must be more than 0');
	const extra = f.extra.trim();
	return {
		errors,
		profile: {
			id: f.id ?? '',
			name: f.name.trim() || f.alias.trim(),
			alias: f.alias.trim(),
			model_file: f.modelFile.trim(),
			mmproj: f.mmproj.trim() || undefined,
			spec_type: f.specType.trim() || undefined,
			spec_draft_max: whole(f.specDraftMax, 'Max draft tokens'),
			spec_draft_model: needsDraftModel(f.specType)
				? f.specDraftModel.trim() || undefined
				: undefined,
			spec_draft_ngl: needsDraftModel(f.specType) ? f.specDraftNgl.trim() || undefined : undefined,
			jinja: f.jinja,
			mlock: f.mlock || undefined,
			ctx_size: whole(f.ctx, 'Total context') ?? 0,
			parallel: f.parallel || undefined,
			ngl: f.ngl.trim() || undefined,
			split_mode: f.splitMode || undefined,
			tensor_split: f.tensorSplit.trim() || undefined,
			flash_attn: f.flashAttn || undefined,
			cache_type_k: f.ctk || undefined,
			cache_type_v: f.ctv || undefined,
			cache_reuse: whole(f.cacheReuse, '--cache-reuse'),
			batch: whole(f.batch, 'Batch') || undefined,
			ubatch: whole(f.ubatch, 'Micro-batch') || undefined,
			threads: whole(f.threads, 'Threads') || undefined,
			temp: real(f.temp, 'Temperature'),
			top_p: real(f.topP, 'Top-p'),
			top_k: whole(f.topK, 'Top-k'),
			min_p: real(f.minP, 'Min-p'),
			repeat_penalty: real(f.repeatPenalty, 'Repeat penalty'),
			load_at_start: f.loadAtStart || undefined,
			idle_unload_seconds: minutes ? Math.round(minutes * 60) : undefined,
			on_demand: f.onDemand || undefined,
			extra_args: extra ? extra.split(/\s+/) : undefined
		}
	};
}

/** How many settings differ, for "3 unsaved changes". */
export function countChanges(a: Form, b: Form): number {
	const keys = Object.keys(a) as (keyof Form)[];
	return keys.filter((k) => {
		// A name that follows the alias is not a second change.
		if (k === 'name' && a.alias !== b.alias) return false;
		// The minutes only mean something while idle unload is on.
		if (k === 'idleMinutes' && !a.idleUnload && !b.idleUnload) return false;
		return (a[k] ?? '') !== (b[k] ?? '');
	}).length;
}

/** `64k`, or the plain count below a whole 1k. */
export function formatCtx(n: number): string {
	return n >= 1024 && n % 1024 === 0 ? `${n / 1024}k` : String(n);
}

export function perSlot(
	ctx: number,
	parallel: number
): { tokens: number; note: string; tight: boolean } {
	if (parallel === 0) {
		return { tokens: ctx, note: 'Slot count left to the server: this is the total', tight: false };
	}
	const tokens = Math.floor(ctx / parallel);
	if (tokens < 8192) {
		return {
			tokens,
			note: 'Tight: long chat histories will be rejected or shifted',
			tight: true
		};
	}
	return {
		tokens,
		note:
			tokens >= 32768
				? 'Room for big repos and long documents'
				: 'Good for chat and coding sessions',
		tight: false
	};
}

const isQuantized = (t: string) => t !== '' && !['f16', 'f32', 'bf16'].includes(t);

export function kvNote(
	ctk: string,
	ctv: string,
	flashAttn: string
): { text: string; warn: boolean } {
	if (isQuantized(ctv) && flashAttn === 'off') {
		return { text: 'Quantized V cache needs flash attention on.', warn: true };
	}
	if (ctk === 'q4_0') {
		return {
			text: 'q4_0 keys can noticeably lower quality on long contexts; q8_0 is the safer saving.',
			warn: true
		};
	}
	return { text: 'q8_0 halves KV memory with almost no quality loss.', warn: false };
}

/** What the slider shows for an `-ngl` value. */
export function nglView(ngl: string, layers: number): { value: number; label: string } {
	const n = Number(ngl);
	const named = ngl === '' || ngl === 'auto' || ngl === 'all' || !Number.isInteger(n) || n < 0;
	if (layers <= 0) {
		return { value: named ? 0 : n, label: named ? ngl || 'auto' : `${n} · layer count unknown` };
	}
	if (named)
		return { value: layers, label: ngl === 'all' ? 'all on GPU' : 'auto · up to the server' };
	const on = Math.min(n, layers);
	if (on === layers) return { value: on, label: `${on} / ${layers} · all on GPU` };
	if (on === 0) return { value: 0, label: `0 / ${layers} · CPU only` };
	return {
		value: on,
		label: `${on} / ${layers} · ${Math.round((1 - on / layers) * 100)}% on CPU, slower decode`
	};
}

/** Maps each form setting to its long flag, as the build's `--help` lists it. */
export const FLAGS = {
	jinja: '--jinja',
	mlock: '--mlock',
	ctx: '--ctx-size',
	parallel: '--parallel',
	ngl: '--n-gpu-layers',
	splitMode: '--split-mode',
	tensorSplit: '--tensor-split',
	flashAttn: '--flash-attn',
	ctk: '--cache-type-k',
	ctv: '--cache-type-v',
	cacheReuse: '--cache-reuse',
	batch: '--batch-size',
	ubatch: '--ubatch-size',
	threads: '--threads',
	temp: '--temp',
	topP: '--top-p',
	topK: '--top-k',
	minP: '--min-p',
	repeatPenalty: '--repeat-penalty',
	specType: '--spec-type',
	specDraftMax: '--spec-draft-n-max',
	specDraftModel: '--spec-draft-model',
	specDraftNgl: '--spec-draft-ngl'
} as const;
export type FlagField = keyof typeof FLAGS;

/**
 * The form settings a build lacks. A build whose `--help` could not be read lists
 * no flags, which says nothing about what it supports.
 */
export function unsupportedFields(flags: readonly string[] | null | undefined): Set<FlagField> {
	if (!flags || flags.length === 0) return new Set();
	const known = new Set(flags);
	return new Set(
		(Object.keys(FLAGS) as FlagField[]).filter(
			(f) => !known.has(FLAGS[f]) && !(f === 'mlock' && known.has(LOAD_MODE))
		)
	);
}

/** Newer builds moved `--mlock` into `--load-mode`. The API picks the same one for the preset. */
const LOAD_MODE = '--load-mode';
const LOCKING_LOAD_MODES = new Set(['mlock', 'mmap+mlock']);

/** How the active build spells the setting, to show next to its switch. */
export function mlockFlag(flags: readonly string[] | null | undefined): string {
	return flags?.includes(LOAD_MODE) && !flags.includes(FLAGS.mlock)
		? `${LOAD_MODE} mmap+mlock`
		: FLAGS.mlock;
}

/** The speculative decoding types offered, from the build's --help. */
const SPEC_TYPES = [
	{ value: '', label: 'Off' },
	{ value: 'draft-mtp', label: 'MTP · heads in the main model' },
	{ value: 'draft-dflash', label: 'DFlash · draft model' },
	{ value: 'draft-dspark', label: 'DSpark · draft model' },
	{ value: 'draft-eagle3', label: 'EAGLE-3 · draft model' },
	{ value: 'draft-simple', label: 'Simple · draft model' },
	{ value: 'ngram-mod', label: 'N-gram mod · no extra model' },
	{ value: 'ngram-simple', label: 'N-gram simple · no extra model' },
	{ value: 'ngram-map-k', label: 'N-gram map · no extra model' },
	{ value: 'ngram-map-k4v', label: 'N-gram map k4v · no extra model' },
	{ value: 'ngram-cache', label: 'N-gram cache · no extra model' }
];

/** A combination or a newer type from an imported command stays selectable as it is. */
export function specTypeOptions(current: string): { value: string; label: string }[] {
	return current && !SPEC_TYPES.some((t) => t.value === current)
		? [...SPEC_TYPES, { value: current, label: current }]
		: SPEC_TYPES;
}

const DRAFT_FILE_TYPES = new Set([
	'draft',
	'draft-simple',
	'draft-eagle3',
	'draft-dflash',
	'draft-dspark'
]);

export const needsDraftModel = (type: string) =>
	type.split(',').some((t) => DRAFT_FILE_TYPES.has(t.trim()));

/** Says so when the chosen file is known not to have the heads MTP needs. Unknown stays silent. */
export function mtpWarning(
	type: string,
	file: string,
	local: readonly LocalModelFile[] | null
): string {
	if (!type.split(',').includes('draft-mtp') || !local) return '';
	const m = local.find((l) => file === l.path || file.startsWith(`${l.path}/`));
	if (!m?.metadata || m.metadata.mtp_layers) return '';
	return 'This file has no MTP heads. Pick a quant tagged MTP in the Library.';
}

/** A name that passes the alias rule, from a model's file name. */
export function aliasFromFile(file: string): string {
	const base = file.split('/').pop() ?? '';
	return base
		.replace(/\.gguf$/i, '')
		.replace(/-\d{5}-of-\d{5}$/, '')
		.replace(/[^A-Za-z0-9._:-]+/g, '-')
		.replace(/^[^A-Za-z0-9]+/, '')
		.slice(0, 128);
}

/** The file a local model loads from: a directory's first shard, or its one model. */
export function modelMainFile(m: LocalModelFile): string {
	if (m.files.length === 1 && m.files[0] === m.path) return m.path;
	const names = m.files.filter((f) => f !== m.mmproj && !/mmproj/i.test(f)).toSorted();
	const main = names.find((f) => /-0*1-of-\d+\.gguf$/i.test(f)) ?? names[0] ?? m.files[0] ?? '';
	return main ? `${m.path}/${main}` : m.path;
}

export function modelChoices(
	local: readonly LocalModelFile[],
	current: string
): { value: string; label: string }[] {
	const out = local.map((m) => {
		const value = modelMainFile(m);
		return { value, label: value };
	});
	if (current && !out.some((o) => o.value === current)) {
		out.unshift({ value: current, label: `${current} (not on disk)` });
	}
	return out;
}

/** The path a Library link names, which may be a model's directory, as a file. */
export function resolveModelPath(path: string, local: readonly LocalModelFile[]): string {
	const m = local.find((l) => l.path === path);
	return m ? modelMainFile(m) : path;
}

export function quantOf(file: string, local: readonly LocalModelFile[]): string {
	const m = local.find((l) => file === l.path || file.startsWith(`${l.path}/`));
	if (m?.quant) return m.quant;
	const match = /[-._]((?:I?Q\d[A-Z0-9_]*|MXFP\d|F16|F32|BF16))(?:[-.]|$)/i.exec(file);
	return match ? match[1].toUpperCase() : '';
}

export interface ListStatus {
	color: string;
	word: string;
}

/** The dot and word of a profile in the list. */
export function listStatus(
	p: Profile,
	model: RouterModel | undefined,
	routerUp: boolean
): ListStatus {
	if (!routerUp) return { color: tones.off, word: 'router stopped' };
	if (model?.failed) return { color: tones.error, word: 'failed' };
	switch (model?.status) {
		case 'loaded':
			return { color: tones.ok, word: 'loaded' };
		case 'loading':
			return { color: tones.warn, word: 'loading' };
		case 'sleeping':
			return { color: tones.idle, word: 'sleeping' };
	}
	return { color: tones.idle, word: p.on_demand ? 'on demand' : 'unloaded' };
}

export function listMeta(p: Profile, quant: string, word: string): string {
	const shape = p.parallel ? `${formatCtx(p.ctx_size)} × ${p.parallel}` : formatCtx(p.ctx_size);
	return [quant, shape, word].filter(Boolean).join(' · ');
}

export interface MemoryBarView {
	label: string;
	segments: { value: number; color: string }[];
}

export interface MemoryView {
	headline: string;
	headlineUnit: string;
	verdict: string;
	verdictColor: string;
	source: string;
	/** Shown under the bars when the figures carry a caveat. */
	caveat: string;
	bars: MemoryBarView[];
	rows: { label: string; value: string; warn?: boolean }[];
}

const gb = (n: number) => `${formatGB(n)} GB`;
const own = (m: ProfileMemoryUse) => m.weights + m.kv + m.compute;

function percents(
	m: ProfileMemoryUse,
	colors: Record<'other' | 'weights' | 'kv' | 'compute', string>
) {
	const pct = (n: number) => (m.capacity > 0 ? (n / m.capacity) * 100 : 0);
	return [
		{ value: pct(m.other), color: colors.other },
		{ value: pct(m.weights), color: colors.weights },
		{ value: pct(m.kv), color: colors.kv },
		{ value: pct(m.compute), color: colors.compute }
	];
}

const SOURCES: Record<string, string> = {
	measured: 'Measured by the router at these settings',
	scaled: "Estimate: the router's measurement, scaled for your changes",
	estimate: 'Estimate from the GGUF metadata'
};

export function memoryView(e: ProfileEstimate, ctk: string, ctv: string): MemoryView {
	const free = Math.abs(e.free_bytes);
	const where = e.gpu && e.verdict_on === 'ram' ? ' RAM' : '';
	const verdicts: Record<string, { text: string; color: string }> = {
		fits: { text: `fits · ${gb(free)} free`, color: tones.ok },
		tight: { text: `very tight · ${gb(free)} free`, color: tones.warn },
		over: { text: `over by ${gb(free)}${where}`, color: tones.errorSoft }
	};
	const verdict = verdicts[e.verdict] ?? { text: e.verdict, color: tones.muted };
	const kvLabel = `KV cache (${ctk || 'f16'}/${ctv || 'f16'})`;
	const others = e.router_up
		? `includes other loaded models: ${gb(e.gpu?.other ?? 0)}`
		: 'other models unknown';
	const ramColors = {
		other: tones.other,
		weights: tones.prefill,
		kv: tones.cached,
		compute: tones.compute
	};
	const ramUse = `System RAM · ${formatGB(e.ram.capacity, 0)} GB`;
	const caveats = [
		e.rough ? "The file's metadata is incomplete, so the KV cache is a rule of thumb." : '',
		e.router_up ? '' : 'The router is stopped, so other loaded models are not counted.'
	];
	if (!e.gpu) {
		return {
			headline: formatGB(own(e.ram)),
			headlineUnit: 'GB in RAM',
			verdict: verdict.text,
			verdictColor: verdict.color,
			source: SOURCES[e.source] ?? e.source,
			caveat: caveats.filter(Boolean).join(' '),
			bars: [
				{
					label: `${ramUse} (includes everything else in use: ${gb(e.ram.other)})`,
					segments: percents(e.ram, ramColors)
				}
			],
			rows: [
				{ label: 'weights in RAM', value: gb(e.ram.weights) },
				{ label: kvLabel, value: gb(e.ram.kv) },
				{ label: 'compute buffers', value: gb(e.ram.compute) }
			]
		};
	}
	return {
		headline: formatGB(own(e.gpu)),
		headlineUnit: 'GB on GPU',
		verdict: verdict.text,
		verdictColor: verdict.color,
		source: SOURCES[e.source] ?? e.source,
		caveat: caveats.filter(Boolean).join(' '),
		bars: [
			{
				label: `VRAM · ${formatGB(e.gpu.capacity, 0)} GB (${others})`,
				segments: percents(e.gpu, {
					other: tones.other,
					weights: tones.weights,
					kv: tones.cached,
					compute: tones.compute
				})
			},
			{ label: ramUse, segments: percents(e.ram, ramColors) }
		],
		rows: [
			{ label: 'weights on GPU', value: gb(e.gpu.weights) },
			{ label: kvLabel, value: gb(e.gpu.kv) },
			{ label: 'compute buffers', value: gb(e.gpu.compute) },
			{ label: 'weights in RAM', value: gb(e.ram.weights), warn: e.ram.weights > 0 }
		]
	};
}

/** A command line's words, honouring quotes and `\` line continuations. */
export function tokenize(line: string): string[] {
	const out: string[] = [];
	let cur = '';
	let started = false;
	let quote = '';
	for (let i = 0; i < line.length; i++) {
		const c = line[i];
		if (quote) {
			if (c === quote) quote = '';
			else if (c === '\\' && quote === '"' && i + 1 < line.length) cur += line[++i];
			else cur += c;
		} else if (c === "'" || c === '"') {
			quote = c;
			started = true;
		} else if (c === '\\') {
			if (line[i + 1] === '\n') i++;
			else if (line[i + 1] === '\r' && line[i + 2] === '\n') i += 2;
			else if (i + 1 < line.length) {
				cur += line[++i];
				started = true;
			}
		} else if (/\s/.test(c)) {
			if (started) out.push(cur);
			cur = '';
			started = false;
		} else {
			cur += c;
			started = true;
		}
	}
	if (started) out.push(cur);
	return out;
}

type Setter = (f: Partial<Form>, value: string) => void;

const valued: Record<string, Setter> = {};
for (const [names, set] of [
	[['-m', '--model'], (f, v) => (f.modelFile = v)],
	[['--mmproj', '-mm'], (f, v) => (f.mmproj = v)],
	[['--spec-type'], (f, v) => (f.specType = v)],
	[['--spec-draft-n-max', '--draft-max', '--draft-n', '--draft'], (f, v) => (f.specDraftMax = v)],
	[['--spec-draft-model', '-md', '--model-draft'], (f, v) => (f.specDraftModel = v)],
	[
		['--spec-draft-ngl', '-ngld', '--gpu-layers-draft', '--n-gpu-layers-draft'],
		(f, v) => (f.specDraftNgl = v)
	],
	[['-a', '--alias'], (f, v) => (f.alias = v)],
	[['-c', '--ctx-size'], (f, v) => (f.ctx = v)],
	[['-np', '--parallel'], (f, v) => (f.parallel = Math.max(Number(v) || 0, 0))],
	[['-ngl', '--n-gpu-layers', '--gpu-layers'], (f, v) => (f.ngl = v)],
	[['-sm', '--split-mode'], (f, v) => (f.splitMode = v)],
	[['-ts', '--tensor-split'], (f, v) => (f.tensorSplit = v)],
	[['-ctk', '--cache-type-k'], (f, v) => (f.ctk = v)],
	[['-ctv', '--cache-type-v'], (f, v) => (f.ctv = v)],
	[['--cache-reuse'], (f, v) => (f.cacheReuse = v)],
	[['-b', '--batch-size'], (f, v) => (f.batch = v)],
	[['-ub', '--ubatch-size'], (f, v) => (f.ubatch = v)],
	[['-t', '--threads'], (f, v) => (f.threads = v)],
	[['--temp', '--temperature'], (f, v) => (f.temp = v)],
	[['--top-p'], (f, v) => (f.topP = v)],
	[['--top-k'], (f, v) => (f.topK = v)],
	[['--min-p'], (f, v) => (f.minP = v)],
	[['--repeat-penalty'], (f, v) => (f.repeatPenalty = v)]
] as [string[], Setter][]) {
	for (const name of names) valued[name] = set;
}

const bare: Record<string, Partial<Form>> = {
	'--jinja': { jinja: true },
	'--no-jinja': { jinja: false },
	'--mlock': { mlock: true }
};

/** Owned by the router, so a pasted value would only be refused. */
const ROUTER_OWNED = new Set(['--host', '--port']);

const isFlag = (t: string) => /^-{1,2}[A-Za-z]/.test(t);

export interface ImportResult {
	patch: Partial<Form>;
	/** Words for the extra arguments field. */
	extra: string[];
	/** What was understood but changed or dropped, to tell the user. */
	notes: string[];
}

/**
 * Maps a pasted llama-server command onto the form: known flags to their settings,
 * anything else to the extra arguments, where the API has the last word on it.
 */
export function parseCommandLine(line: string): ImportResult {
	const words = tokenize(line);
	if (words[0] && !isFlag(words[0])) words.shift();
	const patch: Partial<Form> = {};
	const extra: string[] = [];
	const notes: string[] = [];
	for (let i = 0; i < words.length; i++) {
		const word = words[i];
		const eq = word.startsWith('--') ? word.indexOf('=') : -1;
		const name = eq > 0 ? word.slice(0, eq) : word;
		const inline = eq > 0 ? word.slice(eq + 1) : undefined;
		const next = () => {
			if (inline !== undefined) return inline;
			if (i + 1 < words.length && !isFlag(words[i + 1])) return words[++i];
			return undefined;
		};
		if (name in bare) {
			Object.assign(patch, bare[name]);
		} else if (name === '-fa' || name === '--flash-attn') {
			// Older builds took it as a bare switch.
			patch.flashAttn = next() ?? 'on';
		} else if (name === '-lm' || name === LOAD_MODE) {
			const value = next();
			if (value !== undefined && LOCKING_LOAD_MODES.has(value)) patch.mlock = true;
			else extra.push(...(value === undefined ? [name] : [name, value]));
		} else if (name in valued) {
			const value = next();
			if (value === undefined) notes.push(`${name} had no value`);
			else valued[name](patch, value);
		} else if (ROUTER_OWNED.has(name)) {
			next();
			notes.push(`${name} is left out: the router owns it`);
		} else {
			extra.push(word);
			if (inline === undefined && i + 1 < words.length && !isFlag(words[i + 1]))
				extra.push(words[++i]);
		}
	}
	for (const key of ['modelFile', 'mmproj', 'specDraftModel'] as const) {
		const path = patch[key];
		if (path?.startsWith('/') || path?.startsWith('~')) {
			patch[key] = path.split('/').pop() ?? '';
			notes.push(`${path} became ${patch[key]}: paths are relative to the models directory`);
		}
	}
	if (patch.modelFile && !patch.alias) patch.alias = aliasFromFile(patch.modelFile);
	return { patch, extra, notes };
}

/** The vision projector that comes with a model's directory, if any. */
export function mmprojOf(file: string, local: readonly LocalModelFile[]): string {
	const m = local.find((l) => file.startsWith(`${l.path}/`));
	return m?.mmproj ? `${m.path}/${m.mmproj}` : '';
}

/** A new profile's context: past what the model was trained for, slots are capped but memory is not. */
export function defaultCtx(file: string, local: readonly LocalModelFile[]): number {
	const model = local.find((l) => file === l.path || file.startsWith(`${l.path}/`));
	const trained = model?.metadata?.context_length ?? 0;
	return trained > 0 ? Math.min(DEFAULT_CTX, trained) : DEFAULT_CTX;
}
