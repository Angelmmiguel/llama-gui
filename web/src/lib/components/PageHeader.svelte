<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		/** The h1, also used for the document title. */
		title: string;
		/** A string, or a snippet when part of it is mono. */
		subtitle?: string | Snippet;
		/** Right side: buttons, Segmented, LiveToggle. */
		actions?: Snippet;
	}

	let { title, subtitle, actions }: Props = $props();
</script>

<svelte:head><title>{title} · llama-gui</title></svelte:head>

<div class="header">
	<div class="text">
		<h1>{title}</h1>
		{#if typeof subtitle === 'function'}
			<span class="subtitle">{@render subtitle()}</span>
		{:else if subtitle}
			<span class="subtitle">{subtitle}</span>
		{/if}
	</div>
	{#if actions}
		<div class="actions">{@render actions()}</div>
	{/if}
</div>

<style>
	.header {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		justify-content: space-between;
		gap: var(--space-12);
	}

	.text {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	h1 {
		margin: 0;
		font-size: var(--fs-22);
		font-weight: 600;
		letter-spacing: -0.015em;
	}

	.subtitle {
		font-size: var(--fs-13);
		color: var(--fg-muted);
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-8);
	}
</style>
