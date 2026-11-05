<script lang="ts">
	import { resolve } from '$app/paths';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import Legend from '$lib/components/Legend.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import Row from '$lib/components/Row.svelte';
	import ScrollX from '$lib/components/ScrollX.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import TextLink from '$lib/components/TextLink.svelte';
	import { tones } from '$lib/components/tones';
	import type { WaterfallRow } from './overview';

	let { rows }: { rows: readonly WaterfallRow[] } = $props();

	const COLUMNS = '300px minmax(0, 1fr) 260px';
	const legend = [
		{ label: 'Queued', shape: 'queued' as const },
		{ label: 'Prefill', shape: 'wide' as const, color: tones.prefill },
		{ label: 'Decode', shape: 'wide' as const, color: tones.decode }
	];
	const statusColor: Record<WaterfallRow['tone'], string> = {
		prefill: tones.prefill,
		decode: tones.decode,
		queued: tones.muted,
		ok: tones.ok,
		warn: tones.warn
	};
</script>

<Panel gap={14} aria-labelledby="waterfall-heading">
	<SectionHeading
		id="waterfall-heading"
		title="REQUEST WATERFALL · LAST 60 s"
		subtitle="Each request split into time in queue, prompt prefill and token decode"
	>
		{#snippet actions()}
			<div class="actions">
				<Legend items={legend} />
				<TextLink href={resolve('/requests')}>All requests →</TextLink>
			</div>
		{/snippet}
	</SectionHeading>
	{#if rows.length === 0}
		<EmptyState
			title="No requests in the last 60 s"
			detail="Requests through this address appear here as they run."
		/>
	{:else}
		<ScrollX minWidth={900}>
			<Row columns={COLUMNS} gap={16} padding="0 0 8px" header>
				<span>REQUEST</span>
				<div class="axis">
					<span>−60s</span><span>−45s</span><span>−30s</span><span>−15s</span><span>now</span>
				</div>
				<span class="right">TOKENS IN → OUT · STATUS</span>
			</Row>
			{#each rows as r (r.key)}
				<Row columns={COLUMNS} gap={16} padding="8px 0">
					<div class="request">
						<span class="id">{r.id}</span>
						<span class="endpoint">{r.endpoint}</span>
						<span class="model ellipsis">{r.model}</span>
					</div>
					<div class="track">
						<div class="bar" style:left="min({r.left}%, calc(100% - 3px))" style:width="{r.width}%">
							{#if r.queue > 0}<div class="queued" style:flex="{r.queue} 0 0"></div>{/if}
							{#if r.prefill > 0}
								<div
									class={{ flow: r.live }}
									style:flex="{r.prefill} 0 0"
									style:background-color={tones.prefill}
								></div>
							{/if}
							{#if r.decode > 0}
								<div
									class={{ flow: r.live, 'flow-dark': r.live }}
									style:flex="{r.decode} 0 0"
									style:background-color={r.tone === 'warn' ? tones.warn : tones.decode}
								></div>
							{/if}
						</div>
					</div>
					<div class="figures">
						<span class="io">{r.io}</span>
						<span class="status" style:color={statusColor[r.tone]}>{r.status}</span>
					</div>
				</Row>
			{/each}
		</ScrollX>
	{/if}
</Panel>

<style>
	.actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-14);
		font-size: var(--fs-13);
	}

	.axis {
		display: flex;
		justify-content: space-between;
	}

	.right {
		text-align: right;
	}

	.request {
		display: flex;
		align-items: baseline;
		gap: var(--space-10);
		min-width: 0;
	}

	.id {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-faint);
	}

	.endpoint {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		white-space: nowrap;
	}

	.model {
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}

	.track {
		position: relative;
		height: 22px;
		background-image: linear-gradient(to right, var(--chart-grid) 1px, transparent 1px);
		background-size: 25% 100%;
		border-right: 1px dashed var(--chart-axis);
	}

	.bar {
		position: absolute;
		top: 5px;
		bottom: 5px;
		min-width: 3px;
		display: flex;
		border-radius: var(--radius-2);
		overflow: hidden;
	}

	.figures {
		display: flex;
		justify-content: flex-end;
		align-items: baseline;
		gap: var(--space-12);
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}

	.io {
		color: var(--fg-secondary);
		white-space: nowrap;
	}

	.status {
		min-width: 64px;
		text-align: right;
		white-space: nowrap;
	}
</style>
