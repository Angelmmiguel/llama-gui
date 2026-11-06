<script lang="ts">
	import { resolve } from '$app/paths';
	import {
		cancelBenchRun,
		estimateBench,
		getBenchHistory,
		getBenchRun,
		getCurrentBenchRun,
		listBenchRuns,
		listBuilds,
		listLocalModelFiles,
		startBenchRun,
		type BenchBuildPoint,
		type BenchEstimate,
		type BenchRun,
		type BenchRunDetail,
		type BenchSetup,
		type BuildManifest,
		type LocalModelFile
	} from '$lib/api/client';
	import Band from '$lib/components/Band.svelte';
	import Button from '$lib/components/Button.svelte';
	import Columns from '$lib/components/Columns.svelte';
	import CommandBlock from '$lib/components/CommandBlock.svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import Legend from '$lib/components/Legend.svelte';
	import LineChart from '$lib/components/LineChart.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import ProgressBar from '$lib/components/ProgressBar.svelte';
	import ScrollX from '$lib/components/ScrollX.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import SelectField from '$lib/components/SelectField.svelte';
	import Spinner from '$lib/components/Spinner.svelte';
	import TextLink from '$lib/components/TextLink.svelte';
	import ToggleChip from '$lib/components/ToggleChip.svelte';
	import { tones } from '$lib/components/tones';
	import { errorText, every } from '$lib/monitor/poll';
	import { server } from '$lib/state/server.svelte';
	import ProfileCompare from './ProfileCompare.svelte';
	import {
		agoText,
		decodeChart,
		DEPTH_OPTIONS,
		depthLabel,
		estimateNote,
		FLASH_OPTIONS,
		historyRows,
		KV_OPTIONS,
		modelFile,
		modelLabel,
		prefillBars,
		prefillSubtitle,
		REPETITION_OPTIONS,
		resultRows,
		runOptionLabel,
		runPercent,
		setupProblem,
		toggled
	} from './view';

	const RUNNING_POLL_MS = 1000;
	const IDLE_POLL_MS = 5000;
	const ESTIMATE_DEBOUNCE_MS = 200;

	let models = $state.raw<LocalModelFile[] | null>(null);
	let modelsError = $state('');
	let builds = $state.raw<BuildManifest[]>([]);

	let file = $state('');
	let flash = $state<string[]>(['on', 'off']);
	let kv = $state<string[]>(['f16', 'q8_0']);
	let depths = $state<number[]>([...DEPTH_OPTIONS]);
	let repetitions = $state<number>(5);

	let estimate = $state.raw<BenchEstimate | null>(null);
	let estimateError = $state('');

	let current = $state.raw<BenchRunDetail | null>(null);
	let polledCurrent = $state(false);
	let syncedRun = 0;

	let runs = $state.raw<BenchRun[]>([]);
	let history = $state.raw<BenchBuildPoint[]>([]);
	let historyError = $state('');
	let picked = $state<number | null>(null);
	let detail = $state.raw<BenchRunDetail | null>(null);
	// Bumped when a run ends, so what was read about the model is read again.
	let finished = $state(0);

	let starting = $state(false);
	let actionError = $state('');
	let now = $state(Date.now());

	const running = $derived(current?.state === 'running');
	const activeBuild = $derived(server.status?.active_build ?? undefined);
	const noBuild = $derived(server.status !== null && !activeBuild);
	// Without the API, "no runs yet" is not something this page knows.
	const blind = $derived(server.unreachable || Boolean(modelsError));
	const selectedModel = $derived(models?.find((m) => modelFile(m) === file));

	const options = $derived(
		(models ?? []).map((m) => ({ value: modelFile(m), label: modelLabel(m) }))
	);
	const setup: BenchSetup = $derived({
		model_file: file,
		flash_attn: flash,
		cache_types: kv,
		depths,
		repetitions
	});

	const shownId = $derived(picked ?? runs[0]?.id ?? null);
	const shown = $derived(running ? current : detail?.id === shownId ? detail : null);
	const chart = $derived(
		shown
			? decodeChart(shown.series.decode, shown.state === 'running' ? shown.setup.depths : [])
			: null
	);
	const bars = $derived(shown ? prefillBars(shown.series.prefill) : []);
	const rows = $derived(resultRows(shown?.results ?? []));
	const tgName = $derived(`tg${shown?.setup.n_gen ?? 128}`);
	const ppName = $derived(`pp${shown?.setup.n_prompt ?? 512}`);

	const buildTag = (id: string) => builds.find((b) => b.id === id)?.tag ?? id;
	const runChoices = $derived(
		runs.map((r) => ({ value: String(r.id), label: runOptionLabel(r, buildTag(r.build), now) }))
	);
	const historyView = $derived(
		historyRows(
			history,
			builds,
			activeBuild?.id,
			server.status?.update_available ? server.status.update?.latest : undefined
		)
	);
	const runNote = $derived.by(() => {
		if (running) return '';
		const last = runs[0];
		return last ? `last run ${agoText(last.started_at, now)} on ${buildTag(last.build)}` : '';
	});
	const canRun = $derived(
		Boolean(file) && Boolean(estimate) && !estimateError && !running && !starting && !noBuild
	);

	$effect(() => {
		void loadModels();
		listBuilds()
			.then((r) => (builds = r.builds))
			.catch(() => {});
	});

	async function loadModels() {
		try {
			models = await listLocalModelFiles();
			modelsError = '';
		} catch (e) {
			modelsError = errorText(e);
		}
	}

	$effect(() => {
		const clock = setInterval(() => (now = Date.now()), 30_000);
		return () => clearInterval(clock);
	});

	function adopt(s: BenchSetup) {
		file = s.model_file;
		flash = [...(s.flash_attn ?? [])];
		kv = [...(s.cache_types ?? [])];
		depths = [...s.depths];
		repetitions = s.repetitions;
	}

	$effect(() => {
		return every(running ? RUNNING_POLL_MS : IDLE_POLL_MS, async (signal) => {
			const run = await getCurrentBenchRun(signal).catch(() => undefined);
			if (signal.aborted || run === undefined) return;
			const ended = current?.state === 'running' && run?.state !== 'running';
			current = run;
			polledCurrent = true;
			if (ended) {
				picked = null;
				finished++;
			}
			// A run started elsewhere, or before this page opened, takes over the setup.
			if (run?.state === 'running' && run.id !== syncedRun) {
				syncedRun = run.id;
				adopt(run.setup);
			}
		});
	});

	// The last run's model, if it is still here, or else the first.
	$effect(() => {
		if (file || !models || models.length === 0 || !polledCurrent) return;
		const last = current?.setup.model_file;
		file = last && models.some((m) => modelFile(m) === last) ? last : modelFile(models[0]);
	});

	$effect(() => {
		const model = file;
		void finished;
		if (!model) return;
		let stale = false;
		listBenchRuns(model)
			.then((list) => {
				if (!stale) runs = list;
			})
			.catch(() => {});
		getBenchHistory(model)
			.then((points) => {
				if (stale) return;
				history = points;
				historyError = '';
			})
			.catch((e) => {
				if (!stale) historyError = errorText(e);
			});
		return () => {
			stale = true;
		};
	});

	$effect(() => {
		const id = shownId;
		void finished;
		if (id === null) {
			detail = null;
			return;
		}
		let stale = false;
		getBenchRun(id)
			.then((run) => {
				if (!stale) detail = run;
			})
			.catch((e) => {
				if (!stale) actionError = errorText(e);
			});
		return () => {
			stale = true;
		};
	});

	$effect(() => {
		const body = setup;
		if (!body.model_file) {
			estimate = null;
			return;
		}
		const controller = new AbortController();
		const timer = setTimeout(async () => {
			try {
				estimate = await estimateBench($state.snapshot(body) as BenchSetup, controller.signal);
				estimateError = '';
			} catch (e) {
				if (controller.signal.aborted) return;
				estimate = null;
				estimateError = setupProblem(errorText(e));
			}
		}, ESTIMATE_DEBOUNCE_MS);
		return () => {
			clearTimeout(timer);
			controller.abort();
		};
	});

	function pickModel(value: string) {
		if (value === file) return;
		file = value;
		picked = null;
		runs = [];
		history = [];
		detail = null;
		actionError = '';
	}

	async function run() {
		starting = true;
		actionError = '';
		try {
			const started = await startBenchRun($state.snapshot(setup) as BenchSetup);
			syncedRun = started.id;
			picked = null;
			current = started;
		} catch (e) {
			actionError = setupProblem(errorText(e));
		} finally {
			starting = false;
		}
	}

	async function cancel() {
		actionError = '';
		try {
			await cancelBenchRun();
		} catch (e) {
			actionError = setupProblem(errorText(e));
		}
	}
</script>

<PageHeader
	title="Bench"
	subtitle="llama-bench runs to compare flags, quantizations and builds on your own hardware"
>
	{#snippet actions()}
		{#if runNote}<span class="note">{runNote}</span>{/if}
		<Button variant="primary" size="lg" disabled={!canRun} onclick={run}>
			{running ? 'Running…' : starting ? 'Starting…' : 'Run llama-bench'}
		</Button>
	{/snippet}
</PageHeader>

{#if actionError}
	<Panel tone="inset" padding="12px 16px" aria-label="Bench problem">
		<span class="problem" role="alert">{actionError}</span>
	</Panel>
{/if}

{#if running && current}
	<Panel padding="14px 20px" gap={8} aria-label="Benchmark progress">
		<div class="progress-head">
			<span class="current"><Spinner size={10} />{current.current}</span>
			<span class="progress-side">
				<span class="note"
					>{current.done} / {current.total} tests · models unloaded, requests get a 503</span
				>
				<Button size="xs" variant="danger" onclick={cancel}>Cancel</Button>
			</span>
		</div>
		<ProgressBar
			value={runPercent(current)}
			flow
			darkStripes
			label="{current.done} of {current.total} tests done"
		/>
	</Panel>
{/if}

<Columns>
	<Panel as="aside" flex="1 1 300px" gap={16} aria-labelledby="setup-heading">
		<SectionHeading id="setup-heading" title="RUN SETUP" />
		{#if modelsError}
			<EmptyState title="Could not list the models" detail={modelsError}>
				{#snippet actions()}
					<Button size="sm" onclick={loadModels}>Try again</Button>
				{/snippet}
			</EmptyState>
		{:else if models && models.length === 0}
			<EmptyState title="No models yet" detail="A benchmark needs a GGUF on this machine.">
				{#snippet actions()}
					<TextLink href={resolve('/library')}>Library →</TextLink>
				{/snippet}
			</EmptyState>
		{:else}
			<SelectField
				label="Model"
				{options}
				value={file}
				size={40}
				disabled={running || !models}
				onchange={pickModel}
			/>
		{/if}

		<div class="group" role="group" aria-labelledby="fa-label">
			<span id="fa-label" class="group-label">Flash attention <span class="flag">-fa</span></span>
			<div class="chips">
				{#each FLASH_OPTIONS as o (o)}
					<ToggleChip
						pressed={flash.includes(o)}
						onclick={() => (flash = toggled(flash, o, FLASH_OPTIONS))}>{o}</ToggleChip
					>
				{/each}
			</div>
		</div>
		<div class="group" role="group" aria-labelledby="kv-label">
			<span id="kv-label" class="group-label"
				>KV cache type <span class="flag">-ctk / -ctv</span></span
			>
			<div class="chips">
				{#each KV_OPTIONS as o (o)}
					<ToggleChip pressed={kv.includes(o)} onclick={() => (kv = toggled(kv, o, KV_OPTIONS))}
						>{o}</ToggleChip
					>
				{/each}
			</div>
		</div>
		<div class="group" role="group" aria-labelledby="depth-label">
			<span id="depth-label" class="group-label">Context depth <span class="flag">-d</span></span>
			<div class="chips">
				{#each DEPTH_OPTIONS as o (o)}
					<ToggleChip
						pressed={depths.includes(o)}
						onclick={() => (depths = toggled(depths, o, DEPTH_OPTIONS))}>{depthLabel(o)}</ToggleChip
					>
				{/each}
			</div>
		</div>
		<div class="group" role="group" aria-labelledby="rep-label">
			<span id="rep-label" class="group-label">Repetitions <span class="flag">-r</span></span>
			<div class="chips">
				{#each REPETITION_OPTIONS as o (o)}
					<ToggleChip pressed={repetitions === o} onclick={() => (repetitions = o)}>{o}</ToggleChip>
				{/each}
			</div>
		</div>

		{#if estimate}
			<CommandBlock label="COMMAND" text={estimate.command} />
			<span class="matrix">
				{estimateNote(estimate.tests, repetitions, estimate.seconds)}
				Models are unloaded and requests get a 503 while it runs, then models reload.
			</span>
		{/if}
		{#if estimateError}<span class="problem" role="alert">{estimateError}</span>{/if}
		{#if noBuild}
			<span class="matrix">
				A benchmark uses the active build's llama-bench, and no build is active.
				<TextLink href={resolve('/builds')}>Builds →</TextLink>
			</span>
		{/if}
	</Panel>

	<div class="results">
		{#if shown?.state === 'failed'}
			<Panel tone="inset" padding="12px 16px" aria-label="Failed run">
				<span class="problem" role="alert">
					Run #{shown.id} failed{shown.error ? `: ${setupProblem(shown.error)}` : '.'}
				</span>
			</Panel>
		{:else if shown?.state === 'canceled'}
			<Panel tone="inset" padding="12px 16px" aria-label="Canceled run">
				<span class="note"
					>Run #{shown.id} was canceled after {shown.done} of {shown.total} tests.</span
				>
			</Panel>
		{/if}

		{#if !shown}
			<Panel>
				{#if blind}
					<EmptyState title="Cannot read the runs" detail="They come back with the connection." />
				{:else if shownId !== null}
					<EmptyState title="Loading the run" />
				{:else}
					<EmptyState
						title="No runs{selectedModel ? ` of ${selectedModel.name}` : ''} yet"
						detail="Pick what to compare and run llama-bench. Charts fill in as each test finishes, and every run stays here to compare across builds."
					/>
				{/if}
			</Panel>
		{:else}
			<Band>
				<Panel flex="3 1 460px" aria-labelledby="tg-heading">
					<SectionHeading
						id="tg-heading"
						title="DECODE SPEED vs CONTEXT DEPTH · {tgName}"
						subtitle="How fast one stream generates as the conversation gets longer"
					/>
					{#if chart && chart.series.length === 0}
						<EmptyState
							title={shown.state === 'running'
								? 'Waiting for the first measurement'
								: 'No decode measurements in this run'}
						/>
					{:else if chart && chart.depths.length < 2}
						<EmptyState
							title="One depth measured"
							detail="Add context depths to see how decoding holds up as the context grows."
						/>
						<Legend items={chart.legend} gap={16} />
					{:else if chart}
						<LineChart
							series={chart.series}
							label="Line chart of decode tokens per second at increasing context depth for {chart
								.series.length} configurations"
							max={chart.max}
							height={220}
							yLabels={chart.yLabels}
							xLabels={chart.xLabels}
							yWidth={30}
							gridTop={false}
						/>
						<Legend items={chart.legend} gap={16} />
					{/if}
				</Panel>
				<Panel flex="2 1 340px" aria-labelledby="pp-heading">
					<SectionHeading
						id="pp-heading"
						title="PROMPT PROCESSING · {ppName}"
						subtitle={prefillSubtitle(shown.series.prefill)}
					/>
					{#if bars.length === 0}
						<EmptyState title="No prompt measurements yet" />
					{/if}
					{#each bars as row (row.label)}
						<div class="pp">
							<span class="pp-label">{row.label}</span>
							{#each row.bars as bar (bar.note)}
								<div class="pp-bar">
									<div
										class="pp-fill"
										class:faded={bar.faded}
										style:width="{bar.width}%"
										style:background={tones.prefill}
									></div>
									<span class="pp-value" class:faded={bar.faded}>{bar.value} {bar.note}</span>
								</div>
							{/each}
						</div>
					{/each}
					{#if shown.takeaway}
						<div class="takeaway">
							<span class="takeaway-lead">Takeaway:</span>
							{shown.takeaway}
						</div>
					{/if}
				</Panel>
			</Band>

			<Panel gap={12} aria-labelledby="results-heading">
				<SectionHeading id="results-heading" title="RESULTS · llama-bench -o jsonl">
					{#snippet actions()}
						<div class="result-tools">
							{#if runChoices.length > 1 && !running}
								<SelectField
									label="Run"
									inline
									mono
									size={36}
									options={runChoices}
									value={String(shownId)}
									onchange={(v) => (picked = Number(v))}
								/>
							{/if}
							<CopyButton text={shown.markdown} label="Copy as Markdown" size="sm" tone="muted" />
						</div>
					{/snippet}
				</SectionHeading>
				{#if rows.length === 0}
					<EmptyState
						title={shown.state === 'running' ? 'Results appear as tests finish' : 'No results'}
					/>
				{:else}
					<ScrollX minWidth={760}>
						<table>
							<thead>
								<tr>
									<th scope="col">BACKEND</th>
									<th scope="col">NGL</th>
									<th scope="col">FA</th>
									<th scope="col">TYPE_K / V</th>
									<th scope="col">TEST</th>
									<th scope="col" class="num">T/S</th>
									<th scope="col" class="rel">RELATIVE</th>
								</tr>
							</thead>
							<tbody>
								{#each rows as r (r.key)}
									<tr>
										<td>{r.backend}</td>
										<td>{r.ngl}</td>
										<td>{r.flash}</td>
										<td>{r.kv}</td>
										<td>{r.test}</td>
										<td class="num">{r.speed}</td>
										<td><ProgressBar value={r.relative} color={r.color} height={6} /></td>
									</tr>
								{/each}
							</tbody>
						</table>
					</ScrollX>
				{/if}
			</Panel>
		{/if}

		<Panel aria-labelledby="history-heading">
			<SectionHeading
				id="history-heading"
				title="ACROSS BUILDS · tg128 @ d0{selectedModel ? ` · ${modelLabel(selectedModel)}` : ''}"
				subtitle="Same benchmark after every update, so regressions show up before you notice them in chat"
			/>
			{#if blind || historyError}
				<EmptyState title="Could not load the history" detail={historyError || undefined} />
			{:else if historyView.length === 0}
				<EmptyState
					title="Nothing to compare yet"
					detail="Finished runs with tg128 at an empty context appear here, one row per build."
				/>
			{:else}
				<div class="history">
					{#each historyView as h (h.key)}
						<div class="history-row">
							<span class="history-build">
								<span class="history-tag">{h.tag}</span>
								<span class="history-meta">{h.meta}</span>
							</span>
							<ProgressBar value={h.width} color={h.color} height={18} radius={3} grow />
							<span class="history-value">
								{h.value}
								{#if h.canRun}
									<Button size="xs" variant="ghost" tone="accent" disabled={!canRun} onclick={run}>
										Run now
									</Button>
								{:else}
									<span style:color={h.deltaColor}>{h.delta}</span>
								{/if}
							</span>
						</div>
					{/each}
				</div>
			{/if}
		</Panel>
	</div>
</Columns>

<ProfileCompare />

<style>
	.note {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}

	.problem {
		font-size: var(--fs-13);
		color: var(--color-error-soft);
		overflow-wrap: anywhere;
	}

	.progress-head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-8);
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}

	.current {
		display: flex;
		align-items: center;
		gap: var(--space-8);
		min-width: 0;
		overflow-wrap: anywhere;
	}

	.progress-side {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-10);
	}

	.group {
		display: flex;
		flex-direction: column;
		gap: var(--space-8);
	}

	.group-label {
		font-size: var(--fs-12);
		color: var(--fg-tertiary);
	}

	.flag {
		font-family: var(--font-mono);
		color: var(--fg-faint);
	}

	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-6);
	}

	.matrix {
		font-size: var(--fs-12);
		line-height: 1.5;
		color: var(--fg-muted);
	}

	.results {
		flex: 3 1 700px;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-16);
	}

	.pp {
		display: flex;
		flex-direction: column;
		gap: var(--space-6);
	}

	.pp-label {
		font-size: var(--fs-12);
		color: var(--fg-secondary);
	}

	.pp-bar {
		display: flex;
		align-items: center;
		gap: var(--space-8);
	}

	.pp-fill {
		height: 12px;
		border-radius: var(--radius-1);
		transform-origin: left;
	}

	.pp-fill.faded {
		opacity: 0.45;
	}

	.pp-value {
		flex: none;
		font-family: var(--font-mono);
		font-size: var(--fs-11);
	}

	.pp-value.faded {
		color: var(--fg-muted);
	}

	.takeaway {
		margin-top: auto;
		padding: var(--space-12) var(--space-14);
		background: var(--bg-inset);
		border: 1px solid var(--border-panel);
		border-radius: var(--radius-5);
		font-size: var(--fs-13);
		line-height: 1.5;
		color: var(--fg-secondary);
	}

	.takeaway-lead {
		color: var(--color-decode);
		font-weight: 500;
	}

	.result-tools {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-8);
	}

	table {
		width: 100%;
		border-collapse: collapse;
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}

	th {
		text-align: left;
		font-weight: 500;
		font-size: var(--fs-10);
		letter-spacing: var(--tracking-column);
		color: var(--fg-faint);
		padding: var(--space-6) var(--space-10);
		border-bottom: 1px solid var(--border-panel);
	}

	td {
		padding: var(--space-8) var(--space-10);
		border-bottom: 1px solid var(--border-row);
	}

	.num {
		text-align: right;
		white-space: nowrap;
	}

	.rel {
		width: 30%;
	}

	.history {
		display: flex;
		flex-direction: column;
		gap: var(--space-10);
	}

	.history-row {
		display: grid;
		grid-template-columns: 150px minmax(0, 1fr) 150px;
		align-items: center;
		gap: var(--space-14);
	}

	.history-build {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		min-width: 0;
	}

	.history-tag {
		font-family: var(--font-mono);
		font-size: var(--fs-13);
		overflow-wrap: anywhere;
	}

	.history-meta {
		font-size: var(--fs-11);
		color: var(--fg-muted);
	}

	.history-value {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		gap: var(--space-6);
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		text-align: right;
	}

	@media (max-width: 560px) {
		.history-row {
			grid-template-columns: minmax(0, 1fr) auto;
		}

		.history-row > :nth-child(2) {
			grid-column: 1 / -1;
			grid-row: 2;
		}
	}
</style>
