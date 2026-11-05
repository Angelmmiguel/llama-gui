<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		title: string;
		/** Second line in the faint color. */
		detail?: string;
		actions?: Snippet;
	}

	let { title, detail, actions }: Props = $props();
</script>

<div class="empty">
	<span>{title}</span>
	{#if detail}<span class="detail">{detail}</span>{/if}
	{#if actions}<div class="actions">{@render actions()}</div>{/if}
</div>

<style>
	.empty {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: var(--space-8);
		padding: var(--space-24) var(--space-16);
		font-family: var(--font-sans);
		font-size: var(--fs-13);
		color: var(--fg-muted);
	}

	.detail {
		font-size: var(--fs-12);
		color: var(--fg-faint);
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-8);
	}
</style>
