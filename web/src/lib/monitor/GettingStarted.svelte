<script lang="ts">
	import { resolve } from '$app/paths';
	import {
		getHardware,
		listBuildJobs,
		listLocalModelFiles,
		type Hardware,
		type InstallJob
	} from '$lib/api/client';
	import Band from '$lib/components/Band.svelte';
	import Button from '$lib/components/Button.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import ProgressBar from '$lib/components/ProgressBar.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import Spinner from '$lib/components/Spinner.svelte';
	import StateLabel from '$lib/components/StateLabel.svelte';
	import TextLink from '$lib/components/TextLink.svelte';
	import { tones } from '$lib/components/tones';
	import { formatBytes } from '$lib/format';
	import { errorText, every } from '$lib/monitor/poll';
	import { server } from '$lib/state/server.svelte';
	import { currentJob, hardwareLines, jobEnded, jobPercent } from './setup';

	interface Props {
		/** One line for the other pages: what is missing, and where to see more. */
		compact?: boolean;
	}

	let { compact = false }: Props = $props();

	const JOBS_POLL_MS = 1500;
	const MODELS_POLL_MS = 5000;

	let hardware = $state.raw<Hardware | null>(null);
	let hardwareError = $state('');
	let jobs = $state.raw<InstallJob[]>([]);
	let modelCount = $state<number | null>(null);

	const known = $derived(server.status !== null);
	const build = $derived(server.status?.active_build ?? null);
	const job = $derived(currentJob(jobs));
	const installing = $derived(job !== null && !jobEnded(job));
	const failed = $derived(job?.state === 'failed');

	const needsBuild = $derived(known && !build);
	const needsModels = $derived(modelCount === 0);
	const visible = $derived(compact ? needsBuild : needsBuild || needsModels);

	$effect(() => {
		if (!needsBuild) return;
		let wasInstalling = false;
		return every(JOBS_POLL_MS, async (signal) => {
			const list = await listBuildJobs().catch(() => null);
			if (!list || signal.aborted) return;
			jobs = list;
			const under = list.some((j) => !jobEnded(j));
			// The router starts as an install ends, so its status is worth asking at once.
			if (wasInstalling && !under) void server.poll();
			wasInstalling = under;
		});
	});

	$effect(() => {
		if (compact || !known || (build && modelCount !== null && modelCount > 0)) return;
		return every(MODELS_POLL_MS, async (signal) => {
			const list = await listLocalModelFiles().catch(() => null);
			if (list && !signal.aborted) modelCount = list.length;
		});
	});

	$effect(() => {
		if (compact || !visible || hardware) return;
		getHardware()
			.then((h) => (hardware = h))
			.catch((e) => (hardwareError = errorText(e)));
	});
</script>

{#if compact && visible}
	<Panel tone="inset" padding="10px 16px" gap={8} row aria-label="Getting started">
		<span class="slim">
			{#if installing && job}
				<Spinner size={10} /> Installing llama.cpp {job.version}, {job.step || job.state}
				{#if job.total > 0}· {Math.round(jobPercent(job))}%{/if}
			{:else if failed}
				<span class="failed">Installing llama.cpp failed.</span> No build is active.
			{:else}
				No llama.cpp build is active yet.
			{/if}
		</span>
		<TextLink href={resolve('/')}>Getting started →</TextLink>
	</Panel>
{:else if visible}
	<Panel gap={16} aria-labelledby="setup-heading">
		<SectionHeading
			id="setup-heading"
			title="GETTING STARTED"
			subtitle="Three things stand between this machine and its first request"
		/>
		<Band layout="grid" min={260} radius={8}>
			<Panel tone="inset" gap={10} aria-labelledby="setup-hardware">
				<h3 id="setup-hardware" class="step">1 · HARDWARE</h3>
				{#if hardware}
					<StateLabel color={tones.ok}>Detected</StateLabel>
					<ul class="facts">
						{#each hardwareLines(hardware) as line (line)}
							<li>{line}</li>
						{/each}
					</ul>
					{#each hardware.warnings ?? [] as warning (warning)}
						<span class="warning">{warning}</span>
					{/each}
				{:else if hardwareError}
					<span class="failed" role="alert">{hardwareError}</span>
				{:else}
					<span class="slim"><Spinner size={10} /> Detecting</span>
				{/if}
			</Panel>

			<Panel tone="inset" gap={10} aria-labelledby="setup-build">
				<h3 id="setup-build" class="step">2 · LLAMA.CPP BUILD</h3>
				{#if build}
					<StateLabel color={tones.ok}>{build.tag} is active</StateLabel>
					<span class="text">The router runs on it.</span>
				{:else if installing && job}
					<StateLabel color={tones.decode}>Installing {job.version}</StateLabel>
					<span class="text">{job.step || job.state} · {job.backend}</span>
					<ProgressBar
						value={jobPercent(job)}
						flow
						darkStripes
						label="Installing llama.cpp {job.version}"
					/>
					{#if job.bytes_per_sec > 0}
						<span class="figure">{formatBytes(job.bytes_per_sec)}/s</span>
					{/if}
				{:else if failed && job}
					<StateLabel color={tones.error}>The install failed</StateLabel>
					<span class="failed" role="alert">{job.error || `Installing ${job.version} failed.`}</span
					>
					<div class="cta">
						<Button size="sm" href={resolve('/builds')}>Try from Builds</Button>
					</div>
				{:else}
					<StateLabel color={tones.off}>No build</StateLabel>
					<span class="text">
						Nothing is installing. With LLAMA_GUI_LLAMA_VERSION=none, picking one is up to you.
					</span>
					<div class="cta">
						<Button size="sm" variant="primary" href={resolve('/builds')}
							>Install from Builds</Button
						>
					</div>
				{/if}
			</Panel>

			<Panel tone="inset" gap={10} aria-labelledby="setup-models">
				<h3 id="setup-models" class="step">3 · MODELS</h3>
				{#if modelCount === null}
					<span class="slim"><Spinner size={10} /> Looking</span>
				{:else if modelCount === 0}
					<StateLabel color={tones.off}>No models yet</StateLabel>
					<span class="text"
						>Find one that fits this machine, or put a GGUF in the models directory.</span
					>
					<div class="cta">
						<Button size="sm" variant={build ? 'primary' : 'default'} href={resolve('/library')}>
							Browse the Library
						</Button>
					</div>
				{:else}
					<StateLabel color={tones.ok}>{modelCount} on disk</StateLabel>
					<span class="text">A profile says how each one is launched.</span>
					<TextLink href={resolve('/profiles')}>Profiles →</TextLink>
				{/if}
			</Panel>
		</Band>
	</Panel>
{/if}

<style>
	.slim {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-6);
		font-size: var(--fs-13);
		color: var(--fg-secondary);
	}

	.cta {
		display: flex;
		justify-content: flex-start;
	}

	.step {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		font-weight: 500;
		letter-spacing: var(--tracking-caps);
		color: var(--fg-muted);
	}

	.facts {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		margin: 0;
		padding: 0;
		list-style: none;
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		color: var(--fg-secondary);
		overflow-wrap: anywhere;
	}

	.text {
		font-size: var(--fs-13);
		line-height: 1.5;
		color: var(--fg-secondary);
	}

	.figure {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
	}

	.warning {
		font-size: var(--fs-12);
		color: var(--color-warn);
	}

	.failed {
		font-size: var(--fs-13);
		color: var(--color-error-soft);
		overflow-wrap: anywhere;
	}
</style>
