<script lang="ts">
	import { tones } from './tones';

	interface Props {
		/** CSS color. Pulse only ever draws in green, as in the design. */
		color?: string;
		/** Diameter in px: 6 in labels, 7 profile list, 8 buttons, 10 timelines. */
		size?: 6 | 7 | 8 | 10;
		pulse?: boolean;
		/** Spoken meaning, when the dot stands on its own. */
		label?: string;
	}

	let { color = tones.ok, size = 8, pulse = false, label }: Props = $props();
</script>

<span
	class="dot"
	class:pulse
	role={label ? 'img' : undefined}
	aria-label={label}
	style:width="{size}px"
	style:height="{size}px"
	style:background={color}
></span>

<style>
	.dot {
		display: inline-block;
		flex: none;
		border-radius: var(--radius-round);
	}
</style>
