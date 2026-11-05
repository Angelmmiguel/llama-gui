<script lang="ts">
	interface Props {
		/** Bindable. */
		value?: number;
		min?: number;
		max?: number;
		step?: number;
		/** Spoken name of the minus button, such as `Fewer slots`. */
		decreaseLabel: string;
		/** Spoken name of the plus button. */
		increaseLabel: string;
		onchange?: (value: number) => void;
	}

	let {
		value = $bindable(0),
		min = -Infinity,
		max = Infinity,
		step = 1,
		decreaseLabel,
		increaseLabel,
		onchange
	}: Props = $props();

	function set(next: number) {
		value = Math.max(min, Math.min(max, next));
		onchange?.(value);
	}
</script>

<div class="stepper">
	<button type="button" aria-label={decreaseLabel} onclick={() => set(value - step)}>−</button>
	<span class="value" aria-live="polite">{value}</span>
	<button type="button" aria-label={increaseLabel} onclick={() => set(value + step)}>+</button>
</div>

<style>
	.stepper {
		display: inline-flex;
		align-self: flex-start;
		align-items: center;
		border: 1px solid var(--border-control);
		border-radius: var(--radius-5);
		overflow: hidden;
	}

	button {
		width: 40px;
		min-height: 38px;
		border: none;
		background: var(--bg-control);
		color: var(--fg-primary);
		font-size: var(--fs-16);
	}

	.value {
		min-width: 48px;
		text-align: center;
		font-family: var(--font-mono);
	}
</style>
