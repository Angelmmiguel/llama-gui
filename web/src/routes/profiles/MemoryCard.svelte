<script lang="ts">
	import type { ProfileEstimate } from '$lib/api/client';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import Metric from '$lib/components/Metric.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import StackedBar from '$lib/components/StackedBar.svelte';
	import { memoryView } from './view';

	interface Props {
		estimate: ProfileEstimate | null;
		/** Why there is no estimate. */
		reason: string;
		ctk: string;
		ctv: string;
	}

	let { estimate, reason, ctk, ctv }: Props = $props();

	const view = $derived(estimate ? memoryView(estimate, ctk, ctv) : null);
</script>

<Panel aria-labelledby="pv-h">
	<SectionHeading id="pv-h" title="MEMORY IMPACT" />
	{#if view}
		<div class="head">
			<Metric value={view.headline} unit={view.headlineUnit} size={28} unitSize={12} />
			<span class="verdict" style:color={view.verdictColor}>{view.verdict}</span>
		</div>
		<span class="source">{view.source}</span>
		<div class="bars">
			{#each view.bars as bar (bar.label)}
				<span class="bar-label">{bar.label}</span>
				<StackedBar segments={bar.segments} height={14} transition="width .3s" label={bar.label} />
			{/each}
		</div>
		<div class="rows">
			{#each view.rows as row (row.label)}
				<span class="row">
					<span class="row-label">{row.label}</span>
					<span class:warn={row.warn}>{row.value}</span>
				</span>
			{/each}
		</div>
		{#if view.caveat}<span class="source">{view.caveat}</span>{/if}
	{:else}
		<EmptyState title="No estimate yet" detail={reason} />
	{/if}
</Panel>

<style>
	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		justify-content: space-between;
		gap: var(--space-8);
	}

	.verdict {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}

	.source,
	.bar-label {
		font-size: var(--fs-11);
		color: var(--fg-muted);
	}

	.bars {
		display: flex;
		flex-direction: column;
		gap: var(--space-6);
	}

	.bar-label:not(:first-child) {
		padding-top: var(--space-6);
	}

	.rows {
		display: flex;
		flex-direction: column;
		gap: var(--space-6);
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}

	.row {
		display: flex;
		justify-content: space-between;
		gap: var(--space-8);
	}

	.row-label {
		color: var(--fg-muted);
	}

	.warn {
		color: var(--color-warn);
	}
</style>
