<script lang="ts">
	import AxisLabels from './AxisLabels.svelte';
	import { chartPaths } from './chart';
	import { tones } from './tones';

	export interface ChartSeries {
		data: readonly number[];
		/** CSS color. */
		color?: string;
		/** Stroke width in px. Default 1.75. */
		width?: number;
		/** SVG dash array, e.g. `5 4`. */
		dash?: string;
		/** Fill under the line at 10%. */
		area?: boolean;
		/** Dot at the newest point. */
		endDot?: boolean;
		/** Positions across the width, when the line may stop short of them. */
		slots?: number;
	}

	interface Props {
		series: readonly ChartSeries[];
		/** Spoken description; the chart is `role="img"`. */
		label: string;
		min?: number;
		/** Top of the scale. Defaults to the data's maximum. */
		max?: number;
		/** Height in px. */
		height?: number;
		/** Top to bottom. Also sets the grid: one line per label. */
		yLabels?: readonly string[];
		/** Left to right. */
		xLabels?: readonly string[];
		/** Width of the y label gutter in px. */
		yWidth?: number;
		/** Draw the topmost grid line. Bench's chart leaves it out. */
		gridTop?: boolean;
	}

	let {
		series,
		label,
		min = 0,
		max,
		height = 170,
		yLabels = [],
		xLabels = [],
		yWidth = 28,
		gridTop = true
	}: Props = $props();

	const VIEW_WIDTH = 600;

	const top = $derived(max ?? Math.max(min + 1, ...series.flatMap((s) => s.data)));
	const drawn = $derived(
		series.map((s) => ({ ...s, paths: chartPaths(s.data, VIEW_WIDTH, height, min, top, s.slots) }))
	);
	// Half a unit in from each edge, or the outermost lines would be clipped.
	const grid = $derived.by(() => {
		const n = Math.max(yLabels.length, 2);
		return Array.from({ length: n }, (_, i) => ({
			y: i === 0 ? 0.5 : i === n - 1 ? height - 0.5 : (i / (n - 1)) * height,
			axis: i === n - 1,
			hidden: i === 0 && !gridTop
		}));
	});
</script>

<div class="chart">
	{#if yLabels.length > 0}
		<AxisLabels direction="y" labels={yLabels} {height} width={yWidth} />
	{/if}
	<div class="plot">
		<div class="canvas" style:height="{height}px">
			<svg
				role="img"
				aria-label={label}
				viewBox="0 0 {VIEW_WIDTH} {height}"
				preserveAspectRatio="none"
			>
				{#each grid as line, i (i)}
					{#if !line.hidden}
						<line
							x1="0"
							y1={line.y}
							x2={VIEW_WIDTH}
							y2={line.y}
							stroke={line.axis ? 'var(--chart-axis)' : 'var(--chart-grid)'}
							vector-effect="non-scaling-stroke"
						/>
					{/if}
				{/each}
				{#each drawn as s, i (i)}
					{#if s.area}
						<path d={s.paths.area} fill={s.color ?? tones.decode} fill-opacity="0.10" />
					{/if}
					<path
						d={s.paths.line}
						fill="none"
						stroke={s.color ?? tones.decode}
						stroke-width={s.width ?? 1.75}
						stroke-dasharray={s.dash}
						stroke-linejoin="round"
						vector-effect="non-scaling-stroke"
					/>
				{/each}
			</svg>
			<!-- HTML rather than an SVG circle, which the stretched viewBox would turn into an ellipse. -->
			{#each drawn as s, i (i)}
				{#if s.endDot && s.paths.line}
					<span
						class="dot"
						style:top="{(s.paths.endY / height) * 100}%"
						style:background={s.color ?? tones.decode}
					></span>
				{/if}
			{/each}
		</div>
		{#if xLabels.length > 0}
			<AxisLabels labels={xLabels} />
		{/if}
	</div>
</div>

<style>
	.chart {
		display: flex;
		gap: var(--space-10);
	}

	.plot {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-6);
	}

	.canvas {
		position: relative;
	}

	svg {
		width: 100%;
		height: 100%;
		display: block;
	}

	.dot {
		position: absolute;
		left: 100%;
		width: 7px;
		height: 7px;
		border-radius: var(--radius-round);
		transform: translate(-50%, -50%);
	}
</style>
