<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		/** CSS color. Default muted grey; suggestion cards use `tones.decode`. */
		color?: string;
		children: Snippet;
	}

	let { color, children }: Props = $props();
</script>

<!-- The mono caps label that is not a heading: SAMPLING, COMMAND, UPDATE STEPS. -->
<span class="eyebrow" style:color>{@render children()}</span>

<style>
	.eyebrow {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		letter-spacing: var(--tracking-caps);
		color: var(--fg-muted);
	}
</style>
