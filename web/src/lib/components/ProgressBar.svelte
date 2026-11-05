<script lang="ts">
	import { tones } from './tones';

	interface Props {
		/** Percent, 0 to 100. */
		value: number;
		/** CSS color of the fill. */
		color?: string;
		/** Height in px: 6 table cells, 8 downloads, 10 hero, 18 history. */
		height?: number;
		/** Radius in px. Defaults to a full pill. */
		radius?: number;
		/** Moving stripes while work is underway. */
		flow?: boolean;
		/** Dark stripes for amber fills, light ones for the rest. */
		darkStripes?: boolean;
		/** Grow in from the left on first render. */
		grow?: boolean;
		/** Spoken description. Without it the bar is decorative, as in table cells. */
		label?: string;
	}

	let {
		value,
		color = tones.decode,
		height = 8,
		radius,
		flow = false,
		darkStripes = false,
		grow = false,
		label
	}: Props = $props();

	const width = $derived(Math.max(0, Math.min(100, value)));
</script>

<div
	class="track"
	role={label ? 'progressbar' : undefined}
	aria-label={label}
	aria-valuenow={label ? Math.round(width) : undefined}
	aria-valuemin={label ? 0 : undefined}
	aria-valuemax={label ? 100 : undefined}
	aria-hidden={label ? undefined : 'true'}
	style:height="{height}px"
	style:border-radius="{radius ?? height / 2}px"
>
	<div
		class={['fill', { flow, 'flow-dark': flow && darkStripes, 'grow-x': grow }]}
		style:width="{width}%"
		style:background-color={color}
		style:border-radius={grow ? `${radius ?? height / 2}px` : undefined}
	></div>
</div>

<style>
	.track {
		background: var(--track);
		overflow: hidden;
	}

	.fill {
		height: 100%;
		transition: width 1s linear;
	}
</style>
