<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		/** `neutral` bordered mono (quant), `ok` green bordered mono (LOADED, ACTIVE), `soft` filled sans (model tags). */
		tone?: 'neutral' | 'ok' | 'soft';
		/** `md` is the larger ACTIVE badge. */
		size?: 'sm' | 'md';
		children: Snippet;
	}

	let { tone = 'neutral', size = 'sm', children }: Props = $props();
</script>

<span class="tag {tone} {size}">{@render children()}</span>

<style>
	.tag {
		flex: none;
		border-radius: var(--radius-3);
		font-family: var(--font-mono);
		font-size: var(--fs-10);
		padding: 1px 5px;
		color: var(--fg-secondary);
	}

	.neutral {
		border: 1px solid var(--border-ghost);
	}

	.ok {
		color: var(--color-ok);
		border: 1px solid var(--border-ok);
	}

	.md {
		font-size: var(--fs-11);
		padding: 3px 8px;
	}

	.soft {
		font-family: var(--font-sans);
		font-size: var(--fs-11);
		padding: 1px 6px;
		background: var(--track);
	}
</style>
