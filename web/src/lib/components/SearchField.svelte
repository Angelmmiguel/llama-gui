<script lang="ts">
	interface Props {
		/** Visually hidden name of the field. */
		label: string;
		/** Bindable. */
		value?: string;
		placeholder?: string;
		/** Mono 12 px on a panel, 36 tall (Logs). Otherwise sans 13 px raised, 38 tall (Library). */
		mono?: boolean;
		/** CSS `flex` shorthand. */
		flex?: string;
		/** CSS min-width. */
		minWidth?: string;
		oninput?: (value: string) => void;
	}

	let {
		label,
		value = $bindable(''),
		placeholder,
		mono = false,
		flex,
		minWidth,
		oninput
	}: Props = $props();
</script>

<label class="search" class:mono style:flex style:min-width={minWidth}>
	<svg
		aria-hidden="true"
		width="14"
		height="14"
		viewBox="0 0 24 24"
		fill="none"
		stroke="currentColor"
		stroke-width="2"
		stroke-linecap="round"
	>
		<circle cx="11" cy="11" r="7"></circle>
		<path d="m20 20-3.5-3.5"></path>
	</svg>
	<span class="sr-only">{label}</span>
	<input type="text" {placeholder} bind:value oninput={() => oninput?.(value)} />
</label>

<style>
	.search {
		display: flex;
		align-items: center;
		gap: var(--space-8);
		min-height: 38px;
		padding: 0 var(--space-10);
		background: var(--bg-control);
		border: 1px solid var(--border-control);
		border-radius: var(--radius-5);
		color: var(--fg-muted);
	}

	.search:focus-within {
		border-color: var(--border-raised);
	}

	.mono {
		min-height: 36px;
		background: var(--bg-panel);
	}

	input {
		flex: 1;
		min-width: 0;
		border: none;
		outline: none;
		background: transparent;
		color: var(--fg-primary);
		font-size: var(--fs-13);
	}

	.mono input {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}
</style>
