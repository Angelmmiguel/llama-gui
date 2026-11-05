<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { MouseEventHandler } from 'svelte/elements';

	interface Props {
		selected?: boolean;
		/** `card` is the Slots map card with its amber ring; `compact` the Builds backend choice. */
		variant?: 'card' | 'compact';
		disabled?: boolean;
		onclick?: MouseEventHandler<HTMLButtonElement>;
		children: Snippet;
	}

	let { selected = false, variant = 'card', disabled = false, onclick, children }: Props = $props();
</script>

<button type="button" class={variant} class:selected aria-pressed={selected} {disabled} {onclick}>
	{@render children()}
</button>

<style>
	button {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		text-align: left;
		color: var(--fg-primary);
	}

	.card {
		gap: var(--space-12);
		padding: var(--space-14);
		border: 1px solid var(--border-panel);
		border-radius: var(--radius-6);
		background: var(--bg-inset);
		font: inherit;
	}

	.card.selected {
		border-color: var(--color-decode);
		box-shadow: 0 0 0 1px var(--color-decode);
	}

	.compact {
		gap: var(--space-2);
		min-height: 54px;
		padding: var(--space-8) var(--space-12);
		border: 1px solid var(--border-control);
		border-radius: var(--radius-5);
		background: transparent;
		font-size: var(--fs-13);
	}

	.compact.selected {
		background: var(--bg-selected);
		border-color: var(--color-decode);
	}

	.compact:disabled {
		color: var(--fg-off);
		opacity: 0.6;
	}
</style>
