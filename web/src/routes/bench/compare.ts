import type { Profile, RouterModel } from '$lib/api/client';

/**
 * Comparing two profiles, for what llama-bench cannot run: it has no speculative decoding, and a
 * request cannot turn that off, so measuring it takes loading the model each way. The model that
 * was in memory goes back to how it was afterwards.
 */
export interface CompareRun {
	alias: string;
	tokensPerSecond: number;
	predicted: number;
	promptMs: number;
	predictedMs: number;
	drafted: number;
	accepted: number;
}

export interface CompareApi {
	models: (signal?: AbortSignal) => Promise<readonly RouterModel[]>;
	load: (alias: string) => Promise<unknown>;
	unload: (alias: string) => Promise<unknown>;
	chat: (body: object, signal: AbortSignal) => Promise<unknown>;
	wait: (ms: number, signal: AbortSignal) => Promise<void>;
}

export const DEFAULT_PROMPT =
	'Write a detailed explanation of how a hash map works, with a short example in Python.';
export const DEFAULT_MAX_TOKENS = 500;
const POLL_MS = 500;
/** A big model on a slow disk can take minutes to load. */
const STATUS_ATTEMPTS = 1200;

/** Nothing to choose from a request's side: the same tokens each way, so only the speed differs. */
export function compareRequest(alias: string, prompt: string, maxTokens: number): object {
	return {
		model: alias,
		messages: [{ role: 'user', content: prompt }],
		stream: false,
		cache_prompt: false,
		temperature: 0,
		seed: 1,
		max_tokens: maxTokens
	};
}

const usesSpeculation = (p: Profile) => Boolean(p.spec_type);

/** A copy to compare against, under a name of its own. Undefined if there is nothing to take off. */
export function baselineOf(
	p: Profile,
	existing: readonly Profile[]
): Omit<Profile, 'id'> | undefined {
	if (!usesSpeculation(p)) return undefined;
	const taken = new Set(existing.map((e) => e.alias));
	let alias = `${p.alias}-no-spec`;
	for (let n = 2; taken.has(alias); n++) alias = `${p.alias}-no-spec-${n}`;
	const copy: Omit<Profile, 'id'> & { id?: string } = {
		...p,
		name: `${p.name} without speculation`,
		alias,
		load_at_start: false
	};
	delete copy.id;
	delete copy.spec_type;
	delete copy.spec_draft_max;
	delete copy.spec_draft_model;
	delete copy.spec_draft_ngl;
	return copy;
}

function statusOf(models: readonly RouterModel[], alias: string): string | undefined {
	return models.find((m) => m.id === alias)?.status;
}

async function waitFor(
	api: CompareApi,
	alias: string,
	wanted: string,
	signal: AbortSignal
): Promise<void> {
	for (let i = 0; i < STATUS_ATTEMPTS; i++) {
		signal.throwIfAborted();
		const models = await api.models(signal);
		const entry = models.find((m) => m.id === alias);
		if (entry?.status === wanted) return;
		if (wanted === 'loaded' && entry?.failed) {
			throw new Error(`${alias} failed to load, exit ${entry.exit_code ?? '?'}.`);
		}
		await api.wait(POLL_MS, signal);
	}
	throw new Error(`${alias} did not become ${wanted} in time.`);
}

export function readRun(alias: string, reply: unknown): CompareRun {
	const t = (reply as { timings?: Record<string, number> } | null)?.timings;
	if (!t?.predicted_per_second) throw new Error(`${alias} answered without timings.`);
	return {
		alias,
		tokensPerSecond: t.predicted_per_second,
		predicted: t.predicted_n ?? 0,
		promptMs: t.prompt_ms ?? 0,
		predictedMs: t.predicted_ms ?? 0,
		drafted: t.draft_n ?? 0,
		accepted: t.draft_n_accepted ?? 0
	};
}

/**
 * Always, and on its own signal: a cancelled comparison must still leave things as they were, and
 * a failure here must not hide why the comparison stopped.
 */
async function restoreModels(
	api: CompareApi,
	aliases: readonly string[],
	wasLoaded: readonly string[]
) {
	const signal = new AbortController().signal;
	try {
		const now = await api.models(signal);
		for (const alias of aliases) {
			const loaded = statusOf(now, alias) === 'loaded';
			if (loaded && !wasLoaded.includes(alias)) await api.unload(alias);
			if (!loaded && wasLoaded.includes(alias)) await api.load(alias);
		}
	} catch {
		// Whatever is left is on the dock, and the comparison's own result still stands.
	}
}

/** Runs the same request through each profile in turn, with only one of them in memory. */
export async function runComparison(
	api: CompareApi,
	aliases: readonly [string, string],
	prompt: string,
	maxTokens: number,
	signal: AbortSignal,
	onstep: (step: string) => void
): Promise<CompareRun[]> {
	const before = await api.models(signal);
	const wasLoaded = aliases.filter((a) => statusOf(before, a) === 'loaded');
	const unload = async (alias: string) => {
		const models = await api.models(signal);
		if (statusOf(models, alias) === 'unloaded') return;
		await api.unload(alias);
		await waitFor(api, alias, 'unloaded', signal);
	};
	const runs: CompareRun[] = [];
	try {
		for (const alias of aliases) {
			onstep(`Making room for ${alias}`);
			for (const other of aliases) if (other !== alias) await unload(other);
			onstep(`Loading ${alias}`);
			if (statusOf(await api.models(signal), alias) !== 'loaded') {
				await api.load(alias);
				await waitFor(api, alias, 'loaded', signal);
			}
			onstep(`Generating with ${alias}`);
			runs.push(readRun(alias, await api.chat(compareRequest(alias, prompt, maxTokens), signal)));
		}
	} finally {
		await restoreModels(api, aliases, wasLoaded);
		onstep('');
	}
	return runs;
}

/** How the second run compares with the first, in words. Honest about it being a single run. */
export function verdict(runs: readonly CompareRun[]): string {
	if (runs.length !== 2 || runs[0].tokensPerSecond <= 0) return '';
	const [first, second] = runs;
	const change = (second.tokensPerSecond / first.tokensPerSecond - 1) * 100;
	const size = Math.abs(Math.round(change));
	if (size < 1) return `${second.alias} and ${first.alias} ran at the same speed.`;
	return `${second.alias} was ${size}% ${change > 0 ? 'faster' : 'slower'} than ${first.alias} on this prompt.`;
}

export const acceptedShare = (r: CompareRun) =>
	r.drafted > 0 ? `${Math.round((r.accepted / r.drafted) * 100)}%` : '—';
