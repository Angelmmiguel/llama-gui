<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		/** Width in px below which the content scrolls in its own box instead of squeezing. */
		minWidth: number;
		/** Vertical gap between children in px. */
		gap?: number;
		children: Snippet;
	}

	let { minWidth, gap = 0, children }: Props = $props();
</script>

<div class="scroll">
	<div class="inner" style:min-width="{minWidth}px" style:gap="{gap}px">
		{@render children()}
	</div>
</div>

<style>
	.scroll {
		overflow-x: auto;
	}

	.inner {
		display: flex;
		flex-direction: column;
	}
</style>
