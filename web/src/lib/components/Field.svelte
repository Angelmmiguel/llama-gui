<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		label: string;
		/** Mono flag after the label, such as `-c`. */
		flag?: string;
		/** `div` when the control is a group of buttons (Segmented, Stepper), which a label cannot name. */
		as?: 'label' | 'div';
		/** Gap between the label and the control in px. */
		gap?: 6 | 8;
		/** The label sits left of the control, in muted text, as on Requests' filters. */
		inline?: boolean;
		children: Snippet;
	}

	let { label, flag, as = 'label', gap = 6, inline = false, children }: Props = $props();
</script>

<svelte:element this={as} class="field" class:inline style:gap="{gap}px">
	<span
		>{label}{#if flag}&nbsp;<span class="flag">{flag}</span>{/if}</span
	>
	{@render children()}
</svelte:element>

<style>
	.field {
		min-width: 0;
		display: flex;
		flex-direction: column;
		font-size: var(--fs-12);
		color: var(--fg-tertiary);
	}

	.inline {
		flex-direction: row;
		align-items: center;
		color: var(--fg-muted);
	}

	.flag {
		font-family: var(--font-mono);
		color: var(--fg-faint);
	}
</style>
