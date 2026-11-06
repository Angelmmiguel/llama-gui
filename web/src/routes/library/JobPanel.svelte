<script lang="ts">
	import type { Snippet } from 'svelte';
	import ProgressBar from '$lib/components/ProgressBar.svelte';
	import Spinner from '$lib/components/Spinner.svelte';
	import { tones } from '$lib/components/tones';
	import type { JobView } from './view';

	interface Props {
		view: JobView;
		/** Spoken name of what is being worked on. */
		label: string;
		/** Buttons for what can be done next. */
		actions?: Snippet;
	}

	let { view, label, actions }: Props = $props();

	/** The load has no real progress to count, so the cells only show that it is alive. */
	const CELLS = 32;
	const stepColor = { done: tones.ok, now: tones.decode, later: tones.compute };
</script>

<div class="panel rise">
	<div class="head">
		<span class="stage" role="status">
			{#if view.icon === 'spin'}
				<Spinner size={10} />
			{:else if view.icon === 'done'}
				<span class="tick" aria-hidden="true">✓</span>
			{:else}
				<span class="cross" aria-hidden="true">✕</span>
			{/if}
			<span class={['text', { failed: view.icon === 'failed' }]}>{view.stage}</span>
		</span>
		{#if view.right}<span class="right">{view.right}</span>{/if}
	</div>

	{#if view.bar === 'progress'}
		<ProgressBar
			value={view.percent}
			height={6}
			color={view.icon === 'done' ? tones.ok : tones.prefill}
			flow={view.icon === 'spin'}
			label="{label}: {view.percent}%"
		/>
	{:else if view.bar === 'cells'}
		<div class="cells" aria-hidden="true">
			{#each { length: CELLS }, i (i)}
				<span class="cell" style:animation-delay="{(i * 2) / CELLS}s"></span>
			{/each}
		</div>
	{/if}

	<div class="foot">
		<span class="steps">
			{#each view.steps as step (step.label)}
				<span class="step" style:color={stepColor[step.state]}>
					<span class="step-dot"></span>{step.label}
				</span>
			{/each}
		</span>
		{#if actions}<span class="actions">{@render actions()}</span>{/if}
	</div>
</div>

<style>
	.panel {
		grid-column: 1 / -1;
		display: flex;
		flex-direction: column;
		gap: var(--space-10);
		padding: var(--space-12);
		background: var(--bg-ground);
		border: 1px solid var(--border-panel);
		border-radius: var(--radius-5);
		font-family: var(--font-mono);
		font-size: var(--fs-11);
	}

	.head,
	.foot {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-8);
	}

	.stage {
		display: flex;
		align-items: center;
		gap: var(--space-8);
		min-width: 0;
		color: var(--fg-primary);
	}

	.text {
		overflow-wrap: anywhere;
	}

	.failed,
	.cross {
		color: var(--color-error-soft);
	}

	.tick {
		color: var(--color-ok);
		font-size: var(--fs-13);
		line-height: 1;
	}

	.right {
		flex: none;
		color: var(--fg-muted);
	}

	.cells {
		display: flex;
		gap: 2px;
	}

	.cell {
		flex: 1 1 0;
		min-width: 0;
		height: 14px;
		border-radius: var(--radius-1);
		background: var(--color-weights);
		opacity: 0.12;
		animation: cell-sweep 2s linear infinite;
	}

	@keyframes cell-sweep {
		0%,
		100% {
			opacity: 0.12;
		}
		12% {
			opacity: 1;
		}
		40% {
			opacity: 0.12;
		}
	}

	@media (prefers-reduced-motion: reduce) {
		.cell {
			animation: none;
			opacity: 0.5;
		}
	}

	.steps {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-10);
	}

	.step {
		display: flex;
		align-items: center;
		gap: 5px;
		font-size: var(--fs-10);
	}

	.step-dot {
		width: 6px;
		height: 6px;
		border-radius: var(--radius-round);
		background: currentColor;
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-6);
	}
</style>
