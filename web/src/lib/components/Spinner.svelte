<script lang="ts">
	import { tones } from './tones';

	interface Props {
		/** Inner diameter in px: 10 inline, 12 in step lists. */
		size?: 10 | 12;
		/** CSS color. */
		color?: string;
		/** Spoken meaning. */
		label?: string;
	}

	let { size = 12, color = tones.decode, label }: Props = $props();
</script>

<span
	class="spinner spin"
	role={label ? 'img' : undefined}
	aria-label={label}
	style:width="{size}px"
	style:height="{size}px"
	style:--spinner-color={color}
></span>

<style>
	.spinner {
		display: inline-block;
		flex: none;
		border-radius: var(--radius-round);
		border: 2px solid var(--spinner-color);
		border-top-color: transparent;
	}
</style>
