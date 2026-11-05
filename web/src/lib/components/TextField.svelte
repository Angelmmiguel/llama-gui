<script lang="ts">
	import type { HTMLInputAttributes } from 'svelte/elements';
	import Field from './Field.svelte';

	interface Props extends Omit<HTMLInputAttributes, 'size' | 'value' | 'width'> {
		label: string;
		/** Mono flag after the label, such as `--alias`. */
		flag?: string;
		/** Bindable. */
		value?: string;
		/** Control height in px. */
		size?: 38 | 40;
		/** CSS width. Fills the column by default. */
		width?: string;
	}

	let {
		label,
		flag,
		value = $bindable(''),
		size = 38,
		width,
		type = 'text',
		...rest
	}: Props = $props();
</script>

<Field {label} {flag}>
	<input {...rest} {type} bind:value style:min-height="{size}px" style:width />
</Field>

<style>
	input {
		box-sizing: border-box;
		padding: 0 var(--space-10);
		background: var(--bg-ground);
		border: 1px solid var(--border-control);
		border-radius: var(--radius-5);
		color: var(--fg-primary);
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}
</style>
