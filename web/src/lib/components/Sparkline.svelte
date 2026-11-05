<script lang="ts">
	import { chartPaths } from './chart';
	import { tones } from './tones';

	interface Props {
		data: readonly number[];
		/** CSS color for the line and its 12% fill. */
		color?: string;
		min?: number;
		max?: number;
		height?: number;
	}

	let { data, color = tones.secondary, min, max, height = 32 }: Props = $props();

	const paths = $derived(chartPaths(data, 120, height, min, max));
</script>

<svg
	aria-hidden="true"
	viewBox="0 0 120 {height}"
	preserveAspectRatio="none"
	style:height="{height}px"
>
	<path d={paths.area} fill={color} fill-opacity="0.12" />
	<path
		d={paths.line}
		fill="none"
		stroke={color}
		stroke-width="1.5"
		vector-effect="non-scaling-stroke"
	/>
</svg>

<style>
	svg {
		width: 100%;
		display: block;
	}
</style>
