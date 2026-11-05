<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		/** The heading text. Write caps in the source: the design mixes them, as in `LAST 60 s`. */
		title: string;
		/** Target for the panel's `aria-labelledby`. */
		id?: string;
		as?: 'h2' | 'h3';
		/** Sans 13 px line under the title. */
		subtitle?: string;
		/** Mono 11 px note on the right, such as `last 60 s`. */
		note?: string;
		/** Right side: Legend, links, buttons. */
		actions?: Snippet;
		align?: 'center' | 'baseline';
	}

	let { title, id, as = 'h2', subtitle, note, actions, align = 'center' }: Props = $props();
</script>

<div class="heading" style:align-items={align}>
	<div class="text">
		<svelte:element this={as} {id} class="title">{title}</svelte:element>
		{#if subtitle}<span class="subtitle">{subtitle}</span>{/if}
	</div>
	{#if note}<span class="note">{note}</span>{/if}
	{#if actions}{@render actions()}{/if}
</div>

<style>
	.heading {
		display: flex;
		flex-wrap: wrap;
		justify-content: space-between;
		gap: var(--space-10);
	}

	.text {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.title {
		margin: 0;
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		font-weight: 500;
		letter-spacing: var(--tracking-caps);
		color: var(--fg-muted);
	}

	.subtitle {
		font-size: var(--fs-13);
		color: var(--fg-secondary);
	}

	.note {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-faint);
	}
</style>
