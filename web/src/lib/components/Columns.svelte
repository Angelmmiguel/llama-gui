<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		/** Stretch children to equal height instead of top-aligning them. */
		stretch?: boolean;
		children: Snippet;
	}

	let { stretch = false, children }: Props = $props();
</script>

<!-- The separate, 16 px apart panels of a page: give each a `flex` such as `3 1 700px`. -->
<div class="columns" class:stretch>
	{@render children()}
</div>

<style>
	.columns {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-16);
		align-items: flex-start;
	}

	.stretch {
		align-items: stretch;
	}
</style>
