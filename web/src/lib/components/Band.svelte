<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { HTMLAttributes } from 'svelte/elements';

	interface Props extends HTMLAttributes<HTMLElement> {
		/** Wrapping row (default), vertical stack, or grid. */
		layout?: 'wrap' | 'column' | 'grid';
		/** Grid only: a fixed number of equal columns. */
		columns?: number;
		/** Grid only: auto-fit with this minimum column width in px. */
		min?: number;
		/** Outer radius in px. Mini grids inside a panel use 8. */
		radius?: 8 | 12;
		/** A band inside a band: only the 1 px gaps, no border or radius of its own. */
		nested?: boolean;
		/** CSS `flex` shorthand when the band is itself a flex child. */
		flex?: string;
		as?: 'div' | 'section';
		children: Snippet;
	}

	let {
		layout = 'wrap',
		columns,
		min,
		radius = 12,
		nested = false,
		flex,
		as = 'div',
		children,
		...rest
	}: Props = $props();

	const template = $derived(
		layout !== 'grid'
			? undefined
			: min
				? `repeat(auto-fit, minmax(${min}px, 1fr))`
				: `repeat(${columns ?? 2}, minmax(0, 1fr))`
	);
</script>

<!-- Children paint over the band's background, so only the gaps show it. -->
<svelte:element
	this={as}
	{...rest}
	class={['band', layout, { nested }, rest.class]}
	style:--band-radius="{radius}px"
	style:grid-template-columns={template}
	style:flex
>
	{@render children()}
</svelte:element>

<style>
	.band {
		gap: 1px;
		min-width: 0;
		background: var(--border-panel);
		border: 1px solid var(--border-panel);
		border-radius: var(--band-radius);
		overflow: hidden;
	}

	.wrap {
		display: flex;
		flex-wrap: wrap;
	}

	.column {
		display: flex;
		flex-direction: column;
	}

	.grid {
		display: grid;
	}

	.nested {
		border: none;
		border-radius: 0;
		overflow: visible;
	}

	/* A panel in a band is separated by the gaps, not by its own frame. */
	.band > :global(.panel) {
		border: none;
		/* Over the inline radius Panel always sets. */
		border-radius: 0 !important;
	}
</style>
