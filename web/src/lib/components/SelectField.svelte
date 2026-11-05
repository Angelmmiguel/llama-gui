<script lang="ts">
	import Field from './Field.svelte';

	interface Props {
		label: string;
		/** Mono flag after the label, such as `-sm`. */
		flag?: string;
		/** Plain strings are both value and label. */
		options: readonly (string | { value: string; label: string })[];
		/** Bindable. */
		value?: string;
		/** Control height in px. */
		size?: 36 | 38 | 40;
		/** Mono 12 px text, for file names and flags. */
		mono?: boolean;
		/** Label on the left and a raised control, for filters in a page header. */
		inline?: boolean;
		/** CSS min-width of the control. */
		minWidth?: string;
		disabled?: boolean;
		onchange?: (value: string) => void;
	}

	let {
		label,
		flag,
		options,
		value = $bindable(),
		size = 40,
		mono = false,
		inline = false,
		minWidth,
		disabled = false,
		onchange
	}: Props = $props();
</script>

<Field {label} {flag} {inline}>
	<select
		bind:value
		{disabled}
		class:mono
		class:raised={inline}
		style:min-height="{size}px"
		style:min-width={minWidth}
		onchange={() => onchange?.(value ?? '')}
	>
		{#each options as option (typeof option === 'string' ? option : option.value)}
			{#if typeof option === 'string'}
				<option>{option}</option>
			{:else}
				<option value={option.value}>{option.label}</option>
			{/if}
		{/each}
	</select>
</Field>

<style>
	select {
		padding: 0 var(--space-10);
		background: var(--bg-ground);
		border: 1px solid var(--border-control);
		border-radius: var(--radius-5);
		color: var(--fg-primary);
		font-size: var(--fs-13);
	}

	.raised {
		background: var(--bg-control);
	}

	.mono {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}
</style>
