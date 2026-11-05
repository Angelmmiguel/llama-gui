<script lang="ts">
	export interface LegendItem {
		label: string;
		/** CSS color. */
		color?: string;
		/**
		 * `square` 10 px (default), `dot` round, `wide` 14×10, `queued` stripes 14×10,
		 * `outline` an empty square in `color`, `filled-outline` a square with a ghost border,
		 * `ring` a hollow circle, `line` a 16 px stroke, `marker` a 2×12 tick, `plain` no swatch.
		 */
		shape?:
			| 'square'
			| 'dot'
			| 'wide'
			| 'queued'
			| 'outline'
			| 'filled-outline'
			| 'ring'
			| 'line'
			| 'marker'
			| 'plain';
		/** Dot only: size in px, for smaller "other" marks. */
		size?: number;
		/** Line only: dashed stroke. */
		dashed?: boolean;
		/** Mono figure: after the label, or right-aligned in a `stack`. */
		value?: string;
	}

	interface Props {
		items: readonly LegendItem[];
		/** Gap between items in px. */
		gap?: 12 | 14 | 16;
		/** One item per line with values at the right edge, as on the Library's disk panel. */
		stack?: boolean;
	}

	let { items, gap = 14, stack = false }: Props = $props();
</script>

<div class="legend" class:stack style:gap={stack ? undefined : `${gap}px`}>
	{#each items as item, i (i)}
		{@const shape = item.shape ?? 'square'}
		<span class="item">
			<span class="name">
				{#if shape === 'line'}
					<span
						class="swatch line"
						style:border-top={`2px ${item.dashed ? 'dashed' : 'solid'} ${item.color}`}
					></span>
				{:else if shape === 'outline' || shape === 'ring'}
					<span class="swatch {shape}" style:border-color={item.color}></span>
				{:else if shape === 'queued'}
					<span class="swatch wide queued"></span>
				{:else if shape !== 'plain'}
					<span
						class="swatch {shape}"
						style:background={item.color}
						style:width={item.size ? `${item.size}px` : undefined}
						style:height={item.size ? `${item.size}px` : undefined}
					></span>
				{/if}
				{item.label}{#if item.value && !stack}&nbsp;· <span class="mono">{item.value}</span>{/if}
			</span>
			{#if item.value && stack}<span class="mono">{item.value}</span>{/if}
		</span>
	{/each}
</div>

<style>
	.legend {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		font-size: var(--fs-12);
		color: var(--fg-tertiary);
	}

	.stack {
		flex-direction: column;
		flex-wrap: nowrap;
		align-items: stretch;
		gap: var(--space-6);
	}

	.item {
		display: flex;
		justify-content: space-between;
	}

	.name {
		display: flex;
		align-items: center;
		gap: var(--space-6);
	}

	.swatch {
		flex: none;
		width: 10px;
		height: 10px;
		border-radius: var(--radius-1);
		box-sizing: border-box;
	}

	.dot {
		border-radius: var(--radius-round);
	}

	.wide {
		width: 14px;
	}

	.outline {
		border: 1px solid var(--border-free);
	}

	.filled-outline {
		border: 1px solid var(--border-ghost);
	}

	.ring {
		border: 2px solid var(--fg-bright);
		border-radius: var(--radius-round);
	}

	.line {
		width: 16px;
		height: 0;
		border-radius: 0;
	}

	.marker {
		width: 2px;
		height: 12px;
		border-radius: 0;
	}
</style>
