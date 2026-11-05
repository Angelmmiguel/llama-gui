/** CSS colors for the `color` props of data components, so pages never write a literal. */
export const tones = {
	decode: 'var(--color-decode)',
	prefill: 'var(--color-prefill)',
	cached: 'var(--color-cached)',
	ok: 'var(--color-ok)',
	warn: 'var(--color-warn)',
	error: 'var(--color-error)',
	errorSoft: 'var(--color-error-soft)',
	weights: 'var(--color-weights)',
	compute: 'var(--color-compute)',
	other: 'var(--color-other)',
	idle: 'var(--color-idle)',
	off: 'var(--color-off)',
	primary: 'var(--fg-primary)',
	secondary: 'var(--fg-secondary)',
	muted: 'var(--fg-muted)',
	faint: 'var(--fg-faint)',
	empty: 'var(--border-ghost)'
} as const;

/** Log source colors. Unknown sources fall back to muted. */
export function sourceColor(source: string): string {
	switch (source.trim()) {
		case 'slot':
			return tones.prefill;
		case 'srv':
			return tones.cached;
		case 'router':
			return tones.decode;
		case 'ggml':
			return 'var(--color-source-ggml)';
		default:
			return tones.muted;
	}
}
