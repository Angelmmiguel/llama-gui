<script lang="ts">
	export interface BarSegment {
		/** Percent of the track, or a share of the whole with `ratio`. */
		value: number;
		/** CSS color. */
		color?: string;
		/** Fill with the grey queued stripes instead of a color. */
		queued?: boolean;
		opacity?: number;
		/** Moving stripes, for work still in progress. */
		flow?: boolean;
		/** Pulse the opacity. */
		breathe?: boolean;
	}

	interface Props {
		segments: readonly BarSegment[];
		/** Height in px: 8 to 18 in the design. */
		height?: number;
		/** Radius in px. */
		radius?: number;
		/** Treat values as shares that fill the bar, as in "where the time goes". */
		ratio?: boolean;
		/** Gap between segments in px. */
		gap?: number;
		/** Show the #16191D track behind the segments. */
		track?: boolean;
		/** Quarter tick marks on the track, as on context bars. */
		ticks?: boolean;
		/** Vertical line at this percent, such as the VRAM limit. */
		marker?: number;
		/** Blinking caret after the last segment, for a slot that is decoding. */
		cursor?: boolean;
		/** CSS transition for segment widths, such as `width 1s linear`. */
		transition?: string;
		/** Spoken description. Without it the bar is decorative. */
		label?: string;
	}

	let {
		segments,
		height = 12,
		radius = 3,
		ratio = false,
		gap = 0,
		track = true,
		ticks = false,
		marker,
		cursor = false,
		transition,
		label
	}: Props = $props();

	const clamp = (n: number) => Math.max(0, Math.min(100, n));
</script>

<div
	class="bar"
	class:track
	class:ticks
	role={label ? 'img' : undefined}
	aria-label={label}
	aria-hidden={label ? undefined : 'true'}
	style:height="{height}px"
	style:border-radius="{radius}px"
	style:gap="{gap}px"
>
	{#each segments as s, i (i)}
		<div
			class={['segment', { queued: s.queued, flow: s.flow, breathe: s.breathe }]}
			style:background-color={s.queued ? undefined : s.color}
			style:opacity={s.opacity}
			style:width={ratio ? undefined : `${clamp(s.value)}%`}
			style:flex={ratio ? `${s.value} 0 0` : undefined}
			style:transition
		></div>
	{/each}
	{#if cursor}<div class="cursor blink"></div>{/if}
	{#if marker !== undefined}
		<div class="marker" style:left="{clamp(marker)}%"></div>
	{/if}
</div>

<style>
	.bar {
		position: relative;
		display: flex;
		overflow: hidden;
	}

	.track {
		background-color: var(--track);
	}

	.ticks {
		background-image: linear-gradient(to right, var(--track-tick) 1px, transparent 1px);
		background-size: 25% 100%;
	}

	.segment {
		min-width: 0;
	}

	.cursor {
		flex: none;
		width: 2px;
		background: var(--fg-bright);
	}

	.marker {
		position: absolute;
		top: 0;
		bottom: 0;
		width: 2px;
		background: var(--fg-primary);
	}
</style>
