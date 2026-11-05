<script lang="ts">
	import type { Gpu, TelemetryModel } from '$lib/api/client';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import Legend from '$lib/components/Legend.svelte';
	import Metric from '$lib/components/Metric.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import StatGrid from '$lib/components/StatGrid.svelte';
	import { tones } from '$lib/components/tones';
	import { formatGB } from '$lib/format';
	import {
		cellSize,
		memoryLane,
		residentsOn,
		shortGpuName,
		type CellKind,
		type MemoryLane
	} from './overview';

	interface Props {
		gpus: readonly Gpu[];
		ram: { total: number; available: number };
		models: readonly TelemetryModel[];
		/** Tensor split of the busiest profile. */
		tensorSplit: string;
	}

	let { gpus, ram, models, tensorSplit }: Props = $props();

	const GiB = 1024 ** 3;
	const ramUsed = $derived(ram.total > 0 ? ram.total - ram.available : 0);

	const cell = $derived(
		cellSize(gpus.length > 0 ? Math.max(...gpus.map((g) => g.memory_total ?? 0)) : ram.total)
	);

	const cellLabel = $derived(`1 cell = ${cell / GiB} GB`);

	function gpuLane(g: Gpu): MemoryLane {
		const residents = residentsOn(models, g.index);
		const known = residents.reduce(
			(sum, r) => sum + r.device.model + r.device.context + r.device.compute,
			0
		);
		return memoryLane(residents, g.memory_total ?? known, g.memory_used, cell);
	}

	const lanes = $derived(
		gpus.map((g) => {
			const stats = [
				g.memory_total === null
					? 'memory not reported'
					: `${formatGB(g.memory_used)} / ${formatGB(g.memory_total, 0)} GB`,
				...(g.utilization === null ? [] : [`util ${g.utilization}%`])
			];
			return {
				key: g.index,
				name: `GPU ${g.index} · ${shortGpuName(g.name)}`,
				stats: stats.join(' · '),
				lane: gpuLane(g)
			};
		})
	);

	const hostLane = $derived(memoryLane(residentsOn(models, null), ram.total, ramUsed, cell));

	const usedTotal = $derived(gpus.reduce((sum, g) => sum + (g.memory_used ?? 0), 0));
	const capacity = $derived(gpus.reduce((sum, g) => sum + (g.memory_total ?? 0), 0));

	const temps = $derived(
		gpus
			.map((g) => g.temperature)
			.filter((t) => t !== null)
			.map((t) => `${t}°`)
	);

	const stats = $derived(
		gpus.length > 0
			? [
					{
						label: 'System RAM',
						value: ram.total > 0 ? `${formatGB(ramUsed)} / ${formatGB(ram.total, 0)} GB` : '—'
					},
					{ label: 'Tensor split', value: tensorSplit },
					{ label: 'GPU temp', value: temps.length > 0 ? `${temps.join(' · ')}C` : '—' }
				]
			: []
	);

	const legend = [
		{ label: 'Weights', color: tones.weights },
		{ label: 'KV cache', color: tones.cached },
		{ label: 'Compute buffers', color: tones.compute },
		{ label: 'Other', color: tones.other },
		{ label: 'Free', shape: 'outline' as const }
	];

	const colors: Record<CellKind, string> = {
		weights: tones.weights,
		kv: tones.cached,
		compute: tones.compute,
		other: tones.other,
		free: 'transparent'
	};
</script>

{#snippet row(lane: MemoryLane)}
	<div class="segments">
		{#each lane.segments as seg (seg.key)}
			<div class="segment" style:flex="{seg.cells} 1 0">
				<span class="label ellipsis">{seg.label}</span>
				<div class="cells">
					{#each seg.runs as run (run.kind)}
						{#each { length: run.cells }, i (i)}
							<div
								class={['cell', { free: run.kind === 'free', breathe: run.breathe }]}
								style:background={colors[run.kind]}
							></div>
						{/each}
					{/each}
				</div>
			</div>
		{/each}
	</div>
{/snippet}

<Panel flex="2 1 440px" gap={18} aria-labelledby="memory-heading">
	{#if gpus.length > 0}
		<SectionHeading
			id="memory-heading"
			title="VRAM RESIDENCY"
			subtitle="What lives on each GPU · {cellLabel}"
			align="baseline"
		>
			{#snippet actions()}
				<Metric
					value={formatGB(usedTotal)}
					unit="/ {formatGB(capacity, 0)} GB"
					size={20}
					unitMono
				/>
			{/snippet}
		</SectionHeading>
		{#each lanes as gpu (gpu.key)}
			<div class="lane">
				<div class="lane-head">
					<span>{gpu.name}</span>
					<span class="stats">{gpu.stats}</span>
				</div>
				{@render row(gpu.lane)}
				{#if gpu.lane.note}<span class="note">{gpu.lane.note}</span>{/if}
			</div>
		{/each}
		<Legend items={legend} />
		<StatGrid items={stats} pushDown />
	{:else}
		<SectionHeading
			id="memory-heading"
			title="SYSTEM MEMORY"
			subtitle="No GPU detected: models run on the CPU, from system memory{ram.total > 0
				? ` · ${cellLabel}`
				: ''}"
			align="baseline"
		>
			{#snippet actions()}
				{#if ram.total > 0}
					<Metric
						value={formatGB(ramUsed)}
						unit="/ {formatGB(ram.total, 0)} GB"
						size={20}
						unitMono
					/>
				{/if}
			{/snippet}
		</SectionHeading>
		{#if ram.total > 0}
			<div class="lane">
				<div class="lane-head"><span>System RAM</span></div>
				{@render row(hostLane)}
				{#if hostLane.note}<span class="note">{hostLane.note}</span>{/if}
			</div>
			<Legend items={legend} />
		{:else}
			<EmptyState title="No memory reading yet" />
		{/if}
	{/if}
</Panel>

<style>
	.lane {
		display: flex;
		flex-direction: column;
		gap: var(--space-8);
	}

	.lane-head {
		display: flex;
		flex-wrap: wrap;
		justify-content: space-between;
		gap: var(--space-8);
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}

	.stats,
	.note {
		color: var(--fg-muted);
	}

	.note {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
	}

	.segments {
		display: flex;
		gap: var(--space-4);
	}

	.segment {
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.label {
		height: 13px;
		font-family: var(--font-mono);
		font-size: var(--fs-10);
		color: var(--fg-tertiary);
	}

	.cells {
		display: flex;
		gap: var(--space-2);
	}

	.cell {
		flex: 1 1 0;
		min-width: 0;
		height: 24px;
		border-radius: var(--radius-1);
	}

	.free {
		box-sizing: border-box;
		border: 1px solid var(--border-ghost);
	}
</style>
