<script lang="ts">
	interface Props {
		labels: readonly string[];
		/** `x` runs left to right under a chart; `y` runs top to bottom beside it. */
		direction?: 'x' | 'y';
		/** y only: the chart's height in px. */
		height?: number;
		/** y only: the gutter's width in px. */
		width?: number;
	}

	let { labels, direction = 'x', height, width = 28 }: Props = $props();
</script>

<div
	aria-hidden="true"
	class="axis {direction}"
	style:height={direction === 'y' && height ? `${height}px` : undefined}
	style:width={direction === 'y' ? `${width}px` : undefined}
>
	{#each labels as label, i (i)}
		<span>{label}</span>
	{/each}
</div>

<style>
	.axis {
		display: flex;
		justify-content: space-between;
		font-family: var(--font-mono);
		font-size: var(--fs-10);
		color: var(--fg-faint);
	}

	.y {
		flex: none;
		flex-direction: column;
		text-align: right;
	}
</style>
