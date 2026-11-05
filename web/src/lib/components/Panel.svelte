<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { HTMLAttributes } from 'svelte/elements';

	interface Props extends HTMLAttributes<HTMLElement> {
		as?: 'section' | 'aside' | 'article' | 'nav' | 'div';
		/** `panel` #0F1114 (default), `inset` #0C0E11 for callouts in a panel, `deep` #08090B for logs. */
		tone?: 'panel' | 'inset' | 'deep';
		/** CSS padding. Default `20px 22px`. */
		padding?: string;
		/** Gap between children in px. */
		gap?: number;
		/** Radius in px. Inset callouts use 8 or 10. */
		radius?: 8 | 10 | 12;
		/** CSS `flex` shorthand when placed in a Band or Columns, e.g. `3 1 640px`. */
		flex?: string;
		/** Lay children out in a wrapping row instead of a column. */
		row?: boolean;
		children: Snippet;
	}

	let {
		as = 'section',
		tone = 'panel',
		padding = '20px 22px',
		gap = 14,
		radius = 12,
		flex,
		row = false,
		children,
		...rest
	}: Props = $props();
</script>

<svelte:element
	this={as}
	{...rest}
	class={['panel', `tone-${tone}`, { row }, rest.class]}
	style:padding
	style:gap="{gap}px"
	style:border-radius="{radius}px"
	style:flex
>
	{@render children()}
</svelte:element>

<style>
	.panel {
		min-width: 0;
		display: flex;
		flex-direction: column;
		border: 1px solid var(--border-panel);
	}

	.row {
		flex-flow: row wrap;
		align-items: center;
		justify-content: space-between;
	}

	.tone-panel {
		background: var(--bg-panel);
	}

	.tone-inset {
		background: var(--bg-inset);
	}

	.tone-deep {
		background: var(--bg-deep);
	}
</style>
