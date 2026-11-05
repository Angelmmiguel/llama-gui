<script lang="ts" generics="T extends string | number">
	interface Option {
		value: T;
		/** Defaults to the value. */
		label?: string;
		disabled?: boolean;
	}

	interface Props {
		options: readonly Option[];
		/** Bindable selection. */
		value?: T;
		/** Names the group for assistive tech. */
		label: string;
		/** Background of the group: `panel` on the page, `ground` inside a panel. */
		surface?: 'panel' | 'ground';
		/** `tabs` is the 13 px sans role=tablist of the Library; otherwise mono pills with aria-pressed. */
		tabs?: boolean;
		onchange?: (value: T) => void;
	}

	let {
		options,
		value = $bindable(),
		label,
		surface = 'panel',
		tabs = false,
		onchange
	}: Props = $props();

	function pick(next: T) {
		value = next;
		onchange?.(next);
	}
</script>

<div class="group {surface}" role={tabs ? 'tablist' : 'group'} aria-label={label}>
	{#each options as option (option.value)}
		{@const on = option.value === value}
		<button
			type="button"
			role={tabs ? 'tab' : undefined}
			aria-selected={tabs ? on : undefined}
			aria-pressed={tabs ? undefined : on}
			disabled={option.disabled}
			class:tab={tabs}
			class:on
			onclick={() => pick(option.value)}
		>
			{option.label ?? option.value}
		</button>
	{/each}
</div>

<style>
	.group {
		display: inline-flex;
		flex-wrap: wrap;
		max-width: 100%;
		box-sizing: border-box;
		align-self: flex-start;
		gap: var(--space-2);
		border: 1px solid var(--border-panel);
		border-radius: var(--radius-5);
		padding: 3px;
	}

	.panel {
		background: var(--bg-panel);
	}

	.ground {
		background: var(--bg-ground);
	}

	button {
		min-height: 30px;
		padding: 0 var(--space-10);
		border: none;
		border-radius: var(--radius-4);
		background: transparent;
		color: var(--fg-muted);
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}

	.tab {
		min-height: 32px;
		padding: 0 var(--space-12);
		font-family: inherit;
		font-size: var(--fs-13);
	}

	.on {
		background: var(--bg-segment-on);
		color: var(--fg-bright);
	}
</style>
