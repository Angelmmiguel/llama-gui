<script lang="ts">
	import type { Slot } from '$lib/api/client';
	import Legend from '$lib/components/Legend.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import Row from '$lib/components/Row.svelte';
	import ScrollX from '$lib/components/ScrollX.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import StackedBar from '$lib/components/StackedBar.svelte';
	import StateLabel from '$lib/components/StateLabel.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import { tones } from '$lib/components/tones';
	import { formatTokens } from '$lib/format';
	import { contextSummary, slotActivity, slotFill, stateTone, type ModelSlot } from './overview';

	interface Props {
		slots: readonly ModelSlot[];
		/** Why there are no slots, for the empty state. */
		empty: { title: string; detail?: string };
	}

	let { slots, empty }: Props = $props();

	const busy = $derived(slots.filter((s) => s.state !== 'idle').length);
	const legend = [
		{ label: 'Cached prefix', color: tones.cached },
		{ label: 'Prompt', color: tones.prefill },
		{ label: 'Generated', color: tones.decode }
	];

	function segments(s: Slot) {
		const fill = slotFill(s);
		const pct = (n: number) => (s.n_ctx > 0 ? (n / s.n_ctx) * 100 : 0);
		return [
			{ value: pct(fill.cached), color: tones.cached, opacity: 0.75 },
			{ value: pct(fill.prompt), color: tones.prefill },
			{ value: pct(fill.generated), color: tones.decode }
		];
	}
</script>

<Panel flex="3 1 640px" gap={16} aria-labelledby="slots-heading">
	<SectionHeading
		id="slots-heading"
		title="SLOTS · CONTEXT WINDOWS"
		subtitle={slots.length > 0
			? `${busy} of ${slots.length} busy · ${contextSummary(slots)}`
			: undefined}
	>
		{#snippet actions()}<Legend items={legend} />{/snippet}
	</SectionHeading>
	{#if slots.length === 0}
		<EmptyState title={empty.title} detail={empty.detail} />
	{:else}
		<ScrollX minWidth={640}>
			{#each slots as s (`${s.model}/${s.id}`)}
				<Row columns="150px 104px minmax(0, 1fr) 150px" padding="11px 0">
					<div class="name">
						<span class="id">slot {s.id}</span>
						<span class="model ellipsis">{s.model}</span>
					</div>
					<StateLabel color={stateTone(s.state)}>{s.state}</StateLabel>
					<StackedBar
						segments={segments(s)}
						height={16}
						ticks
						cursor={s.state === 'decode'}
						transition="width 1s linear"
						label="{s.model} slot {s.id}: {s.total} of {s.n_ctx} tokens"
					/>
					<div class="figures">
						<span class="used">{formatTokens(s.total)} / {formatTokens(s.n_ctx)}</span>
						<span class="speed">{slotActivity(s)}</span>
					</div>
				</Row>
			{/each}
		</ScrollX>
	{/if}
</Panel>

<style>
	.name,
	.figures {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		min-width: 0;
	}

	.id {
		font-family: var(--font-mono);
		font-size: var(--fs-13);
	}

	.model {
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}

	.figures {
		text-align: right;
		font-family: var(--font-mono);
	}

	.used {
		font-size: var(--fs-12);
	}

	.speed {
		font-size: var(--fs-11);
		color: var(--fg-muted);
	}
</style>
