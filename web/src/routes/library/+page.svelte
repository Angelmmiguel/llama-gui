<script lang="ts">
	import { resolve } from '$app/paths';
	import {
		ApiError,
		cancelDownload,
		deleteLocalModel,
		getHardware,
		getLibraryDisk,
		getLibraryLocal,
		getLibraryRepo,
		getLibrarySuggestions,
		listDownloads,
		loadModel,
		searchLibrary,
		startDownload,
		unloadModel,
		type DownloadJob,
		type Hardware,
		type LibraryDisk,
		type LibraryHardware,
		type LibrarySuggestion,
		type LocalModel,
		type RepoRow
	} from '$lib/api/client';
	import Band from '$lib/components/Band.svelte';
	import Button from '$lib/components/Button.svelte';
	import Columns from '$lib/components/Columns.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import Eyebrow from '$lib/components/Eyebrow.svelte';
	import Legend from '$lib/components/Legend.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import SearchField from '$lib/components/SearchField.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import Segmented from '$lib/components/Segmented.svelte';
	import Spinner from '$lib/components/Spinner.svelte';
	import StackedBar from '$lib/components/StackedBar.svelte';
	import TextLink from '$lib/components/TextLink.svelte';
	import ToggleChip from '$lib/components/ToggleChip.svelte';
	import { tones } from '$lib/components/tones';
	import { formatBytes } from '$lib/format';
	import { errorText, every } from '$lib/monitor/poll';
	import DownloadsPanel from './DownloadsPanel.svelte';
	import FitBar from './FitBar.svelte';
	import ModelRow from './ModelRow.svelte';
	import {
		bestVariant,
		contextLabel,
		CTX_OPTIONS,
		displayTags,
		downloadState,
		filterOptions,
		fitScale,
		fitTotal,
		headroomLabel,
		hubProblem,
		isHostOnly,
		jobPercent,
		KV_OPTIONS,
		localAction,
		localDescription,
		looksLikeRepo,
		machineLabel,
		matchesFilter,
		matchesLocal,
		nativeContext,
		paramsLabel,
		profileLabel,
		repoDescription,
		repoHint,
		serverFilter,
		verdictColor,
		verdictLabel,
		type ListFilter
	} from './view';

	const DEFAULT_CTX = 32768;
	const DEFAULT_KV = 'f16';
	const LOCAL_POLL_MS = 5000;
	const JOBS_POLL_MS = 1500;
	const FIT_DEBOUNCE_MS = 250;
	const QUERY_DEBOUNCE_MS = 400;
	const LOOKUPS_AT_ONCE = 3;

	const WHY: Record<string, string> = {
		largest_dense: 'LARGEST DENSE MODEL THAT FITS',
		fast_long_context: 'FAST AT LONG CONTEXT',
		most_capable_offload: 'MOST CAPABLE WITH OFFLOAD'
	};

	let tab = $state<'local' | 'hub'>('hub');
	let query = $state('');
	let filter = $state<ListFilter>('all');
	let ctx = $state<number>(DEFAULT_CTX);
	let kv = $state<string>(DEFAULT_KV);

	// What requests use: these follow the controls after a pause.
	let appliedCtx = $state<number>(DEFAULT_CTX);
	let appliedKv = $state<string>(DEFAULT_KV);
	let appliedQuery = $state('');

	let detected = $state<Hardware | null>(null);
	let machine = $state<LibraryHardware | null>(null);
	let local = $state<LocalModel[] | null>(null);
	let localError = $state('');
	let suggestions = $state<LibrarySuggestion[] | null>(null);
	let suggestionsError = $state('');
	let hub = $state<{ source: string; results: RepoRow[] } | null>(null);
	let hubError = $state('');
	let hubLoading = $state(false);
	// Rows of a plain search, as each one's lookup comes back.
	let lookups = $state<Record<string, RepoRow>>({});
	let lookupErrors = $state<Record<string, string>>({});
	let jobs = $state<DownloadJob[]>([]);
	let disk = $state<LibraryDisk | null>(null);
	let diskError = $state('');

	let expanded = $state<{ local?: string | null; hub?: string | null }>({});
	let busy = $state<Record<string, boolean>>({});
	let notice = $state<{ text: string; failed: boolean } | null>(null);
	let confirming = $state<string | null>(null);

	const scale = $derived(fitScale(machine));
	const hostOnly = $derived(isHostOnly(machine));
	const ctxLabel = $derived(contextLabel(appliedCtx).toUpperCase());
	const fitQuery = $derived({ ctx: appliedCtx, kv: appliedKv });
	const apiFilter = $derived(serverFilter(filter));

	const localRows = $derived((local ?? []).filter((m) => matchesLocal(m, query)));
	const hubRows = $derived(
		(hub?.results ?? []).map((r) => lookups[r.repo] ?? r).filter((r) => matchesFilter(r, filter))
	);

	const openLocal = $derived(
		expanded.local === undefined ? (localRows[0]?.path ?? null) : expanded.local
	);
	const openHub = $derived(expanded.hub === undefined ? (hubRows[0]?.repo ?? null) : expanded.hub);

	const listTitle = $derived.by(() => {
		if (tab === 'local') return `ON THIS MACHINE · ${disk?.path ?? 'MODELS'}`;
		if (hub?.source === 'catalog')
			return `HUGGING FACE · STARTER CATALOG · ${hubRows.length} MODELS`;
		const n = hubRows.length;
		return `HUGGING FACE · GGUF REPOS · ${n} RESULT${n === 1 ? '' : 'S'}`;
	});

	const legend = $derived([
		{ label: 'Weights', color: tones.weights },
		{ label: 'KV cache', color: tones.cached },
		{ label: 'Compute', color: tones.compute },
		...(scale ? [{ label: scale.label, color: tones.primary, shape: 'marker' as const }] : [])
	]);

	const diskParts = $derived.by(() => {
		if (!disk || disk.total_bytes <= 0) return null;
		const other = Math.max(0, disk.total_bytes - disk.free_bytes - disk.gguf_bytes);
		return {
			segments: [
				{ value: (disk.gguf_bytes / disk.total_bytes) * 100, color: tones.decode },
				{ value: (other / disk.total_bytes) * 100, color: tones.compute }
			],
			items: [
				{ label: 'GGUF models', color: tones.decode, value: formatBytes(disk.gguf_bytes) },
				{ label: 'Other files', color: tones.compute, value: formatBytes(other) },
				{ label: 'Free', shape: 'plain' as const, value: formatBytes(disk.free_bytes) }
			]
		};
	});

	$effect(() => {
		const nextCtx = ctx;
		const nextKv = kv;
		const timer = setTimeout(() => {
			appliedCtx = nextCtx;
			appliedKv = nextKv;
		}, FIT_DEBOUNCE_MS);
		return () => clearTimeout(timer);
	});

	$effect(() => {
		const next = query.trim();
		const timer = setTimeout(() => (appliedQuery = next), QUERY_DEBOUNCE_MS);
		return () => clearTimeout(timer);
	});

	async function fetchLocal(params: { ctx: number; kv: string }, signal?: AbortSignal) {
		try {
			const res = await getLibraryLocal(params, signal);
			if (signal?.aborted) return;
			local = res.models;
			machine = res.hardware;
			localError = '';
		} catch (e) {
			if (!signal?.aborted) localError = errorText(e);
		}
	}

	async function fetchDisk() {
		try {
			disk = await getLibraryDisk();
			diskError = '';
		} catch (e) {
			diskError = errorText(e);
		}
	}

	$effect(() => {
		const params = fitQuery;
		return every(LOCAL_POLL_MS, (signal) => fetchLocal(params, signal));
	});

	$effect(() => {
		void getHardware().then(
			(h) => (detected = h),
			() => {}
		);
		void fetchDisk();
	});

	$effect(() => {
		const params = fitQuery;
		const controller = new AbortController();
		suggestionsError = '';
		getLibrarySuggestions(params, controller.signal).then(
			(res) => {
				suggestions = res.suggestions;
				suggestionsError = '';
			},
			(e: unknown) => {
				if (!controller.signal.aborted) suggestionsError = errorText(e);
			}
		);
		return () => controller.abort();
	});

	$effect(() => {
		if (tab !== 'hub') return;
		const params = { ...fitQuery, q: appliedQuery, filter: apiFilter };
		const controller = new AbortController();
		hubLoading = true;
		searchLibrary(params, controller.signal)
			.then(
				(res) => {
					hub = { source: res.source, results: res.results };
					hubError = '';
				},
				(e: unknown) => {
					if (controller.signal.aborted) return;
					hub = null;
					hubError = errorText(e);
				}
			)
			.finally(() => {
				if (!controller.signal.aborted) hubLoading = false;
			});
		return () => controller.abort();
	});

	// A plain search knows no files or sizes. Asking for each row a few at a time keeps the list
	// quick to appear and the hub's rate limit out of reach; the server caches what comes back.
	$effect(() => {
		const rows = hub?.source === 'search' ? hub.results : [];
		const params = fitQuery;
		const controller = new AbortController();
		const pending = rows.map((r) => r.repo);
		const worker = async () => {
			for (let repo = pending.shift(); repo !== undefined; repo = pending.shift()) {
				try {
					const res = await getLibraryRepo(repo, params, controller.signal);
					if (controller.signal.aborted) return;
					lookups[repo] = res.repo;
				} catch (e) {
					if (controller.signal.aborted) return;
					lookupErrors[repo] = errorText(e);
					if (e instanceof ApiError && e.status === 429) {
						hubError = e.message;
						pending.length = 0;
					}
				}
			}
		};
		for (let i = 0; i < LOOKUPS_AT_ONCE; i++) void worker();
		return () => controller.abort();
	});

	$effect(() => {
		let known: Set<string> | null = null;
		return every(JOBS_POLL_MS, async (signal) => {
			try {
				const list = await listDownloads(signal);
				if (signal.aborted) return;
				jobs = list;
				const done = list.filter((j) => j.state === 'done').map((j) => j.id);
				if (known && done.some((id) => !known?.has(id))) refreshFiles();
				known = new Set(done);
			} catch {
				// The next tick tries again, and the list keeps its last good state meanwhile.
			}
		});
	});

	function refreshFiles() {
		void fetchLocal({ ctx: appliedCtx, kv: appliedKv });
		void fetchDisk();
	}

	function onQuery(value: string) {
		if (tab === 'local' && looksLikeRepo(value)) tab = 'hub';
	}

	async function act(key: string, work: () => Promise<unknown>, done?: string) {
		busy[key] = true;
		notice = null;
		try {
			await work();
			if (done) notice = { text: done, failed: false };
		} catch (e) {
			notice = { text: errorText(e), failed: true };
		} finally {
			delete busy[key];
		}
	}

	async function download(repo: string, files: readonly string[]) {
		await act(`dl:${repo}:${files[0]}`, () => startDownload(repo, files));
		try {
			jobs = await listDownloads();
		} catch {
			// The poll catches up.
		}
	}

	function cancel(job: DownloadJob) {
		void act(`job:${job.id}`, () => cancelDownload(job.id, true));
	}

	async function routerAction(m: LocalModel, kind: 'load' | 'unload', id: string) {
		await act(`model:${m.path}`, () => (kind === 'load' ? loadModel(id) : unloadModel(id)));
		refreshFiles();
	}

	async function removeModel(m: LocalModel) {
		confirming = null;
		await act(`delete:${m.path}`, () => deleteLocalModel(m.path), `Deleted ${m.path}.`);
		refreshFiles();
	}

	const toggle = (which: 'local' | 'hub', id: string, open: boolean) => {
		expanded[which] = open ? null : id;
	};
</script>

{#snippet search()}
	<SearchField
		label="Search models"
		bind:value={query}
		placeholder="Search models or paste user/repo"
		minWidth="280px"
		flex="1 1 280px"
		oninput={onQuery}
	/>
{/snippet}

{#snippet actions()}
	<div class="header-actions">
		<Segmented
			label="Library source"
			tabs
			bind:value={tab}
			options={[
				{ value: 'local', label: local ? `On this machine · ${local.length}` : 'On this machine' },
				{ value: 'hub', label: 'Hugging Face' }
			]}
		/>
		{@render search()}
	</div>
{/snippet}

{#snippet downloadButton(
	repo: string,
	files: readonly string[],
	label: string,
	size: 'sm' | 'ml',
	block: boolean
)}
	{@const state = downloadState(repo, files, jobs, local ?? [])}
	{#if state.kind === 'on-disk'}
		<Button variant="ghost" {size} {block} disabled>On disk</Button>
	{:else if state.kind === 'active'}
		<Button variant="ghost" {size} {block} disabled>
			{state.job.state === 'queued' ? 'Queued' : `${jobPercent(state.job)}%`}
		</Button>
	{:else}
		<Button
			variant="ghost"
			tone={size === 'sm' ? 'accent' : undefined}
			{size}
			{block}
			disabled={busy[`dl:${repo}:${files[0]}`]}
			onclick={() => download(repo, files)}
		>
			{label}
		</Button>
	{/if}
{/snippet}

{#snippet alert(text: string)}
	<p class="alert" role="alert">{text}</p>
{/snippet}

{#snippet loading(text: string)}
	<p class="loading"><Spinner size={10} />{text}</p>
{/snippet}

{#snippet variantHeader()}
	<div class="variant variant-head" aria-hidden="true">
		<span>QUANT</span><span>FILE</span><span>MEMORY AT {ctxLabel}</span><span>VERDICT</span><span
		></span>
	</div>
{/snippet}

{#snippet localRow(m: LocalModel)}
	{@const open = openLocal === m.path}
	{@const action = localAction(m)}
	<ModelRow
		name={m.name}
		source={m.path}
		tags={displayTags(m.fit.moe ? ['moe'] : [], m.metadata)}
		loaded={m.status === 'loaded'}
		params={paramsLabel(m.metadata)}
		context={profileLabel(m)}
		{open}
		ontoggle={() => toggle('local', m.path, open)}
	>
		{#snippet fit()}
			<FitBar fit={m.fit} {scale} prefix={m.quant || undefined} />
		{/snippet}
		<p class="description">{localDescription(m)}</p>
		{@render variantHeader()}
		<div class="variant">
			<span>{m.quant || 'unknown'}</span>
			<span class="dim">{formatBytes(m.size_bytes)}</span>
			<span class="variant-bar"><FitBar fit={m.fit} {scale} bare /></span>
			<span style:color={verdictColor(m.fit)}>{verdictLabel(m.fit)} · {fitTotal(m.fit)}</span>
			<span class="variant-action">
				{#if action.kind === 'profile'}
					<Button
						variant="ghost"
						tone="accent"
						size="sm"
						block
						href={`${resolve('/profiles')}?new=${encodeURIComponent(action.path)}`}
						title="Create a profile, which is what makes this model loadable"
					>
						Load
					</Button>
				{:else if action.kind === 'busy'}
					<Button variant="ghost" size="sm" block disabled>{action.label}</Button>
				{:else}
					<Button
						variant="ghost"
						tone={action.kind === 'load' ? 'accent' : 'muted'}
						size="sm"
						block
						disabled={busy[`model:${m.path}`]}
						onclick={() => routerAction(m, action.kind === 'load' ? 'load' : 'unload', action.id)}
					>
						{action.kind === 'load' ? 'Load' : 'Unload'}
					</Button>
				{/if}
			</span>
		</div>
		<div class="foot">
			{#if confirming === m.path}
				<span class="hint" role="alert">
					Delete {m.path} from disk{m.profile
						? `, leaving the profile ${m.profile.alias} without a model`
						: ''}? This can't be undone.
				</span>
				<span class="foot-actions">
					<Button
						variant="danger"
						size="sm"
						disabled={busy[`delete:${m.path}`]}
						onclick={() => removeModel(m)}
					>
						Delete model
					</Button>
					<Button variant="ghost" size="sm" onclick={() => (confirming = null)}>Keep</Button>
				</span>
			{:else}
				<span class="hint">
					{#if m.profile}
						<TextLink href={resolve('/profiles')}>Open in Profiles</TextLink> to change context{hostOnly
							? ' and slots'
							: ', slots and GPU layers'}.
					{:else}
						Load opens a new profile for this file, where you set its context and slots.
					{/if}
				</span>
				<span class="foot-actions">
					<Button
						variant="danger"
						size="sm"
						disabled={m.status === 'loaded' || m.status === 'loading'}
						title={m.status === 'loaded' ? 'Unload it first' : undefined}
						onclick={() => (confirming = m.path)}
					>
						Delete
					</Button>
				</span>
			{/if}
		</div>
	</ModelRow>
{/snippet}

{#snippet hubRow(r: RepoRow)}
	{@const open = openHub === r.repo}
	{@const best = bestVariant(r)}
	<ModelRow
		name={r.name}
		source={r.repo}
		tags={displayTags(r.tags, r.metadata)}
		params={paramsLabel(r.metadata)}
		context={nativeContext(r.metadata) ||
			(r.downloads ? `${r.downloads.toLocaleString('en-US')} downloads` : undefined)}
		{open}
		ontoggle={() => toggle('hub', r.repo, open)}
	>
		{#snippet fit()}
			{#if best}
				<FitBar fit={best.fit} {scale} prefix={best.quant} />
			{:else if lookupErrors[r.repo]}
				<span class="dim small">Can't check this one</span>
			{:else if r.variants}
				<span class="dim small">No GGUF files</span>
			{:else}
				<span class="dim small"><Spinner size={10} /> Checking</span>
			{/if}
		{/snippet}
		<p class="description">{repoDescription(r)}</p>
		{#if lookupErrors[r.repo]}
			{@render alert(lookupErrors[r.repo])}
		{:else if !r.variants}
			{@render loading('Reading the repository')}
		{:else if r.variants.length === 0}
			<EmptyState title="No GGUF files in this repository" />
		{:else}
			{#if r.metadata_error}
				<p class="hint">
					Sizes only, as the file's header could not be read: {r.metadata_error}
				</p>
			{/if}
			{@render variantHeader()}
			{#each r.variants as v (v.id)}
				<div class="variant">
					<span>{v.quant}{v.shards > 1 ? ` × ${v.shards}` : ''}</span>
					<span class="dim">{formatBytes(v.size_bytes)}</span>
					<span class="variant-bar"><FitBar fit={v.fit} {scale} bare /></span>
					<span style:color={verdictColor(v.fit)}>{verdictLabel(v.fit)} · {fitTotal(v.fit)}</span>
					<span class="variant-action">
						{@render downloadButton(r.repo, v.download_files, 'Download', 'sm', true)}
					</span>
				</div>
			{/each}
			<span class="hint">{repoHint(r, disk?.path ?? '')}</span>
		{/if}
	</ModelRow>
{/snippet}

<PageHeader
	title="Library"
	subtitle="Your GGUF models and everything on Hugging Face, checked against your hardware"
	{actions}
/>

<Panel aria-label="Fit estimate settings" row padding="14px 20px" gap={12} class="fit-settings">
	<div class="machine">
		<svg
			aria-hidden="true"
			width="18"
			height="18"
			viewBox="0 0 24 24"
			fill="none"
			stroke="currentColor"
			stroke-width="1.8"
			stroke-linecap="round"
			stroke-linejoin="round"
		>
			<rect x="3" y="6" width="18" height="12" rx="2"></rect>
			<path d="M7 10h4v4H7zM15 10h2M15 14h2M7 3v3M17 3v3M7 18v3M17 18v3"></path>
		</svg>
		<div class="machine-text">
			<span>Estimating fit for <span class="mono">{machineLabel(detected, machine)}</span></span>
			<span class="dim small">
				Weights + KV cache at your context size + compute buffers. {headroomLabel(machine)}
			</span>
		</div>
	</div>
	<div class="fit-controls">
		<span class="control">
			<span class="dim small">Context</span>
			<Segmented label="Context size" surface="ground" bind:value={ctx} options={CTX_OPTIONS} />
		</span>
		<span class="control">
			<span class="dim small">KV cache</span>
			<Segmented label="KV cache type" surface="ground" bind:value={kv} options={KV_OPTIONS} />
		</span>
	</div>
</Panel>

{#if notice}
	<p class={['notice', { failed: notice.failed }]} role={notice.failed ? 'alert' : 'status'}>
		{notice.text}
	</p>
{/if}

<section aria-labelledby="sg-h" class="suggestions">
	<SectionHeading
		title={`SUGGESTED FOR THIS MACHINE AT ${ctxLabel} CONTEXT`}
		id="sg-h"
		align="baseline"
	>
		{#snippet actions()}
			<span class="dim small">Updates when you change context or KV cache type</span>
		{/snippet}
	</SectionHeading>
	{#if suggestionsError}
		{@render alert(hubProblem(suggestionsError))}
	{:else if !suggestions}
		{@render loading('Checking the starter catalog on Hugging Face')}
	{:else if suggestions.length === 0}
		<Panel>
			<EmptyState
				title="Nothing in the starter catalog fits at this context"
				detail="Try a shorter context or a smaller KV cache type, or search for a smaller model."
			/>
		</Panel>
	{:else}
		<Band layout="grid" min={300}>
			{#each suggestions as s (s.category)}
				<article class="card">
					<Eyebrow color={tones.decode}>{WHY[s.category] ?? s.category.toUpperCase()}</Eyebrow>
					<div class="card-title">
						<h3>{s.name}</h3>
						<span class="mono dim">{s.quant} · {paramsLabel(s.metadata)}</span>
					</div>
					<FitBar fit={s.fit} {scale} height={12} />
					<p class="description">{s.note}</p>
					<div class="card-action">
						{@render downloadButton(
							s.repo,
							s.download_files,
							`Download ${s.quant} · ${formatBytes(s.download_bytes)}`,
							'ml',
							true
						)}
					</div>
				</article>
			{/each}
		</Band>
	{/if}
</section>

<Columns>
	<Panel aria-labelledby="ml-h" flex="3 1 760px" gap={12}>
		<SectionHeading title={listTitle} id="ml-h">
			{#snippet actions()}
				<Legend items={legend} gap={12} />
			{/snippet}
		</SectionHeading>

		{#if tab === 'hub'}
			<div class="filters">
				{#each filterOptions(hostOnly) as option (option.value)}
					<ToggleChip
						variant="pill"
						pressed={filter === option.value}
						onclick={() => (filter = option.value)}
					>
						{option.label}
					</ToggleChip>
				{/each}
			</div>
			{#if hubError}
				{@render alert(hubProblem(hubError))}
			{/if}
			{#if hubRows.length > 0}
				<div class="rows">
					{#each hubRows as r (r.repo)}
						{@render hubRow(r)}
					{/each}
				</div>
			{:else if hubLoading}
				{@render loading('Asking Hugging Face')}
			{:else if hubError}
				<EmptyState title="Nothing to show from Hugging Face right now">
					{#snippet actions()}
						<Button variant="ghost" size="sm" onclick={() => (tab = 'local')}>
							Show this machine
						</Button>
					{/snippet}
				</EmptyState>
			{:else}
				<EmptyState
					title={appliedQuery ? `Nothing matches “${appliedQuery}”` : 'Nothing to show'}
					detail={filter === 'all'
						? 'Try other words, or paste a repository such as user/repo.'
						: 'Try another filter, or All.'}
				/>
			{/if}
		{:else}
			{#if localError}
				{@render alert(localError)}
			{/if}
			{#if localRows.length > 0}
				<div class="rows">
					{#each localRows as m (m.path)}
						{@render localRow(m)}
					{/each}
				</div>
			{:else if local === null}
				{#if !localError}{@render loading('Reading the models directory')}{/if}
			{:else if local.length === 0}
				<EmptyState
					title="No models on this machine"
					detail={`GGUF files in ${disk?.path ?? 'the models directory'} show here. Download one from Hugging Face to start.`}
				>
					{#snippet actions()}
						<Button variant="ghost" size="sm" onclick={() => (tab = 'hub')}
							>Browse Hugging Face</Button
						>
					{/snippet}
				</EmptyState>
			{:else}
				<EmptyState title={`No local model matches “${query.trim()}”`} />
			{/if}
		{/if}
	</Panel>

	<aside class="side">
		<DownloadsPanel {jobs} oncancel={cancel} />

		<Panel aria-labelledby="dk-h" gap={12}>
			<SectionHeading title={`DISK · ${disk?.path ?? 'MODELS'}`} id="dk-h" />
			{#if diskError}
				{@render alert(diskError)}
			{:else if diskParts}
				<StackedBar segments={diskParts.segments} label="Disk use" />
				<Legend items={diskParts.items} stack />
			{:else}
				{@render loading('Measuring')}
			{/if}
		</Panel>

		<Panel aria-labelledby="hw-h" gap={10}>
			<SectionHeading title="HOW TO READ THE VERDICTS" id="hw-h" />
			<div class="verdicts">
				{#if hostOnly}
					<span>
						<span style:color={tones.ok}>Fits in RAM</span>: everything runs from system memory on
						the CPU, with the headroom left over.
					</span>
					<span>
						<span style:color={tones.errorSoft}>Too large</span>: won't fit in RAM at this context.
					</span>
				{:else}
					<span>
						<span style:color={tones.ok}>Fits on GPU</span>: everything on the GPUs. Fastest.
					</span>
					<span>
						<span style:color={tones.warn}>Partial offload</span>: some layers, or MoE experts with
						<code>--n-cpu-moe</code>, run from system RAM. Works, but slower.
					</span>
					<span>
						<span style:color={tones.errorSoft}>Too large</span>: won't fit in VRAM and RAM together
						at this context.
					</span>
				{/if}
			</div>
		</Panel>
	</aside>
</Columns>

<style>
	.header-actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-8);
		flex: 1 1 auto;
	}

	:global(.fit-settings) {
		align-items: center;
		justify-content: space-between;
	}

	.machine {
		display: flex;
		align-items: center;
		gap: var(--space-14);
		color: var(--fg-muted);
		min-width: 0;
	}

	.machine svg {
		flex: none;
	}

	.machine-text {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		font-size: var(--fs-13);
		color: var(--fg-primary);
		min-width: 0;
	}

	.mono {
		font-family: var(--font-mono);
	}

	.dim {
		color: var(--fg-muted);
	}

	.small {
		font-size: var(--fs-12);
	}

	.fit-controls {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-14);
	}

	.control {
		display: flex;
		align-items: center;
		gap: var(--space-8);
	}

	.suggestions {
		display: flex;
		flex-direction: column;
		gap: var(--space-10);
	}

	.card {
		background: var(--bg-panel);
		padding: var(--space-18) var(--space-20);
		display: flex;
		flex-direction: column;
		gap: var(--space-12);
		min-width: 0;
	}

	.card-title {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		font-size: var(--fs-12);
	}

	h3 {
		margin: 0;
		font-size: var(--fs-16);
		font-weight: 600;
		overflow-wrap: anywhere;
	}

	.card-action {
		margin-top: auto;
	}

	.description {
		margin: 0;
		font-size: var(--fs-13);
		line-height: 1.5;
		color: var(--fg-tertiary);
	}

	.filters {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-6);
	}

	.rows {
		display: flex;
		flex-direction: column;
	}

	.variant {
		display: grid;
		grid-template-columns: 100px 70px minmax(0, 1fr) 230px 110px;
		align-items: center;
		gap: var(--space-14);
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}

	.variant-head {
		font-size: var(--fs-10);
		letter-spacing: 0.06em;
		color: var(--fg-faint);
	}

	.foot {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-8) var(--space-14);
	}

	.foot .hint {
		flex: 1 1 240px;
	}

	.foot-actions {
		display: flex;
		gap: var(--space-8);
	}

	.hint {
		font-size: var(--fs-12);
		color: var(--fg-muted);
		margin: 0;
	}

	.hint :global(a) {
		font-size: var(--fs-12);
	}

	.side {
		flex: 1 1 320px;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-16);
	}

	.verdicts {
		display: flex;
		flex-direction: column;
		gap: var(--space-10);
		font-size: var(--fs-13);
		line-height: 1.5;
		color: var(--fg-tertiary);
	}

	code {
		font-family: var(--font-mono);
	}

	.notice {
		margin: 0;
		font-size: var(--fs-13);
		color: var(--fg-secondary);
	}

	.failed {
		color: var(--color-error-soft);
	}

	.alert {
		margin: 0;
		padding: var(--space-10) var(--space-14);
		font-size: var(--fs-13);
		line-height: 1.5;
		color: var(--color-warn);
		background: var(--tint-warn);
		border: 1px solid var(--border-panel);
		border-radius: 8px;
	}

	.loading {
		margin: 0;
		display: flex;
		align-items: center;
		gap: var(--space-8);
		font-size: var(--fs-13);
		color: var(--fg-muted);
	}

	@media (max-width: 720px) {
		.machine {
			align-items: flex-start;
		}

		.variant-head {
			display: none;
		}

		.variant {
			grid-template-columns: minmax(0, 1fr) auto;
			gap: var(--space-6) var(--space-10);
			padding-block: var(--space-6);
			border-top: 1px solid var(--border-row);
		}

		.variant-bar,
		.variant-action {
			grid-column: 1 / -1;
		}

		.variant > :nth-child(4) {
			grid-column: 1 / -1;
		}
	}
</style>
