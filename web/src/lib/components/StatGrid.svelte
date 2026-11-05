<script lang="ts">
	export interface Stat {
		label: string;
		value: string;
		/** CSS color of the label, for `code` labels such as `200`. */
		labelColor?: string;
		/** CSS color of the value, such as warnings today. */
		valueColor?: string;
	}

	interface Props {
		items: readonly Stat[];
		columns?: number;
		/** Value size in px: 13 Overview, 14 timings, 15 slot detail, 16 counts. */
		valueSize?: 13 | 14 | 15 | 16;
		/** `sans` 11 px, `mono` 10 px, or `code` mono 12 px for status codes. */
		labelStyle?: 'sans' | 'mono' | 'code';
		/** Pin to the bottom of a column panel. */
		pushDown?: boolean;
	}

	let {
		items,
		columns = 3,
		valueSize = 13,
		labelStyle = 'sans',
		pushDown = false
	}: Props = $props();
</script>

<div
	class="grid"
	class:push={pushDown}
	style:grid-template-columns="repeat({columns}, minmax(0, 1fr))"
>
	{#each items as item, i (i)}
		<div class="stat">
			<span class="label {labelStyle}" style:color={item.labelColor}>{item.label}</span>
			<span class="value" style:font-size="{valueSize}px" style:color={item.valueColor}
				>{item.value}</span
			>
		</div>
	{/each}
</div>

<style>
	.grid {
		display: grid;
		gap: 1px;
		background: var(--border-panel);
		border: 1px solid var(--border-panel);
		border-radius: var(--radius-5);
		overflow: hidden;
	}

	.push {
		margin-top: auto;
	}

	.stat {
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		padding: var(--space-10) var(--space-12);
		background: var(--bg-panel);
	}

	.label {
		color: var(--fg-muted);
	}

	.sans {
		font-size: var(--fs-11);
	}

	.mono {
		font-family: var(--font-mono);
		font-size: var(--fs-10);
	}

	.code {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}

	.value {
		font-family: var(--font-mono);
	}
</style>
