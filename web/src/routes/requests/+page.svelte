<script lang="ts">
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import {
		getRequestSummary,
		listRequests,
		requestsCsvUrl,
		type RequestDetail,
		type RequestRecord,
		type RequestSummary
	} from '$lib/api/client';
	import Band from '$lib/components/Band.svelte';
	import Button from '$lib/components/Button.svelte';
	import Columns from '$lib/components/Columns.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import Eyebrow from '$lib/components/Eyebrow.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import Row from '$lib/components/Row.svelte';
	import ScrollX from '$lib/components/ScrollX.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import Segmented from '$lib/components/Segmented.svelte';
	import SelectField from '$lib/components/SelectField.svelte';
	import StackedBar from '$lib/components/StackedBar.svelte';
	import StatGrid from '$lib/components/StatGrid.svelte';
	import StatTile from '$lib/components/StatTile.svelte';
	import { tones } from '$lib/components/tones';
	import { formatClock, formatDayClock, formatInt, formatMs, formatTokens } from '$lib/format';
	import { server } from '$lib/state/server.svelte';
	import Detail from './Detail.svelte';
	import Scatter from './Scatter.svelte';
	import {
		apiFilters,
		filtersQuery,
		firstTokenMs,
		mergeNewest,
		parseFilters,
		RANGES,
		scatterView,
		statusLevel,
		type EndpointKind,
		type Filters,
		type Range
	} from './view';

	const PAGE_SIZE = 50;
	const REFRESH_MS = 15_000;

	const kinds: { value: EndpointKind; label: string }[] = [
		{ value: 'all', label: 'All' },
		{ value: 'chat', label: 'chat' },
		{ value: 'completions', label: 'completions' },
		{ value: 'embeddings', label: 'embeddings' },
		{ value: 'infill', label: 'infill' }
	];
	const rangeOptions = Object.entries(RANGES).map(([value, r]) => ({ value, label: r.label }));
	const statusColor = { ok: tones.ok, warn: tones.warn, error: tones.errorSoft };

	const filters = $derived(parseFilters(page.url.searchParams));
	// Selecting a request changes the URL too, and must not refetch the lists.
	const listKey = $derived(`${filters.kind}|${filters.model}|${filters.range}`);

	let summary = $state<RequestSummary | null>(null);
	let modelNames = $state<string[]>([]);
	let rows = $state<RequestRecord[]>([]);
	let cursor = $state('');
	let loading = $state(false);
	let loadingMore = $state(false);
	let failure = $state('');
	let selected = $state<RequestDetail | null>(null);
	let generation = 0;

	function navigate(next: Filters, replaceState = false) {
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- resolve() is the prefix.
		void goto(resolve('/requests') + filtersQuery(next), {
			replaceState,
			keepFocus: true,
			noScroll: true
		});
	}

	function select(id: number) {
		navigate({ ...filters, id }, true);
	}

	async function refresh(fresh: boolean) {
		const run = ++generation;
		const current = filters;
		const query = apiFilters(current, Date.now());
		if (fresh) loading = true;
		try {
			const [nextSummary, list, allModels] = await Promise.all([
				getRequestSummary(query),
				listRequests({ ...query, limit: PAGE_SIZE }),
				// A summary filtered to one model only names that one.
				current.model ? getRequestSummary({ ...query, model: undefined }) : null
			]);
			if (run !== generation) return;
			summary = nextSummary;
			modelNames = (allModels ?? nextSummary).models.map((m) => m.model).filter(Boolean);
			failure = '';
			if (fresh) {
				rows = list.requests;
				cursor = list.next_cursor;
			} else {
				const merged = mergeNewest(rows, list.requests);
				rows = merged.rows;
				if (merged.reset) cursor = list.next_cursor;
			}
			if (current.id === null && rows.length > 0) navigate({ ...current, id: rows[0].id }, true);
		} catch (e) {
			if (run !== generation) return;
			failure = e instanceof Error ? e.message : String(e);
			if (fresh) {
				// The old data answers a different question, so it must not stay under the new filter.
				summary = null;
				rows = [];
				cursor = '';
			}
		} finally {
			if (run === generation) loading = false;
		}
	}

	$effect(() => {
		void listKey;
		// What is on screen stays until the new data arrives, so a filter never blanks the page.
		untrack(() => void refresh(true));
		const timer = setInterval(() => {
			if (document.visibilityState === 'visible') void refresh(false);
		}, REFRESH_MS);
		return () => clearInterval(timer);
	});

	async function loadMore() {
		loadingMore = true;
		const run = generation;
		try {
			const list = await listRequests({
				...apiFilters(filters, Date.now()),
				limit: PAGE_SIZE,
				cursor
			});
			if (run !== generation) return;
			const known = new Set(rows.map((r) => r.id));
			rows = [...rows, ...list.requests.filter((r) => !known.has(r.id))];
			cursor = list.next_cursor;
		} catch (e) {
			failure = e instanceof Error ? e.message : String(e);
		} finally {
			loadingMore = false;
		}
	}

	const modelOptions = $derived([
		{ value: '', label: 'All models' },
		...[...new Set([...modelNames, ...(filters.model ? [filters.model] : [])])]
			.sort()
			.map((m) => ({ value: m, label: m }))
	]);

	const csvHref = $derived(
		requestsCsvUrl({ ...apiFilters(filters, summary ? Date.parse(summary.until) : Date.now()) })
	);

	const label = (r: RequestRecord) => r.error || String(r.status);
	const count = (s: RequestSummary, status: string) =>
		s.statuses.find((x) => x.status === status)?.count ?? 0;

	function percent(part: number, whole: number): string {
		return whole > 0 ? `${((part / whole) * 100).toFixed(part === whole ? 0 : 1)}%` : '—';
	}

	const kpis = $derived.by(() => {
		if (!summary) return [];
		const k = summary.kpis;
		const busy = count(summary, '503 busy');
		const overContext = count(summary, '400 ctx');
		const prompt = k.prompt_tokens + k.cached_tokens;
		const perMinute = k.per_minute < 10 ? k.per_minute.toFixed(1) : formatInt(k.per_minute);
		return [
			{
				label: 'REQUESTS',
				value: formatInt(k.requests),
				note: `${perMinute} / min${busy ? ` · ${formatInt(busy)} rejected busy` : ''}`
			},
			{
				label: 'SUCCESS',
				value: percent(k.requests - k.errors, k.requests),
				note: overContext
					? `${formatInt(overContext)} over context limit`
					: `${formatInt(k.errors)} failed`
			},
			{
				label: 'TTFT p50 / p95',
				value: formatMs(summary.ttft.p50),
				note: `p95 ${formatMs(summary.ttft.p95)}`
			},
			{
				label: 'END-TO-END p50',
				value: formatMs(summary.total.p50),
				note: `p95 ${formatMs(summary.total.p95)}`
			},
			{
				label: 'TOKENS IN / OUT',
				value: formatTokens(prompt),
				note: `${formatTokens(k.predicted_tokens)} generated`
			},
			{
				label: 'PROMPT CACHE HIT',
				value: percent(k.cached_tokens, prompt),
				note: `${formatTokens(k.cached_tokens)} tokens not recomputed`
			}
		];
	});

	const scatter = $derived(summary ? scatterView(summary.scatter, selected) : null);
	const statuses = $derived(
		(summary?.statuses ?? []).map((s) => ({
			label: s.status,
			value: formatInt(s.count),
			labelColor: statusColor[statusLevel(s.status)]
		}))
	);

	const rowColumns = $derived(
		`${filters.range === '1h' ? 70 : 90}px 64px minmax(0, 1.4fr) minmax(0, 1fr) 96px 72px 86px`
	);
	const clock = $derived(filters.range === '1h' ? formatClock : formatDayClock);

	function ttftCell(r: RequestRecord): string {
		const ms = firstTokenMs(r);
		return r.status >= 400 || ms <= 0 ? '—' : formatMs(ms);
	}

	function ioCell(r: RequestRecord): string {
		// Zero here means upstream reported no count, as embeddings do.
		const count = (n: number) => (n > 0 ? formatTokens(n) : '—');
		return `${count(r.prompt_n + r.cache_n)} → ${count(r.predicted_n)}`;
	}

	const emptyDetail = $derived(
		server.state === 'running'
			? 'Calls through this server show here once they finish.'
			: `The router is ${server.state}, so nothing is being served.`
	);
</script>

<PageHeader
	title="Requests"
	subtitle="Every call to the server in {RANGES[filters.range]
		.phrase}, with llama.cpp's own timings"
>
	{#snippet actions()}
		<Segmented
			label="Endpoint"
			options={kinds}
			value={filters.kind}
			onchange={(kind) => navigate({ ...filters, kind, id: null })}
		/>
		<SelectField
			inline
			size={36}
			label="Model"
			options={modelOptions}
			value={filters.model}
			onchange={(model) => navigate({ ...filters, model, id: null })}
		/>
		<SelectField
			inline
			size={36}
			label="Range"
			options={rangeOptions}
			value={filters.range}
			onchange={(range) => navigate({ ...filters, range: range as Range, id: null })}
		/>
	{/snippet}
</PageHeader>

{#if failure && !summary}
	<Panel>
		<EmptyState title="Could not load requests" detail={failure}>
			{#snippet actions()}
				<Button size="sm" onclick={() => refresh(true)}>Try again</Button>
			{/snippet}
		</EmptyState>
	</Panel>
{:else if summary && summary.kpis.requests === 0}
	<Panel>
		<EmptyState title="No requests in {RANGES[filters.range].phrase}" detail={emptyDetail} />
	</Panel>
{:else if summary && scatter}
	<div class="results" class:stale={loading} aria-busy={loading}>
		<Band layout="grid" min={180}>
			{#each kpis as kpi (kpi.label)}
				<StatTile label={kpi.label} value={kpi.value} note={kpi.note} />
			{/each}
		</Band>

		<Band>
			<Panel flex="3 1 620px" radius={8} aria-labelledby="scatter-heading">
				<Scatter
					view={scatter}
					capped={summary.scatter.length < summary.kpis.requests}
					onselect={select}
				/>
			</Panel>
			<Panel flex="2 1 400px" radius={8} gap={18} aria-labelledby="time-heading">
				<SectionHeading
					id="time-heading"
					title="WHERE THE TIME GOES"
					subtitle="Median time per stage for each model"
				/>
				{#each summary.models as m (m.model)}
					<div class="model">
						<div class="model-head">
							<span class="ellipsis">{m.model || 'unknown model'}</span>
							<span class="mono-note">{formatMs(m.total_ms)} median</span>
						</div>
						<StackedBar
							ratio
							track={false}
							gap={2}
							height={14}
							label="{m.model}: queue, prefill and decode"
							segments={[
								{ value: m.queue_ms, queued: true },
								{ value: m.prefill_ms, color: tones.prefill },
								{ value: m.decode_ms, color: tones.decode }
							]}
						/>
						<span class="mono-note">
							queue {formatMs(m.queue_ms)} · prefill {formatMs(m.prefill_ms)} · decode
							{formatMs(m.decode_ms)}
						</span>
					</div>
				{/each}
				<div class="statuses">
					<Eyebrow>STATUS CODES</Eyebrow>
					<StatGrid items={statuses} columns={3} valueSize={16} labelStyle="code" />
				</div>
			</Panel>
		</Band>

		{#if summary.truncated}
			<span class="hint">Totals cover only the newest 50,000 requests of this range.</span>
		{/if}

		<Columns>
			<Panel flex="3 1 640px" gap={12} aria-labelledby="list-heading">
				<SectionHeading id="list-heading" title="RECENT · {formatInt(rows.length)} SHOWN">
					{#snippet actions()}
						<Button variant="ghost" size="sm" tone="muted" downloadHref={csvHref}>Export CSV</Button
						>
					{/snippet}
				</SectionHeading>
				<ScrollX minWidth={720}>
					<Row columns={rowColumns} gap={12} padding="0 10px 8px" header>
						<span>TIME</span><span>ID</span><span>ENDPOINT</span><span>MODEL</span>
						<span class="right">IN → OUT</span><span class="right">TTFT</span>
						<span class="right">STATUS</span>
					</Row>
					{#each rows as r (r.id)}
						<Row
							columns={rowColumns}
							gap={12}
							padding="0 10px"
							minHeight={40}
							selected={filters.id === r.id}
							onclick={() => select(r.id)}
						>
							<span class="dim">{clock(r.started_at)}</span>
							<span class="dim">#{r.id}</span>
							<span class="ellipsis">{r.method} {r.endpoint}</span>
							<span class="secondary ellipsis">{r.model}</span>
							<span class="right">{ioCell(r)}</span>
							<span class="right">{ttftCell(r)}</span>
							<span class="right ellipsis" style:color={statusColor[statusLevel(label(r))]}>
								{label(r)}
							</span>
						</Row>
					{/each}
				</ScrollX>
				{#if failure}<span class="hint">Could not refresh: {failure}</span>{/if}
				{#if cursor}
					<Button variant="ghost" size="sm" tone="muted" disabled={loadingMore} onclick={loadMore}>
						{loadingMore ? 'Loading…' : 'Load more'}
					</Button>
				{/if}
			</Panel>

			<Panel as="aside" flex="2 1 420px" gap={16} aria-labelledby="detail-heading">
				<Detail
					id={filters.id}
					capture={server.status?.capture}
					onload={(detail) => (selected = detail)}
				/>
			</Panel>
		</Columns>
	</div>
{/if}

<style>
	.results {
		display: flex;
		flex-direction: column;
		gap: var(--space-16);
		transition: opacity 0.15s ease;
	}

	/* The delay keeps a quick answer from flickering. */
	.stale {
		opacity: 0.55;
		transition-delay: 0.15s;
	}

	.model {
		display: flex;
		flex-direction: column;
		gap: var(--space-8);
	}

	.model-head {
		display: flex;
		justify-content: space-between;
		gap: var(--space-8);
		font-size: var(--fs-13);
	}

	.mono-note {
		flex: none;
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
	}

	.statuses {
		display: flex;
		flex-direction: column;
		gap: var(--space-8);
		margin-top: auto;
	}

	.hint {
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}

	.right {
		text-align: right;
	}

	.dim {
		color: var(--fg-muted);
	}

	.secondary {
		color: var(--fg-secondary);
	}
</style>
