<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { MouseEventHandler } from 'svelte/elements';

	interface Props {
		/** CSS grid-template-columns, e.g. `150px 104px minmax(0, 1fr) 150px`. */
		columns: string;
		/** Column gap in px. */
		gap?: number;
		/** CSS padding. Default `12px 0`. */
		padding?: string;
		minHeight?: number;
		/** The mono 10 px column-label row above a list: no divider, hidden from assistive tech. */
		header?: boolean;
		/** Renders a real button with `aria-pressed`, for rows that select a detail view. */
		onclick?: MouseEventHandler<HTMLButtonElement>;
		/** Selected look: raised background with a 2 px amber edge. */
		selected?: boolean;
		as?: 'div' | 'li';
		children: Snippet;
	}

	let {
		columns,
		gap = 14,
		padding = '12px 0',
		minHeight,
		header = false,
		onclick,
		selected = false,
		as = 'div',
		children
	}: Props = $props();
</script>

{#if onclick}
	<button
		type="button"
		class="row pick"
		class:selected
		aria-pressed={selected}
		{onclick}
		style:grid-template-columns={columns}
		style:gap="{gap}px"
		style:padding
		style:min-height={minHeight ? `${minHeight}px` : undefined}
	>
		{@render children()}
	</button>
{:else}
	<svelte:element
		this={as}
		class={['row', { header }]}
		aria-hidden={header ? 'true' : undefined}
		style:grid-template-columns={columns}
		style:gap="{gap}px"
		style:padding
		style:min-height={minHeight ? `${minHeight}px` : undefined}
	>
		{@render children()}
	</svelte:element>
{/if}

<style>
	.row {
		display: grid;
		align-items: center;
		border-top: 1px solid var(--border-row);
	}

	.header {
		border-top: none;
		font-family: var(--font-mono);
		font-size: var(--fs-10);
		letter-spacing: var(--tracking-column);
		color: var(--fg-faint);
	}

	.pick {
		width: 100%;
		border: none;
		border-top: 1px solid var(--border-row);
		border-radius: var(--radius-4);
		background: transparent;
		text-align: left;
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		color: var(--fg-primary);
	}

	.selected {
		background: var(--bg-selected);
		box-shadow: inset 2px 0 0 var(--color-decode);
	}
</style>
