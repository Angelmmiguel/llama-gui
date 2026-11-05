<script lang="ts">
	import Metric from './Metric.svelte';
	import Sparkline from './Sparkline.svelte';
	import { tones } from './tones';

	interface Props {
		/** Mono caps label, written in caps. */
		label: string;
		value: string;
		unit?: string;
		/** Line under the value: mono with a sparkline, sans without. */
		note?: string;
		/** With data, the 28 px sparkline tile of Overview; without, the 24 px KPI tile of Requests. */
		data?: readonly number[];
		/** CSS color of the sparkline. */
		color?: string;
		min?: number;
		max?: number;
	}

	let { label, value, unit, note, data, color = tones.secondary, min, max }: Props = $props();
</script>

<!-- Tiles carry no frame: place them in a Band. -->
<div class="tile" class:spark={data !== undefined}>
	<span class="label">{label}</span>
	<Metric {value} {unit} size={data ? 28 : 24} />
	{#if data}
		<div class="chart"><Sparkline {data} {color} {min} {max} /></div>
	{/if}
	{#if note}<span class="note">{note}</span>{/if}
</div>

<style>
	.tile {
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-6);
		padding: var(--space-16) var(--space-18);
		background: var(--bg-panel);
	}

	.spark {
		gap: var(--space-8);
		padding: var(--space-18) var(--space-18) var(--space-14);
	}

	.label {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		letter-spacing: var(--tracking-caps);
		color: var(--fg-muted);
	}

	.chart {
		margin-top: auto;
	}

	.note {
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}

	.spark .note {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
	}
</style>
