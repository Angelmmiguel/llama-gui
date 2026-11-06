import type {
	Gpu,
	LocalModel,
	MemoryDevice,
	Profile,
	RouterModel,
	TelemetryModel
} from '$lib/api/client';
import { formatBytes } from '$lib/format';
import { acceptanceLabel, quantOf, speculationName } from './overview';

/** A model that holds memory, one cell of the dock. */
export interface DockCell {
	id: string;
	quant?: string;
	loading: boolean;
	sleeping: boolean;
	busy: number;
	slots: number;
	memory: number;
	role: string;
	/** On disk, for a model still loading: upstream reports no progress to show instead. */
	fileBytes?: number;
	/** Such as MTP, when the model drafts tokens. */
	speculation?: string;
	/** The share of its drafts it kept since loading. */
	acceptance?: string;
	error?: string;
}

/** A model that could be loaded, one row of the drawer and the empty panel. */
export interface DiskRow {
	key: string;
	name: string;
	/** Quantisation, size on disk and role. */
	meta: string;
	/** Its alias at the router; absent until a profile makes it loadable. */
	profile?: string;
	verdict: string;
	level: 'ok' | 'warn' | 'error';
	/** The model to load, or absent when it needs a profile first. */
	load?: string;
	/** An idle model to unload first, to make room. */
	unloadFirst?: string;
	/** The model's path, for making a profile. */
	path?: string;
	disabled: boolean;
}

export interface FreeMemory {
	bytes: number;
	/** VRAM with a GPU, otherwise RAM. */
	kind: 'VRAM' | 'RAM';
}

const RESIDENT = new Set(['loaded', 'loading', 'sleeping']);

export const isResident = (status: string) => RESIDENT.has(status);

function profileOf(id: string, profiles: readonly Profile[]) {
	return profiles.find((p) => p.alias === id);
}

function localOf(file: string | undefined, local: readonly LocalModel[]) {
	if (!file) return undefined;
	return local.find((l) => file === l.path || file.startsWith(`${l.path}/`));
}

/** What a model is for, as far as its profile says. Unknown output never gets a guess. */
export function roleOf(profile: Profile | undefined): string {
	if (!profile) return 'model';
	const args = profile.extra_args?.join(' ') ?? '';
	if (/--embeddings?\b/.test(args)) return 'embeddings';
	if (/--rerank(ing)?\b/.test(args)) return 'reranking';
	return profile.mmproj ? 'vision' : 'chat';
}

/** Free memory where models go: summed across GPUs, as a model can be split over them. */
export function freeMemory(gpus: readonly Gpu[], ramAvailable: number): FreeMemory {
	const known = gpus.filter((g) => g.memory_total !== null && g.memory_used !== null);
	if (known.length === 0) return { bytes: ramAvailable, kind: 'RAM' };
	const bytes = known.reduce((sum, g) => sum + (g.memory_total ?? 0) - (g.memory_used ?? 0), 0);
	return { bytes: Math.max(0, bytes), kind: 'VRAM' };
}

export function dockCells(
	models: readonly TelemetryModel[],
	profiles: readonly Profile[],
	local: readonly LocalModel[],
	/** Asked to load but not yet reported as loading, so the cell appears at once. */
	pending: ReadonlySet<string> = new Set()
): DockCell[] {
	return models
		.filter((m) => isResident(m.status) || pending.has(m.id))
		.toSorted((a, b) => b.memory.total - a.memory.total || a.id.localeCompare(b.id))
		.map((m) => {
			const profile = profileOf(m.id, profiles);
			return {
				id: m.id,
				quant: quantOf(profile?.model_file),
				loading: m.status === 'loading' || !isResident(m.status),
				sleeping: m.status === 'sleeping',
				busy: m.slots.filter((s) => s.state !== 'idle').length,
				slots: m.slots.length,
				memory: m.memory.total,
				role: roleOf(profile),
				fileBytes: localOf(profile?.model_file, local)?.size_bytes,
				speculation: m.speculation && speculationName(m.speculation.types),
				acceptance: m.speculation && acceptanceLabel(m.speculation.accepted, m.speculation.drafted),
				error: m.error
			};
		});
}

function deviceBytes(devices: readonly MemoryDevice[], kind: FreeMemory['kind']): number {
	return devices
		.filter((d) => kind === 'RAM' || d.kind === 'device')
		.reduce((sum, d) => sum + d.model + d.context + d.compute, 0);
}

/** Only a verdict that the model fits or never can leaves no way to run part of it on the CPU. */
const NO_PARTIAL = new Set(['fits_gpu', 'too_large']);

interface VerdictInput {
	need: number;
	free: FreeMemory;
	headroom: number;
	fit: string | undefined;
	models: readonly TelemetryModel[];
}

function verdictOf({ need, free, headroom, fit, models }: VerdictInput) {
	const available = Math.max(0, free.bytes - headroom);
	if (need <= 0) return { verdict: 'Memory need not known yet', level: 'ok' as const };
	if (need <= available) {
		return {
			verdict: `Fits · needs ${formatBytes(need)} of ${formatBytes(available)} free`,
			level: 'ok' as const
		};
	}
	const swap = models
		.filter((m) => m.status === 'loaded' && m.slots.every((s) => s.state === 'idle'))
		.toSorted((a, b) => a.memory.total - b.memory.total)
		.find((m) => need <= available + m.memory.total);
	if (swap) {
		return {
			verdict: `Needs ${formatBytes(need)} · will unload ${swap.id}`,
			level: 'warn' as const,
			unloadFirst: swap.id
		};
	}
	if (free.kind === 'VRAM' && !(fit && NO_PARTIAL.has(fit))) {
		return {
			verdict: `Needs ${formatBytes(need)} · part may run on the CPU`,
			level: 'warn' as const
		};
	}
	return {
		verdict: `Needs ${formatBytes(need)} · not enough ${free.kind} right now`,
		level: 'error' as const,
		disabled: true
	};
}

const LEVELS = { ok: 0, warn: 1, error: 2 };

export function diskRows(input: {
	models: readonly TelemetryModel[];
	estimates: readonly RouterModel[];
	profiles: readonly Profile[];
	local: readonly LocalModel[];
	free: FreeMemory;
	/** Kept free on purpose, by the router's memory margin. */
	headroom: number;
	pending?: ReadonlySet<string>;
}): DiskRow[] {
	const { models, estimates, profiles, local, free, headroom, pending } = input;
	const rows: DiskRow[] = [];
	for (const m of models) {
		if (isResident(m.status) || pending?.has(m.id)) continue;
		const profile = profileOf(m.id, profiles);
		const file = localOf(profile?.model_file, local);
		const estimate = estimates.find((e) => e.id === m.id);
		const measured = deviceBytes(estimate?.memory.max.devices ?? [], free.kind);
		const need = measured || (file?.fit.total_bytes ?? 0);
		const quant = file?.quant || quantOf(profile?.model_file);
		const verdict = verdictOf({ need, free, headroom, fit: file?.fit.verdict, models });
		rows.push({
			key: `model:${m.id}`,
			name: file?.name ?? m.id,
			meta: [quant, file ? `${formatBytes(file.size_bytes)} on disk` : undefined, roleOf(profile)]
				.filter(Boolean)
				.join(' · '),
			profile: m.id,
			load: m.id,
			disabled: false,
			...verdict,
			...(estimate?.failed && {
				verdict: `Failed to load last time, exit ${estimate.exit_code ?? '?'}`,
				level: 'error' as const,
				disabled: false
			})
		});
	}
	for (const file of local) {
		if (profiles.some((p) => localOf(p.model_file, [file]))) continue;
		rows.push({
			key: `file:${file.path}`,
			name: file.name,
			meta: [file.quant, `${formatBytes(file.size_bytes)} on disk`].filter(Boolean).join(' · '),
			path: file.path,
			disabled: false,
			...verdictOf({
				need: file.fit.total_bytes,
				free,
				headroom,
				fit: file.fit.verdict,
				models: []
			})
		});
	}
	return rows.toSorted((a, b) => LEVELS[a.level] - LEVELS[b.level] || a.name.localeCompare(b.name));
}
