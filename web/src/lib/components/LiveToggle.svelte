<script lang="ts">
	import StatusDot from './StatusDot.svelte';
	import { tones } from './tones';

	interface Props {
		/** Bindable: true while updates flow. */
		live?: boolean;
		/** Text while live. */
		label?: string;
		pausedLabel?: string;
		onchange?: (live: boolean) => void;
	}

	let {
		live = $bindable(true),
		label = 'live · 1 s',
		pausedLabel = 'paused',
		onchange
	}: Props = $props();

	function toggle() {
		live = !live;
		onchange?.(live);
	}
</script>

<button type="button" aria-pressed={live} onclick={toggle}>
	<StatusDot color={live ? tones.ok : tones.off} pulse={live} />{live ? label : pausedLabel}
</button>

<style>
	button {
		display: inline-flex;
		align-items: center;
		gap: var(--space-8);
		min-height: 36px;
		padding: 0 var(--space-12);
		border: 1px solid var(--border-control);
		border-radius: var(--radius-5);
		background: var(--bg-control);
		color: var(--fg-primary);
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}
</style>
