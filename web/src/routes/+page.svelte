<script lang="ts">
	import {
		getTelemetryHistory,
		listModels,
		listProfiles,
		listRequests,
		startServer,
		type Profile,
		type RequestRecord,
		type TelemetryHistory,
		type RouterModel
	} from '$lib/api/client';
	import Band from '$lib/components/Band.svelte';
	import Button from '$lib/components/Button.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import LineChart from '$lib/components/LineChart.svelte';
	import LiveToggle from '$lib/components/LiveToggle.svelte';
	import Metric from '$lib/components/Metric.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import Segmented from '$lib/components/Segmented.svelte';
	import StatTile from '$lib/components/StatTile.svelte';
	import { tones } from '$lib/components/tones';
	import { formatInt, formatMs, formatTokens } from '$lib/format';
	import LogTail from '$lib/monitor/LogTail.svelte';
	import MemoryPanel from '$lib/monitor/MemoryPanel.svelte';
	import {
		allSlots,
		CHART_RANGES,
		chartData,
		backendLabel,
		busiestProfile,
		gpuSummary,
		niceMax,
		tensorSplitLabel,
		waterfallRows,
		type ChartRange
	} from '$lib/monitor/overview';
	import GettingStarted from '$lib/monitor/GettingStarted.svelte';
	import { errorText, every } from '$lib/monitor/poll';
	import ProfileSummary from '$lib/monitor/ProfileSummary.svelte';
	import RouterModels from '$lib/monitor/RouterModels.svelte';
	import SlotBars from '$lib/monitor/SlotBars.svelte';
	import Waterfall from '$lib/monitor/Waterfall.svelte';
	import { server } from '$lib/state/server.svelte';
	import { telemetry } from '$lib/state/telemetry.svelte';

	const REQUESTS_MS = 2000;
	const CONFIG_MS = 10_000;
	// History gets a new point this often at the finest.
	const HISTORY_MS = 10_000;
	// Wider than the chart: a long request that began before the window still ends inside it.
	const RECENT_MS = 10 * 60_000;

	let recent = $state.raw<RequestRecord[]>([]);
	let profiles = $state.raw<Profile[]>([]);
	let estimates = $state.raw<RouterModel[]>([]);
	let range = $state<ChartRange>('1m');
	let history = $state.raw<TelemetryHistory | null>(null);
	let startError = $state('');
	let starting = $state(false);

	$effect(() => telemetry.subscribe());

	// Paused freezes the view, so these stop too.
	$effect(() => {
		if (!telemetry.live) return;
		return every(REQUESTS_MS, async (signal) => {
			const since = new Date(Date.now() - RECENT_MS).toISOString();
			const list = await listRequests({ since, limit: 50 }).catch(() => null);
			if (list && !signal.aborted) recent = list.requests;
		});
	});

	$effect(() => {
		if (!telemetry.live) return;
		return every(CONFIG_MS, async (signal) => {
			const [p, m] = await Promise.all([
				listProfiles().catch(() => null),
				listModels().catch(() => null)
			]);
			if (signal.aborted) return;
			if (p) profiles = p;
			if (m) estimates = m;
		});
	});

	const snap = $derived(telemetry.latest);
	// With nothing to run yet, an empty dashboard would only get in the way of what to do.
	const noBuild = $derived(server.status !== null && !server.status.active_build);
	const routerState = $derived(snap?.server.state ?? server.state);
	const routerUp = $derived(routerState === 'running');

	const subtitle = $derived.by(() => {
		if (!snap) return undefined;
		const build = server.status?.active_build;
		const loaded = snap.models.filter((m) => m.status === 'loaded').length;
		return [
			build ? `llama.cpp ${build.tag}` : 'no llama.cpp build',
			...(build ? [backendLabel(build.backend)] : []),
			gpuSummary(snap.gpus),
			routerUp ? `${loaded} of ${snap.models.length} models loaded` : `router ${routerState}`
		].join(' · ');
	});

	// Waits for its range's history before switching, so changing range never blanks the chart.
	const shown = $derived(history && history.range === range ? history.points : null);
	const chart = $derived(chartData(shown ? range : '1m', snap?.series, shown));
	const chartRange = $derived(
		CHART_RANGES.find((r) => r.value === (shown ? range : '1m')) ?? CHART_RANGES[0]
	);
	const decode = $derived(chart.decode);
	const decodeTop = $derived(niceMax(Math.max(0, chart.peak, ...decode)));
	const decodeAvg = $derived(
		decode.length > 0 ? decode.reduce((a, b) => a + b, 0) / decode.length : 0
	);
	const decodePeak = $derived(chart.peak);
	const yLabels = $derived(
		[3, 2, 1, 0].map((i) => {
			const v = (decodeTop / 3) * i;
			return v >= 1000 ? formatTokens(v) : String(Math.round(v * 10) / 10);
		})
	);

	const slots = $derived(snap ? allSlots(snap.models) : []);
	const busiest = $derived(busiestProfile(profiles, snap?.models ?? []));

	$effect(() => {
		const current = range;
		if (current === '1m' || !telemetry.live) return;
		return every(HISTORY_MS, async (signal) => {
			const next = await getTelemetryHistory(current, signal).catch(() => null);
			if (next && !signal.aborted) history = next;
		});
	});

	const nowMs = $derived(snap ? Date.parse(snap.time) : 0);
	const rows = $derived(snap ? waterfallRows(snap.in_flight, recent, snap.models, nowMs) : []);
	// Newest first from the API, so reversing lets the newest win.
	const lastRequest = $derived(
		new Map(
			recent
				.toReversed()
				.map((r) => [r.model, Math.max(0, (nowMs - Date.parse(r.started_at) - r.total_ms) / 1000)])
		)
	);

	const downReason = $derived.by(() => {
		switch (routerState) {
			case 'starting':
				return { title: 'The router is starting', detail: undefined };
			case 'stopping':
				return { title: 'The router is stopping', detail: undefined };
			case 'crashed':
				return { title: 'The router crashed', detail: server.status?.last_error };
			case 'unreachable':
				return { title: 'Cannot reach llama-gui', detail: 'Retrying.' };
			case 'connecting':
				return { title: 'Connecting', detail: undefined };
			default:
				return { title: 'The router is stopped', detail: server.status?.last_error };
		}
	});

	const slotsEmpty = $derived(
		!routerUp
			? downReason
			: (snap?.models.length ?? 0) === 0
				? { title: 'No models', detail: 'Add a profile to give the router a model to load.' }
				: {
						title: 'No models loaded',
						detail: 'Load a model below and its slots appear here.'
					}
	);
	const modelsEmpty = $derived(
		!routerUp
			? downReason
			: { title: 'No models', detail: 'Add a profile or download a model from the Library.' }
	);

	const tiles = $derived.by(() => {
		if (!snap) return [];
		const { requests, kv } = snap;
		const spark = chart.spark;
		const hasTtft = requests.ttft_p50_ms > 0;
		const deferred = requests.deferred;
		const batching = [
			busiest?.batch ? `batch ${busiest.batch}` : '',
			busiest?.ubatch ? `ubatch ${busiest.ubatch}` : ''
		]
			.filter(Boolean)
			.join(' · ');
		return [
			{
				label: 'PREFILL',
				value: routerUp ? formatInt(snap.throughput.prefill) : '—',
				unit: 'tok/s',
				data: spark.prefill,
				color: tones.prefill,
				note: batching || 'batching at defaults'
			},
			{
				label: 'TTFT p50',
				value: hasTtft ? formatInt(requests.ttft_p50_ms) : '—',
				unit: 'ms',
				data: spark.ttft,
				color: tones.secondary,
				note: hasTtft ? `p95 ${formatMs(requests.ttft_p95_ms)}` : 'no requests in 5 min'
			},
			{
				label: 'REQUESTS',
				value: routerUp ? formatInt(requests.per_minute) : '—',
				unit: '/ min',
				data: spark.rpm,
				color: tones.secondary,
				note: deferred > 0 ? `${deferred} deferred in queue` : 'queue empty'
			},
			{
				label: 'KV CACHE',
				value: kv.total > 0 ? ((kv.used / kv.total) * 100).toFixed(1) : '—',
				unit: '% used',
				data: spark.kv,
				color: tones.cached,
				note:
					kv.total > 0
						? `${formatTokens(kv.used)} / ${formatTokens(kv.total)} cells`
						: 'no slots loaded'
			}
		];
	});

	async function start() {
		starting = true;
		startError = '';
		try {
			await startServer();
			await server.poll();
		} catch (e) {
			startError = errorText(e);
		} finally {
			starting = false;
		}
	}
</script>

<PageHeader title="Overview" {subtitle}>
	{#snippet actions()}
		<Segmented label="Chart range" options={CHART_RANGES} bind:value={range} />
		<LiveToggle bind:live={telemetry.live} />
	{/snippet}
</PageHeader>

<GettingStarted />

{#if noBuild}
	<!-- GettingStarted says it all. -->
{:else if !snap}
	<Panel>
		<EmptyState
			title={telemetry.connected ? 'Waiting for the first sample' : 'Connecting to telemetry'}
			detail={server.unreachable ? 'llama-gui is not answering. Retrying.' : undefined}
		/>
	</Panel>
{:else}
	{#if !routerUp}
		<Panel padding="4px 6px">
			<EmptyState title={downReason.title} detail={snap.server.error ?? downReason.detail}>
				{#snippet actions()}
					{#if routerState === 'stopped' || routerState === 'crashed'}
						<Button size="sm" disabled={starting} onclick={start}>Start the router</Button>
					{/if}
					{#if startError}<span class="error" role="alert">{startError}</span>{/if}
				{/snippet}
			</EmptyState>
		</Panel>
	{/if}

	<Band>
		<Panel flex="2 1 560px" gap={14} aria-labelledby="decode-heading">
			<SectionHeading
				id="decode-heading"
				title="DECODE THROUGHPUT · ALL SLOTS"
				note={chartRange.note}
				align="baseline"
			/>
			<div class="hero">
				<Metric
					value={routerUp ? snap.throughput.decode.toFixed(1) : '—'}
					unit="tok/s"
					size={56}
					unitSize={14}
				/>
				{#if routerUp}
					<span class="summary">avg {decodeAvg.toFixed(1)} · peak {decodePeak.toFixed(1)}</span>
				{/if}
			</div>
			<LineChart
				series={[{ data: decode, area: true, endDot: true, color: tones.decode }]}
				label="Decode tokens per second, {chartRange.note}"
				max={decodeTop}
				{yLabels}
				xLabels={chartRange.ticks}
			/>
		</Panel>
		<Band layout="grid" columns={2} nested flex="1 1 380px">
			{#each tiles as t (t.label)}
				<StatTile {...t} />
			{/each}
		</Band>
	</Band>

	<Band>
		<SlotBars {slots} empty={slotsEmpty} />
		<MemoryPanel
			gpus={snap.gpus}
			ram={snap.ram}
			models={snap.models}
			tensorSplit={tensorSplitLabel(busiest)}
		/>
	</Band>

	<Waterfall {rows} />

	<Band>
		<RouterModels
			models={snap.models}
			{estimates}
			{profiles}
			gpus={snap.gpus}
			{lastRequest}
			empty={modelsEmpty}
			canAct={routerUp}
		/>
		<ProfileSummary profile={busiest} />
		<LogTail live={telemetry.live} />
	</Band>
{/if}

<style>
	.hero {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: var(--space-6) var(--space-18);
	}

	.summary {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		color: var(--fg-secondary);
	}

	.error {
		font-size: var(--fs-12);
		color: var(--color-error-soft);
	}
</style>
