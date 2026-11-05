<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { MouseEventHandler } from 'svelte/elements';

	interface Props {
		pressed?: boolean;
		/**
		 * The free-standing filter toggles: `pill` round, solid when on (Library filters);
		 * `source` colored outline when on (Logs sources); `option` raised when on (Bench).
		 */
		variant?: 'pill' | 'source' | 'option';
		/** `source` only: CSS color when pressed. */
		color?: string;
		onclick?: MouseEventHandler<HTMLButtonElement>;
		children: Snippet;
	}

	let { pressed = false, variant = 'option', color, onclick, children }: Props = $props();
</script>

<button
	type="button"
	class="chip {variant}"
	class:on={pressed}
	aria-pressed={pressed}
	{onclick}
	style:--chip-color={color}
>
	{@render children()}
</button>

<style>
	.chip {
		min-height: 32px;
		background: transparent;
		font-size: var(--fs-12);
	}

	.pill {
		padding: 0 var(--space-12);
		border-radius: var(--radius-pill);
		border: 1px solid var(--border-ghost);
		color: var(--fg-secondary);
	}

	.pill.on {
		background: var(--fg-primary);
		border-color: var(--fg-primary);
		color: var(--fg-on-accent);
	}

	.source {
		padding: 0 var(--space-10);
		border-radius: var(--radius-4);
		border: 1px solid var(--border-ghost);
		color: var(--fg-off);
		font-family: var(--font-mono);
	}

	.source.on {
		border-color: var(--chip-color, var(--fg-primary));
		color: var(--chip-color, var(--fg-primary));
	}

	.option {
		min-width: 44px;
		padding: 0 var(--space-10);
		border-radius: var(--radius-4);
		border: 1px solid var(--border-control);
		color: var(--fg-muted);
		font-family: var(--font-mono);
	}

	.option.on {
		background: var(--bg-segment-on);
		border-color: var(--border-raised);
		color: var(--fg-bright);
	}
</style>
