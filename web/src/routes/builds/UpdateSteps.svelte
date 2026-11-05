<script lang="ts">
	import Spinner from '$lib/components/Spinner.svelte';
	import StatusDot from '$lib/components/StatusDot.svelte';
	import { tones } from '$lib/components/tones';
	import type { Step } from './view';

	interface Props {
		steps: Step[];
	}

	let { steps }: Props = $props();
</script>

<ol class="steps">
	{#each steps as step (step.key)}
		<li class="step" class:lit={step.state === 'done' || step.state === 'active'}>
			<span class="mark">
				{#if step.state === 'done'}
					<svg
						role="img"
						aria-label="done"
						width="16"
						height="16"
						viewBox="0 0 24 24"
						fill="none"
						stroke="var(--color-ok)"
						stroke-width="2.5"
						stroke-linecap="round"
						stroke-linejoin="round"><path d="m5 12 5 5L20 7"></path></svg
					>
				{:else if step.state === 'active'}
					<Spinner label="in progress" />
				{:else if step.state === 'failed'}
					<StatusDot color={tones.error} label="failed" />
				{:else}
					<StatusDot color={tones.empty} />
				{/if}
			</span>
			<span class="label">{step.label}</span>
			<span class="meta">{step.meta}</span>
		</li>
	{/each}
</ol>

<style>
	.steps {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.step {
		display: grid;
		grid-template-columns: 22px minmax(0, 1fr) auto;
		align-items: center;
		gap: var(--space-12);
		min-height: 40px;
		border-top: 1px solid var(--border-row);
	}

	.mark {
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.label {
		font-size: var(--fs-13);
		color: var(--fg-muted);
	}

	.lit .label {
		color: var(--fg-primary);
	}

	.meta {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
		text-align: right;
		overflow-wrap: anywhere;
	}
</style>
