<script lang="ts">
	import type { Snippet } from 'svelte';
	import StatusDot from './StatusDot.svelte';
	import { tones } from './tones';

	interface Props {
		/** CSS color for both dot and text. */
		color?: string;
		children: Snippet;
	}

	let { color = tones.idle, children }: Props = $props();
</script>

<!-- Mono state with a small dot, such as a slot's `decode` or `idle`. -->
<span class="state" style:color>
	<StatusDot {color} size={6} />
	<span>{@render children()}</span>
</span>

<style>
	.state {
		display: inline-flex;
		align-items: center;
		gap: var(--space-6);
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}
</style>
