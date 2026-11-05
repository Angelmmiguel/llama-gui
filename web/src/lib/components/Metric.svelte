<script lang="ts">
	interface Props {
		value: string;
		/** Smaller text after the value. */
		unit?: string;
		/** Value size in px: 56 hero, 40 versions, 28 tiles, 24 KPIs, 22 and 20 side stats. */
		size?: 56 | 40 | 28 | 24 | 22 | 20;
		/** Unit size in px. */
		unitSize?: 12 | 14;
		/** Set the unit in mono, attached to the value, as in `36.5 / 48 GB`. */
		unitMono?: boolean;
		/** CSS color of the value. */
		color?: string;
	}

	let { value, unit, size = 28, unitSize = 12, unitMono = false, color }: Props = $props();

	const tracking = $derived(size >= 40 ? '-0.03em' : size >= 24 ? '-0.02em' : undefined);
</script>

<span class="metric" class:attached={unitMono} class:spread={size === 56}>
	<span
		class="value"
		class:hero={size === 56}
		class:version={size === 40}
		style:font-size="{size}px"
		style:letter-spacing={tracking}
		style:color>{value}</span
	>
	{#if unit}
		<span class="unit" class:mono={unitMono} style:font-size="{unitSize}px">{unit}</span>
	{/if}
</span>

<style>
	.metric {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: var(--space-6);
	}

	.spread {
		gap: var(--space-6) var(--space-18);
	}

	.attached {
		gap: 0.3em;
	}

	.value {
		font-family: var(--font-mono);
	}

	.hero {
		line-height: 1;
	}

	.version {
		line-height: 1.05;
	}

	.unit {
		color: var(--fg-muted);
	}
</style>
