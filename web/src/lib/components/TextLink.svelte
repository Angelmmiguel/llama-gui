<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		/** An already resolved path: pass `resolve('/requests')`. */
		href: string;
		children: Snippet;
	}

	let { href, children }: Props = $props();
</script>

<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- callers resolve the path -->
<a {href}>{@render children()}</a>

<style>
	a {
		font-size: var(--fs-13);
		color: var(--color-decode);
	}
</style>
