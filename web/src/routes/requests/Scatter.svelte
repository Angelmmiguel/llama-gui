<script lang="ts">
	import AxisLabels from '$lib/components/AxisLabels.svelte';
	import Legend from '$lib/components/Legend.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import { tones } from '$lib/components/tones';
	import { formatTokens } from '$lib/format';
	import { msTick, ticks, type ScatterView } from './view';

	interface Props {
		view: ScatterView;
		/** The API sent only the newest of the requests in the window. */
		capped: boolean;
		onselect: (id: number) => void;
	}

	let { view, capped, onselect }: Props = $props();

	const HEIGHT = 260;

	const yLabels = $derived(ticks(view.maxMs, 4).reverse().map(msTick));
	const xLabels = $derived(
		ticks(view.maxTokens, 5).map((v, i, all) =>
			i === all.length - 1 ? `${formatTokens(v)} prompt tokens` : formatTokens(v)
		)
	);
	const legend = [
		{ label: 'Mostly cached', color: tones.cached, shape: 'dot' as const },
		{ label: 'Cold prompt', color: tones.prefill, shape: 'dot' as const },
		{ label: 'Selected', shape: 'ring' as const, color: 'var(--fg-bright)' }
	];
</script>

<SectionHeading
	id="scatter-heading"
	title="PROMPT SIZE vs TIME TO FIRST TOKEN"
	subtitle={capped
		? `Requests that reuse the prompt cache stay flat; showing the newest ${view.dots.length} with timings.`
		: 'Requests that reuse the prompt cache stay flat; cold prompts pay for every token.'}
>
	{#snippet actions()}<Legend items={legend} />{/snippet}
</SectionHeading>

<div class="chart">
	<AxisLabels direction="y" labels={yLabels} height={HEIGHT} width={44} />
	<div class="body">
		<div
			class="plot"
			role="group"
			aria-label="Scatter plot of prompt tokens against time to first token, split by cache use"
			style:height="{HEIGHT}px"
		>
			{#each view.dots as dot (dot.id)}
				<!-- The list below is the keyboard way in: 500 tab stops here would be a trap. -->
				<button
					type="button"
					class="dot"
					class:cached={dot.cached}
					tabindex="-1"
					aria-hidden="true"
					title={dot.title}
					style:left="{dot.x}%"
					style:bottom="{dot.y}%"
					onclick={() => onselect(dot.id)}
				></button>
			{/each}
			{#if view.selected}
				<span class="mark" style:left="{view.selected.x}%" style:bottom="{view.selected.y}%"></span>
			{/if}
		</div>
		<AxisLabels labels={xLabels} />
	</div>
</div>

<style>
	.chart {
		display: flex;
		gap: var(--space-10);
	}

	.body {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-6);
	}

	.plot {
		position: relative;
		border-left: 1px solid var(--chart-axis);
		border-bottom: 1px solid var(--chart-axis);
		background-image:
			linear-gradient(to top, var(--chart-grid-soft) 1px, transparent 1px),
			linear-gradient(to right, var(--chart-grid-soft) 1px, transparent 1px);
		background-size:
			100% 25%,
			20% 100%;
	}

	.dot {
		position: absolute;
		width: 8px;
		height: 8px;
		margin: 0 0 -4px -4px;
		padding: 0;
		border: none;
		border-radius: 50%;
		background: var(--color-prefill);
		opacity: 0.8;
	}

	.dot.cached {
		background: var(--color-cached);
	}

	.dot:hover {
		opacity: 1;
		outline: 1px solid var(--fg-bright);
	}

	.mark {
		position: absolute;
		width: 16px;
		height: 16px;
		margin: 0 0 -8px -8px;
		border: 2px solid var(--fg-bright);
		border-radius: 50%;
		box-sizing: border-box;
		pointer-events: none;
	}
</style>
