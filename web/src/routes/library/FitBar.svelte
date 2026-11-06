<script lang="ts">
	import type { LibraryFit } from '$lib/api/client';
	import StackedBar from '$lib/components/StackedBar.svelte';
	import { fitSegments, fitTotal, verdictColor, verdictLabel, type FitScale } from './view';

	interface Props {
		fit: LibraryFit;
		scale: FitScale | null;
		/** Before the total on the first line, such as the quantisation. */
		prefix?: string;
		height?: number;
		/** Only the bar, for a table row that shows the verdict in its own column. */
		bare?: boolean;
	}

	let { fit, scale, prefix, height = 10, bare = false }: Props = $props();

	const total = $derived(fitTotal(fit));
	const verdict = $derived(verdictLabel(fit));
</script>

<span class="fit">
	<StackedBar
		segments={fitSegments(fit, scale)}
		{height}
		transition="width 0.5s ease"
		marker={scale?.marker}
		label={`${total}: ${verdict}`}
	/>
	{#if !bare}
		<span class="caption">
			<span class="total">{prefix ? `${prefix} · ` : ''}{total}</span>
			<span style:color={verdictColor(fit)}>{verdict}</span>
		</span>
	{/if}
</span>

<style>
	.fit {
		display: flex;
		flex-direction: column;
		gap: var(--space-6);
		min-width: 0;
	}

	.caption {
		display: flex;
		flex-wrap: wrap;
		justify-content: space-between;
		gap: var(--space-2) var(--space-6);
		font-family: var(--font-mono);
		font-size: var(--fs-11);
	}

	.total {
		color: var(--fg-secondary);
	}
</style>
