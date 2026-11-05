<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		/** The active build lacks this flag. */
		off: boolean;
		/** Names the active build in the note. */
		build: string;
		children: Snippet;
	}

	let { off, build, children }: Props = $props();
</script>

<fieldset disabled={off} class:off>
	{@render children()}
	{#if off}<span class="note">not in {build}</span>{/if}
</fieldset>

<style>
	fieldset {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		min-width: 0;
		margin: 0;
		padding: 0;
		border: none;
	}

	.off > :global(*:not(.note)) {
		opacity: 0.5;
	}

	.note {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--color-warn);
	}
</style>
