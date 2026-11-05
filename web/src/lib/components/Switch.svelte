<script lang="ts">
	interface Props {
		/** Bindable. */
		checked?: boolean;
		/** Sans 13 px label. Omit in `row` mode to lead with the flag. */
		label?: string;
		/** Mono flag such as `--jinja`, after the label, or as the title in `row` mode. */
		flag?: string;
		/** `row` mode: sans 12 px line under the flag. */
		help?: string;
		/** The divided, full-width compile option row of Builds. */
		row?: boolean;
		disabled?: boolean;
		onchange?: (checked: boolean) => void;
	}

	let {
		checked = $bindable(false),
		label,
		flag,
		help,
		row = false,
		disabled = false,
		onchange
	}: Props = $props();

	function toggle() {
		checked = !checked;
		onchange?.(checked);
	}
</script>

<button
	type="button"
	role="switch"
	aria-checked={checked}
	{disabled}
	class:row
	class="switch"
	onclick={toggle}
>
	<span class="track" class:on={checked}><span class="knob"></span></span>
	{#if row}
		<span class="text">
			<span class="title">{flag ?? label}</span>
			{#if help}<span class="help">{help}</span>{/if}
		</span>
	{:else}
		<span
			>{label}{#if flag}&nbsp;<span class="flag">{flag}</span>{/if}</span
		>
	{/if}
</button>

<style>
	.switch {
		display: flex;
		align-items: center;
		gap: var(--space-10);
		min-height: 40px;
		padding: 0;
		background: transparent;
		border: none;
		color: var(--fg-primary);
		font-size: var(--fs-13);
		text-align: left;
	}

	.switch:disabled {
		opacity: 0.6;
	}

	.row {
		display: grid;
		grid-template-columns: 36px minmax(0, 1fr);
		gap: var(--space-12);
		width: 100%;
		min-height: 48px;
		padding: var(--space-6) 0;
		border-top: 1px solid var(--border-row);
	}

	.track {
		position: relative;
		flex: none;
		display: block;
		width: 36px;
		height: 20px;
		border-radius: var(--radius-6);
		background: var(--border-ghost);
	}

	.track.on {
		background: var(--color-decode);
	}

	.knob {
		position: absolute;
		top: 2px;
		left: 2px;
		width: 16px;
		height: 16px;
		border-radius: var(--radius-round);
		background: var(--fg-muted);
	}

	.on .knob {
		left: 18px;
		background: var(--fg-on-accent);
	}

	.flag {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-faint);
	}

	.text {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
	}

	.title {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}

	.help {
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}
</style>
