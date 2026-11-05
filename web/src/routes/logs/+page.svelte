<script lang="ts">
	import { tick, untrack } from 'svelte';
	import { SvelteSet } from 'svelte/reactivity';
	import {
		getLogVolume,
		listEvents,
		listLogs,
		LOGS_STREAM,
		logsDownloadUrl,
		stream,
		type LogRecord,
		type LogVolume,
		type ServerEvent
	} from '$lib/api/client';
	import AxisLabels from '$lib/components/AxisLabels.svelte';
	import Button from '$lib/components/Button.svelte';
	import Columns from '$lib/components/Columns.svelte';
	import Cursor from '$lib/components/Cursor.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import Legend from '$lib/components/Legend.svelte';
	import LiveToggle from '$lib/components/LiveToggle.svelte';
	import LogLine from '$lib/components/LogLine.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import SearchField from '$lib/components/SearchField.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import Segmented from '$lib/components/Segmented.svelte';
	import StatGrid from '$lib/components/StatGrid.svelte';
	import ToggleChip from '$lib/components/ToggleChip.svelte';
	import { sourceColor, tones } from '$lib/components/tones';
	import { formatClock, formatInt } from '$lib/format';
	import { server } from '$lib/state/server.svelte';
	import {
		appendCapped,
		atBottom,
		groupEvents,
		KNOWN_SOURCES,
		lineText,
		localMidnight,
		logQuery,
		matchesView,
		MAX_LINES,
		type LevelChoice,
		type LogView
	} from './view';

	const INITIAL_LINES = 1000;
	const FLUSH_MS = 250;
	const POLL_MS = 20_000;
	const DEBOUNCE_MS = 250;

	const levelOptions: { value: LevelChoice; label: string }[] = [
		{ value: 'all', label: 'All' },
		{ value: 'warn', label: 'Warn+' },
		{ value: 'error', label: 'Errors' }
	];

	let search = $state('');
	let q = $state('');
	let level = $state<LevelChoice>('all');
	let off = $state<string[]>([]);
	let seen = $state<string[]>([...KNOWN_SOURCES]);
	const logView: LogView = $derived({ q, level, off });
	const query = $derived(logQuery(logView, seen));

	let lines = $state<LogRecord[]>([]);
	let loading = $state(true);
	let failure = $state('');
	let tailing = $state(true);
	let connected = $state(false);
	let following = $state(true);
	let missed = $state(0);
	const expanded = new SvelteSet<number>();
	let viewport = $state<HTMLElement>();

	let volume = $state<LogVolume | null>(null);
	let events = $state<ServerEvent[]>([]);
	let sideFailure = $state('');

	// Live lines wait here while a fetch runs, and are drawn in batches.
	let pending: LogRecord[] = [];
	let lastId = 0;
	let run = 0;

	function noteSources(records: readonly LogRecord[]) {
		const fresh = [...new Set(records.map((l) => l.source))].filter((s) => !seen.includes(s));
		if (fresh.length > 0) seen = [...seen, ...fresh];
	}

	async function reload(follow = true) {
		const current = ++run;
		loading = true;
		try {
			// With every source off nothing can match, and an empty list means no filter to the API.
			const fetched = seen.every((s) => off.includes(s))
				? { lines: [] }
				: await listLogs({ ...query, limit: INITIAL_LINES });
			if (current !== run) return;
			const ordered = fetched.lines.toReversed();
			noteSources(ordered);
			lines = appendCapped(ordered, []);
			lastId = Math.max(lastId, ordered.at(-1)?.id ?? 0);
			pending = pending.filter((l) => l.id > lastId);
			failure = '';
			if (follow) missed = 0;
			if (follow) following = true;
		} catch (e) {
			if (current !== run) return;
			failure = e instanceof Error ? e.message : String(e);
		} finally {
			if (current === run) {
				loading = false;
				void toBottom();
			}
		}
	}

	async function toBottom() {
		await tick();
		if (following && viewport) viewport.scrollTop = viewport.scrollHeight;
	}

	function flush() {
		if (loading || pending.length === 0) return;
		const batch = pending;
		pending = [];
		noteSources(batch);
		const fresh = batch.filter((l) => l.id > lastId && matchesView(logView, l));
		lastId = Math.max(lastId, batch.at(-1)?.id ?? 0);
		if (fresh.length === 0) return;
		// Measured now, not taken from the last scroll event: one still on its way would
		// otherwise lose to the scroll below and pull a reader back down.
		if (viewport) onscroll();
		lines = appendCapped(lines, fresh);
		if (following) void toBottom();
		else missed += fresh.length;
	}

	// Debounced so typing does not fetch per keystroke.
	$effect(() => {
		const value = search;
		const timer = setTimeout(() => (q = value), DEBOUNCE_MS);
		return () => clearTimeout(timer);
	});

	// A new source joining the toggles changes the object but not the request.
	const queryKey = $derived(JSON.stringify(query));

	$effect(() => {
		void queryKey;
		untrack(reload);
	});

	$effect(() => {
		if (!tailing) return;
		// Lines may have gone by while paused, and the backlog of the stream is unfiltered.
		if (lastId > 0) untrack(reload);
		let dropped = false;
		const stop = stream<LogRecord>(LOGS_STREAM, (line) => pending.push(line), {
			event: 'log',
			query: { backlog: 1 },
			onStatus: (up) => {
				// The server drops a tail that falls behind, so what passed meanwhile is fetched.
				if (up && dropped) void reload(false);
				dropped = !up;
				connected = up;
			}
		});
		const timer = setInterval(flush, FLUSH_MS);
		return () => {
			stop();
			clearInterval(timer);
			connected = false;
			pending = [];
		};
	});

	async function loadSide() {
		try {
			const [nextVolume, nextEvents] = await Promise.all([
				getLogVolume(),
				listEvents(localMidnight(Date.now()))
			]);
			volume = nextVolume;
			events = nextEvents.events;
			sideFailure = '';
		} catch (e) {
			sideFailure = e instanceof Error ? e.message : String(e);
		}
	}

	$effect(() => {
		untrack(loadSide);
		const timer = setInterval(() => {
			if (document.visibilityState === 'visible') void loadSide();
		}, POLL_MS);
		return () => clearInterval(timer);
	});

	function onscroll() {
		if (!viewport) return;
		following = atBottom(viewport.scrollTop, viewport.clientHeight, viewport.scrollHeight);
		if (following) missed = 0;
	}

	function jumpToLatest() {
		following = true;
		missed = 0;
		void toBottom();
	}

	function toggleSource(source: string) {
		off = off.includes(source) ? off.filter((s) => s !== source) : [...off, source];
	}

	function toggleLine(id: number) {
		if (expanded.has(id)) expanded.delete(id);
		else expanded.add(id);
	}

	const minutes = $derived(volume?.minutes ?? []);
	const tallest = $derived(
		Math.max(1, ...minutes.map((m) => m.debug + m.info + m.warn + m.error + m.other))
	);
	const hasVolume = $derived(tallest > 1 || minutes.some((m) => m.info + m.warn + m.error > 0));
	const hasOther = $derived(minutes.some((m) => m.debug + m.other > 0));
	const legend = $derived([
		{ label: 'Info', color: 'var(--border-raised)' },
		...(hasOther ? [{ label: 'Debug and other', color: 'var(--border-ghost)' }] : []),
		{ label: 'Warn', color: tones.warn },
		{ label: 'Error', color: tones.error }
	]);
	const axis = $derived(
		minutes.length === 0
			? []
			: [0, 15, 30, 45].map((i) => formatClock(minutes[i]?.minute).slice(0, 5)).concat('now')
	);

	const severityColor: Record<string, string> = {
		info: tones.prefill,
		warn: tones.warn,
		error: tones.error
	};
	const groups = $derived(groupEvents(events));
	const today = $derived([
		{
			label: 'Warnings today',
			value: formatInt(volume?.today.warn),
			valueColor: tones.warn
		},
		{
			label: 'Errors today',
			value: formatInt(volume?.today.error),
			valueColor: tones.error
		}
	]);

	const filtered = $derived(search.trim() !== '' || level !== 'all' || off.length > 0);
	const running = $derived(server.state === 'running');
</script>

{#snippet subtitle()}
	llama-server output, parsed by source and level
	{#if server.status?.log_verbosity !== undefined}
		· verbosity <span class="mono">-lv {server.status.log_verbosity}</span>
	{/if}
{/snippet}

<PageHeader title="Logs" {subtitle}>
	{#snippet actions()}
		<Button downloadHref={logsDownloadUrl(query)}>Download .log</Button>
		<LiveToggle
			bind:live={tailing}
			label={connected ? 'tailing' : 'connecting'}
			pausedLabel="paused"
		/>
	{/snippet}
</PageHeader>

<Panel gap={12} aria-labelledby="volume-heading">
	<SectionHeading id="volume-heading" title="LOG VOLUME · LAST 60 MIN">
		{#snippet actions()}<Legend items={legend} />{/snippet}
	</SectionHeading>
	{#if hasVolume}
		<div
			class="volume"
			role="img"
			aria-label="Stacked bars of log lines per minute by level over the last hour"
		>
			{#each minutes as m (m.minute)}
				<div
					class="minute"
					title="{formatClock(m.minute).slice(0, 5)}: {formatInt(
						m.debug + m.info + m.warn + m.error + m.other
					)} lines"
				>
					{#if m.error}<div class="error" style:height="{(m.error / tallest) * 100}%"></div>{/if}
					{#if m.warn}<div class="warn" style:height="{(m.warn / tallest) * 100}%"></div>{/if}
					{#if m.info}<div class="info" style:height="{(m.info / tallest) * 100}%"></div>{/if}
					{#if m.debug + m.other}
						<div class="other" style:height="{((m.debug + m.other) / tallest) * 100}%"></div>
					{/if}
				</div>
			{/each}
		</div>
		<AxisLabels labels={axis} />
	{:else if sideFailure && !volume}
		<EmptyState title="Could not load the volume" detail={sideFailure} />
	{:else if volume}
		<EmptyState title="No log lines in the last hour" />
	{/if}
</Panel>

<Columns>
	<Panel tone="deep" flex="3 1 700px" padding="0" gap={0} aria-labelledby="lines-heading">
		<h2 id="lines-heading" class="visually-hidden">Log lines</h2>
		<div class="filters">
			<SearchField
				label="Filter log lines"
				mono
				flex="1 1 260px"
				placeholder="Filter, e.g. task 4826 or cuda"
				bind:value={search}
			/>
			<Segmented label="Level" options={levelOptions} bind:value={level} />
			<div class="sources" role="group" aria-label="Source">
				{#each seen as source (source)}
					<ToggleChip
						variant="source"
						color={sourceColor(source)}
						pressed={!off.includes(source)}
						onclick={() => toggleSource(source)}
					>
						{source || 'none'}
					</ToggleChip>
				{/each}
			</div>
		</div>

		<div class="frame">
			<div class="viewport" bind:this={viewport} {onscroll}>
				{#if lines.length > 0}
					<div class="rows">
						{#each lines as line (line.id)}
							<LogLine
								n={line.id}
								ts={formatClock(line.ts)}
								level={line.level.toUpperCase()}
								source={line.source}
								model={line.model}
								msg={lineText(line)}
								expanded={expanded.has(line.id)}
								ontoggle={() => toggleLine(line.id)}
							/>
						{/each}
						{#if tailing}<div class="caret"><Cursor /></div>{/if}
					</div>
				{:else if failure}
					<EmptyState title="Could not load logs" detail={failure}>
						{#snippet actions()}
							<Button size="sm" onclick={() => reload()}>Try again</Button>
						{/snippet}
					</EmptyState>
				{:else if !loading}
					{#if filtered}
						<EmptyState title="No lines match these filters." />
					{:else if !running}
						<EmptyState
							title="No log lines yet"
							detail="The router is {server.state}: start it to see llama-server's output here."
						/>
					{:else}
						<EmptyState title="No log lines yet" detail="llama-server has not said anything." />
					{/if}
				{/if}
			</div>
			{#if !following && lines.length > 0}
				<div class="jump">
					<Button size="sm" onclick={jumpToLatest}>
						{missed > 0 ? `${formatInt(missed)} new · jump to latest` : 'Jump to latest'}
					</Button>
				</div>
			{/if}
		</div>
		<div class="foot">
			<span>Newest {formatInt(MAX_LINES)} lines at most; click a line to read it in full.</span>
			{#if !running && lines.length > 0}
				<span>Router {server.state}: no new lines until it starts.</span>
			{/if}
		</div>
	</Panel>

	<Panel as="aside" flex="1 1 320px" aria-labelledby="events-heading">
		<SectionHeading id="events-heading" title="EVENTS · TODAY" />
		{#if groups.length > 0}
			<ol class="events">
				{#each groups as group (group.id)}
					<li>
						<span class="rail">
							<span class="pin" style:background={severityColor[group.severity] ?? tones.muted}
							></span>
							<span class="wire"></span>
						</span>
						<span class="event">
							<span class="title">
								{group.title}{#if group.count > 1}&nbsp;× {group.count}{/if}
							</span>
							<span class="meta">
								{group.count > 1 ? `${formatClock(group.oldest)}–` : ''}{formatClock(group.newest)}
								{#if group.meta}· {group.meta}{/if}
							</span>
						</span>
					</li>
				{/each}
			</ol>
		{:else if sideFailure}
			<EmptyState title="Could not load events" detail={sideFailure} />
		{:else}
			<EmptyState title="Nothing happened today" />
		{/if}
		{#if volume}<StatGrid items={today} columns={2} valueSize={16} pushDown />{/if}
	</Panel>
</Columns>

<style>
	.mono {
		font-family: var(--font-mono);
	}

	.visually-hidden {
		position: absolute;
		width: 1px;
		height: 1px;
		overflow: hidden;
		clip: rect(0 0 0 0);
	}

	.volume {
		display: flex;
		align-items: flex-end;
		gap: 3px;
		height: 90px;
	}

	.minute {
		flex: 1 1 0;
		min-width: 0;
		height: 100%;
		display: flex;
		flex-direction: column;
		justify-content: flex-end;
		gap: 1px;
	}

	.minute > div {
		border-radius: 1px;
	}

	.info {
		background: var(--border-raised);
	}

	.other {
		background: var(--border-ghost);
	}

	.warn {
		background: var(--color-warn);
	}

	.error {
		background: var(--color-error);
	}

	.filters {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-10);
		padding: 14px var(--space-16);
		border-bottom: 1px solid var(--border-panel);
	}

	.sources {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-6);
	}

	.frame {
		position: relative;
	}

	.viewport {
		height: 640px;
		max-height: 70vh;
		overflow: auto;
		padding: var(--space-8) 0;
		box-sizing: border-box;
	}

	.rows {
		min-width: 860px;
	}

	.caret {
		padding: 3px var(--space-16) 3px 82px;
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		line-height: 1.5;
	}

	.jump {
		position: absolute;
		right: var(--space-16);
		bottom: var(--space-16);
	}

	.foot {
		display: flex;
		flex-wrap: wrap;
		justify-content: space-between;
		gap: var(--space-8);
		padding: var(--space-8) var(--space-16);
		border-top: 1px solid var(--border-panel);
		font-size: var(--fs-11);
		color: var(--fg-faint);
	}

	.events {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		max-height: 560px;
		overflow-y: auto;
	}

	.events li {
		display: grid;
		grid-template-columns: 14px minmax(0, 1fr);
		gap: var(--space-12);
	}

	.rail {
		display: flex;
		flex-direction: column;
		align-items: center;
	}

	.pin {
		flex: none;
		width: 10px;
		height: 10px;
		margin-top: 4px;
		border-radius: 50%;
	}

	.wire {
		flex: 1;
		width: 1px;
		min-height: 18px;
		background: var(--border-control);
	}

	.event {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		padding-bottom: 14px;
		min-width: 0;
	}

	.title {
		font-size: var(--fs-13);
		overflow-wrap: anywhere;
	}

	.meta {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
		overflow-wrap: anywhere;
	}

	@media (max-width: 640px) {
		.volume {
			gap: 1px;
		}
	}
</style>
