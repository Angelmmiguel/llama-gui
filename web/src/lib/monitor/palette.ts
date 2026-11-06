import type { RouterModel } from '$lib/api/client';

export interface PaletteCommand {
	id: string;
	label: string;
	/** Muted text on the right: what kind of thing this is. */
	hint: string;
	/** Also matched by the filter, never shown. */
	keywords?: string;
}

export const PAGES = [
	{ route: '/', label: 'Overview', keywords: 'home dashboard throughput' },
	{ route: '/slots', label: 'Slots', keywords: 'context kv heatmap' },
	{ route: '/requests', label: 'Requests', keywords: 'latency history ttft' },
	{ route: '/logs', label: 'Logs', keywords: 'events output' },
	{ route: '/playground', label: 'Playground', keywords: 'chat test prompt try completion' },
	{ route: '/library', label: 'Library', keywords: 'models download hugging face gguf' },
	{ route: '/profiles', label: 'Profiles', keywords: 'launch flags settings' },
	{ route: '/builds', label: 'Builds', keywords: 'llama.cpp update version install' },
	{ route: '/bench', label: 'Bench', keywords: 'llama-bench benchmark speed' }
] as const;

export type PageRoute = (typeof PAGES)[number]['route'];

export function pageCommands(): PaletteCommand[] {
	return PAGES.map((p) => ({
		id: `page:${p.route}`,
		label: p.label,
		hint: 'Page',
		keywords: p.keywords
	}));
}

/** Whichever the model's state calls for: anything holding memory can be unloaded. */
export function modelCommands(models: readonly RouterModel[]): PaletteCommand[] {
	return models.map((m) => {
		const unloaded = m.status === 'unloaded' || m.status === '';
		return {
			id: `${unloaded ? 'load' : 'unload'}:${m.id}`,
			label: `${unloaded ? 'Load' : 'Unload'} ${m.id}`,
			hint: m.status ? `Model · ${m.status}` : 'Model',
			keywords: 'model'
		};
	});
}

/** Splits a command's id back into what to do and to what. */
export function parseCommandId(id: string): { kind: string; target: string } {
	const at = id.indexOf(':');
	return at < 0 ? { kind: id, target: '' } : { kind: id.slice(0, at), target: id.slice(at + 1) };
}

/**
 * Keeps the commands holding every word of the query as a substring, in any order and
 * case. A match on the label beats one on the keywords, and one at its start beats both.
 * The sort is stable, so equals keep their order.
 */
export function filterCommands<T extends PaletteCommand>(
	commands: readonly T[],
	query: string
): T[] {
	const words = query.toLowerCase().split(/\s+/).filter(Boolean);
	if (words.length === 0) return [...commands];
	const rank = (c: T): number | null => {
		const label = c.label.toLowerCase();
		const all = `${label} ${(c.keywords ?? '').toLowerCase()} ${c.hint.toLowerCase()}`;
		if (!words.every((w) => all.includes(w))) return null;
		if (label.startsWith(words[0])) return 0;
		return words.every((w) => label.includes(w)) ? 1 : 2;
	};
	return commands
		.map((c) => ({ c, r: rank(c) }))
		.filter((x): x is { c: T; r: number } => x.r !== null)
		.toSorted((a, b) => a.r - b.r)
		.map((x) => x.c);
}

/** The next active row, wrapping around at both ends. */
export function moveActive(active: number, step: number, count: number): number {
	return count > 0 ? (active + step + count) % count : 0;
}
