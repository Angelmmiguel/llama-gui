<script lang="ts">
	import {
		activateBuild,
		cancelBuildJob,
		checkForUpdates,
		compareBuilds,
		deleteBuild,
		getAvailableBuild,
		getHardware,
		installBuild,
		listAvailableBuilds,
		listBuildJobs,
		listBuilds,
		type AvailableRelease,
		type BuildManifest,
		type Hardware,
		type InstallJob,
		type UpdateInfo
	} from '$lib/api/client';
	import Band from '$lib/components/Band.svelte';
	import Button from '$lib/components/Button.svelte';
	import Eyebrow from '$lib/components/Eyebrow.svelte';
	import Metric from '$lib/components/Metric.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import ProgressBar from '$lib/components/ProgressBar.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import Segmented from '$lib/components/Segmented.svelte';
	import SelectCard from '$lib/components/SelectCard.svelte';
	import SelectField from '$lib/components/SelectField.svelte';
	import Spinner from '$lib/components/Spinner.svelte';
	import TextField from '$lib/components/TextField.svelte';
	import { tones } from '$lib/components/tones';
	import { formatBytes } from '$lib/format';
	import { backendLabel } from '$lib/monitor/overview';
	import { errorText, every } from '$lib/monitor/poll';
	import { server } from '$lib/state/server.svelte';
	import InstalledBuilds from './InstalledBuilds.svelte';
	import Timeline from './Timeline.svelte';
	import UpdateSteps from './UpdateSteps.svelte';
	import {
		affectKeywords,
		ago,
		binaryTag,
		buildTimeline,
		checkLabel,
		compareTarget,
		defaultBackend,
		hardwareLine,
		installLabel,
		isRateLimited,
		isRunning,
		jobLine,
		jobPercent,
		noBuildState,
		normalizeTag,
		updateSteps,
		upstreamProblem,
		type Channel,
		type UpstreamProblem
	} from './view';

	const NIGHTLY_LIMIT = 25;
	const STABLE_LIMIT = 10;
	const TIMELINE_DOTS = 24;
	const JOBS_MS = 1000;
	const SWITCH_LIMIT_MS = 90_000;

	let builds = $state.raw<BuildManifest[]>([]);
	let activeId = $state('');
	let hardware = $state.raw<Hardware | null>(null);
	let jobs = $state.raw<InstallJob[]>([]);
	let stables = $state.raw<AvailableRelease[]>([]);
	let nightlies = $state.raw<AvailableRelease[]>([]);
	let problem = $state.raw<UpstreamProblem | null>(null);
	let releasesLoaded = $state(false);
	let checking = $state(false);
	let checkInfo = $state.raw<UpdateInfo | undefined>(undefined);
	let now = $state(Date.now());

	let channel = $state<Channel>('stable');
	let channelChosen = $state(false);
	let picked = $state('');
	let details = $state.raw<Record<string, AvailableRelease>>({});
	let customTag = $state('');
	// Choosing a release by its tag instead of from a channel's list.
	let otherTag = $state(false);
	let customError = $state('');
	let customBusy = $state(false);
	let backendChosen = $state('');
	let aheadBy = $state<number | null>(null);
	let runningRelease = $state.raw<AvailableRelease | null>(null);

	let targetError = $state('');
	let installError = $state('');
	let switching = $state('');
	let swapState = $state<'idle' | 'switching' | 'done' | 'failed'>('idle');
	let swapMeta = $state<string | undefined>(undefined);
	let buildErrors = $state.raw<Record<string, string>>({});

	const active = $derived(builds.find((b) => b.id === activeId));
	const info = $derived(checkInfo ?? server.status?.update);
	const routerState = $derived(server.state);
	const list = $derived(channel === 'stable' ? stables : nightlies);
	const target = $derived.by<AvailableRelease | null>(() => {
		const tag = picked || list[0]?.tag;
		if (!tag) return null;
		return details[tag] ?? list.find((r) => r.tag === tag) ?? null;
	});
	const targetBinary = $derived(target ? binaryTag(target) : null);
	const options = $derived(target?.options ?? []);
	const backend = $derived(
		options.some((o) => o.backend === backendChosen && !o.reason)
			? backendChosen
			: defaultBackend(options, active?.backend)
	);
	const option = $derived(options.find((o) => o.backend === backend));
	const candidateId = $derived(targetBinary && backend ? `${targetBinary}-${backend}` : '');
	const candidate = $derived(builds.find((b) => b.id === candidateId));
	const standing = $derived(compareTarget(active?.tag, target));
	const upToDate = $derived(!!active && candidateId === active.id);
	const runningJob = $derived(jobs.find((j) => isRunning(j)) ?? null);
	const job = $derived(runningJob ?? jobs.findLast((j) => j.id === candidateId) ?? null);
	const failedJob = $derived(job?.state === 'failed' ? job : null);
	const busy = $derived(!!runningJob || swapState === 'switching');
	const steps = $derived(updateSteps(job, { state: swapState, meta: swapMeta }));

	const noBuild = $derived(noBuildState(builds.length));
	const heroLabel = $derived.by(() => {
		if (swapState === 'switching') return 'SWITCHING…';
		if (runningJob) return active ? 'UPDATING…' : 'INSTALLING…';
		if (!active) return noBuild.heading;
		if (!target) return 'RUNNING';
		if (upToDate) return 'UP TO DATE';
		switch (standing) {
			case 'newer':
				return 'UPDATE AVAILABLE';
			case 'older':
				return 'OLDER VERSION SELECTED';
			case 'same':
				return 'OTHER BACKEND SELECTED';
		}
		return 'VERSION SELECTED';
	});
	const targetHeading = $derived.by(() => {
		if (picked && picked !== list[0]?.tag) return 'Selected version';
		return channel === 'stable' ? 'Latest stable' : 'Latest nightly';
	});
	const runningMeta = $derived.by(() => {
		if (!active) return '';
		return [
			active.probe.commit ? active.probe.commit.slice(0, 7) : '',
			runningRelease
				? ago(runningRelease.published_at, now)
				: `installed ${ago(active.installed_at, now)}`,
			`${backendLabel(active.backend)}${active.variant ? ` ${active.variant}` : ''}`,
			routerState === 'running' ? '' : `router ${routerState}`
		]
			.filter(Boolean)
			.join(' · ');
	});
	const targetMeta = $derived.by(() => {
		if (!target) return '';
		return [
			target.tag !== targetBinary && targetBinary ? targetBinary : '',
			ago(target.published_at, now),
			standing === 'newer' && aheadBy !== null ? `${aheadBy} commits ahead` : ''
		]
			.filter(Boolean)
			.join(' · ');
	});
	const hint = $derived.by(() => {
		if (upToDate) return 'You are running this version with this backend.';
		if (candidate) return 'Already installed: switching restarts the router on it.';
		const size = option ? `${formatBytes(option.size)} download. ` : '';
		if (!active) return `${size}Switch to it once installed and the router can start.`;
		return `${size}The server keeps running until the swap; loaded models reload after it.`;
	});
	const keywords = $derived(affectKeywords(active?.backend));
	const timeline = $derived(
		active && target && standing === 'newer'
			? buildTimeline(
					nightlies,
					stables,
					active.tag,
					target,
					runningRelease ? Date.parse(runningRelease.published_at) : null,
					keywords,
					TIMELINE_DOTS
				)
			: null
	);
	const channelOptions = $derived(
		(() => {
			const rows = list.map((r) => ({ value: r.tag, label: releaseLabel(r) }));
			const tag = target?.tag;
			if (tag && !rows.some((r) => r.value === tag)) {
				rows.unshift({ value: tag, label: target ? releaseLabel(target) : tag });
			}
			return rows;
		})()
	);

	function releaseLabel(r: AvailableRelease) {
		const installed = builds.some((b) => b.release_tag === r.tag);
		return `${r.tag} · ${ago(r.published_at, now)}${installed ? ' · installed' : ''}`;
	}

	async function refreshBuilds() {
		const out = await listBuilds();
		builds = out.builds;
		activeId = out.active;
	}

	async function loadReleases() {
		try {
			const [s, n] = await Promise.all([
				listAvailableBuilds('stable', STABLE_LIMIT),
				listAvailableBuilds('nightly', NIGHTLY_LIMIT)
			]);
			stables = s;
			nightlies = n;
			problem = null;
		} catch (e) {
			problem = upstreamProblem(e);
		} finally {
			releasesLoaded = true;
		}
	}

	$effect(() => {
		void refreshBuilds().catch(() => {});
		void getHardware()
			.then((h) => (hardware = h))
			.catch(() => {});
		void loadReleases();
	});

	$effect(() => every(JOBS_MS, pollJobs));

	$effect(() => {
		const timer = setInterval(() => (now = Date.now()), 30_000);
		return () => clearInterval(timer);
	});

	// The first answer picks the channel of what runs, unless the user already chose.
	$effect(() => {
		if (!channelChosen && (active?.channel === 'nightly' || active?.channel === 'stable')) {
			channel = active.channel;
		}
	});

	// A release in the list has no backends unless it is its channel's newest.
	$effect(() => {
		const r = target;
		targetError = '';
		if (!r || r.options || details[r.tag]) return;
		let stale = false;
		getAvailableBuild(r.tag)
			.then((full) => {
				if (!stale) details = { ...details, [r.tag]: full };
			})
			.catch((e) => {
				if (stale) return;
				if (isRateLimited(e)) problem = upstreamProblem(e);
				else targetError = errorText(e);
			});
		return () => {
			stale = true;
		};
	});

	$effect(() => {
		const from = active?.tag;
		const to = targetBinary;
		aheadBy = null;
		if (!from || !to || standing !== 'newer') return;
		let stale = false;
		compareBuilds(from, to)
			.then((c) => {
				if (!stale) aheadBy = c.ahead_by;
			})
			.catch((e) => {
				// Anything else only costs the commit count.
				if (!stale && isRateLimited(e)) problem = upstreamProblem(e);
			});
		return () => {
			stale = true;
		};
	});

	$effect(() => {
		const tag = active?.release_tag;
		if (!tag || !releasesLoaded || problem) return;
		const known = [...nightlies, ...stables].find((r) => r.tag === tag);
		if (known) {
			runningRelease = known;
			return;
		}
		if (runningRelease?.tag === tag) return;
		let stale = false;
		getAvailableBuild(tag)
			.then((r) => {
				if (!stale) runningRelease = r;
			})
			.catch(() => {
				// Only costs the age; the installed time stands in for it.
			});
		return () => {
			stale = true;
		};
	});

	let wasRunning = false;
	async function pollJobs(signal: AbortSignal) {
		const out = await listBuildJobs().catch(() => null);
		if (!out || signal.aborted) return;
		jobs = out;
		const running = out.some((j) => isRunning(j));
		if (wasRunning && !running) await refreshBuilds().catch(() => {});
		wasRunning = running;
	}

	async function check() {
		checking = true;
		try {
			checkInfo = await checkForUpdates();
			await loadReleases();
			await server.poll();
			now = Date.now();
		} catch (e) {
			problem = upstreamProblem(e);
		} finally {
			checking = false;
		}
	}

	function pickVersionSource(next: string) {
		channelChosen = true;
		customError = '';
		otherTag = next === 'other';
		if (otherTag) return;
		channel = next as Channel;
		picked = '';
		customTag = '';
	}

	async function useCustomTag() {
		customError = '';
		const tag = normalizeTag(customTag);
		if (!tag) {
			customError = 'A tag looks like b8105 or v0.9.0.';
			return;
		}
		customBusy = true;
		try {
			const r = await getAvailableBuild(tag);
			details = { ...details, [r.tag]: r };
			channelChosen = true;
			if (r.channel === 'stable' || r.channel === 'nightly') channel = r.channel;
			picked = r.tag;
		} catch (e) {
			const issue = upstreamProblem(e);
			customError = issue.kind === 'rate-limited' ? issue.message : errorText(e);
		} finally {
			customBusy = false;
		}
	}

	async function install() {
		if (!target) return;
		installError = '';
		swapState = 'idle';
		swapMeta = undefined;
		try {
			await installBuild({ version: target.tag, backend: backend || 'auto' });
			jobs = await listBuildJobs();
			wasRunning = jobs.some((j) => isRunning(j));
			await refreshBuilds();
		} catch (e) {
			installError = errorText(e);
		}
	}

	async function cancel() {
		if (!runningJob) return;
		await cancelBuildJob(runningJob.id).catch((e) => (installError = errorText(e)));
	}

	async function switchTo(id: string) {
		switching = id;
		swapState = 'switching';
		swapMeta = 'restarting the router';
		buildErrors = {};
		try {
			await activateBuild(id);
			const deadline = Date.now() + SWITCH_LIMIT_MS;
			for (;;) {
				await server.poll();
				const st = server.status;
				if (st?.active_build?.id === id && st.state === 'running') break;
				if (st?.state === 'crashed') throw new Error(st.last_error || 'The router crashed on it');
				if (Date.now() > deadline) throw new Error('The router did not come back in time');
				await new Promise((r) => setTimeout(r, 1000));
			}
			swapState = 'done';
			swapMeta = `running ${id}`;
		} catch (e) {
			swapState = 'failed';
			swapMeta = 'see the build';
			buildErrors = { [id]: errorText(e) };
		} finally {
			switching = '';
			await refreshBuilds().catch(() => {});
		}
	}

	async function remove(id: string) {
		buildErrors = {};
		try {
			await deleteBuild(id);
			await refreshBuilds();
		} catch (e) {
			buildErrors = { [id]: errorText(e) };
		}
	}
</script>

<PageHeader
	title="Builds"
	subtitle="Which llama.cpp you run, which prebuilt it came from, and what changed since"
>
	{#snippet actions()}
		<Button disabled={checking} onclick={check}>
			{checking ? 'Checking…' : checkLabel(info, now)}
		</Button>
	{/snippet}
</PageHeader>

{#if problem}
	<Panel tone="inset" padding="12px 16px" aria-label="Release information">
		<span class="warn" role="status">{problem.message}</span>
	</Panel>
{/if}

<Band as="section" aria-labelledby="up-h">
	<Panel flex="1 1 520px" padding="24px" gap={18}>
		<SectionHeading id="up-h" title={heroLabel} />
		<div class="versions">
			<div class="version">
				<span class="label">{active ? 'Running' : 'No build'}</span>
				<Metric value={active?.tag ?? '—'} size={40} />
				<span class="meta">{runningMeta || noBuild.meta}</span>
			</div>
			{#if target}
				<svg
					aria-hidden="true"
					width="40"
					height="20"
					viewBox="0 0 40 20"
					fill="none"
					stroke="var(--fg-gutter)"
					stroke-width="2"
					stroke-linecap="round"
					stroke-linejoin="round"><path d="M2 10h34M28 3l8 7-8 7"></path></svg
				>
				<div class="version">
					<span class="label">{targetHeading}</span>
					<Metric value={target.tag} size={40} color={tones.decode} />
					<span class="meta">{targetMeta}</span>
				</div>
			{/if}
		</div>

		{#if runningJob}
			<div class="progress">
				<ProgressBar
					value={jobPercent(runningJob)}
					height={10}
					flow
					darkStripes
					label="Install progress"
				/>
				<div class="progress-line">
					<span>{jobLine(runningJob)}</span>
					<span>{jobPercent(runningJob)}%</span>
				</div>
				<div>
					<Button size="sm" variant="ghost" onclick={cancel}>Cancel</Button>
				</div>
			</div>
		{:else if target}
			<div class="buttons">
				{#if upToDate}
					<!-- Nothing to do: the steps and the heading say so. -->
				{:else if candidate}
					<Button
						variant="primary"
						size="xl"
						disabled={busy || candidate.supported === false}
						onclick={() => switchTo(candidate.id)}
					>
						{swapState === 'switching' ? 'Switching…' : `Switch to ${candidate.tag}`}
					</Button>
				{:else}
					<Button variant="primary" size="xl" disabled={busy || !option} onclick={install}>
						{installLabel(target.tag, option)}
					</Button>
				{/if}
			</div>
			<span class="note">{hint}</span>
		{/if}
		{#if candidate?.supported === false}
			<span class="error" role="alert">
				{candidate.tag} has no router mode, so llama-gui cannot run it. Pick a newer version.
			</span>
		{/if}
		{#if targetError}<span class="error" role="alert">{target?.tag}: {targetError}</span>{/if}
		{#if installError}<span class="error" role="alert">{installError}</span>{/if}
		{#if failedJob?.error}<span class="error" role="alert">{failedJob.error}</span>{/if}

		{#if releasesLoaded && !problem}
			<div class="selector">
				<Eyebrow>VERSION</Eyebrow>
				<div class="selector-row">
					<Segmented
						label="Channel"
						surface="ground"
						options={[{ value: 'stable' }, { value: 'nightly' }, { value: 'other' }]}
						value={otherTag ? 'other' : channel}
						onchange={pickVersionSource}
					/>
					{#if otherTag}
						<form
							class="custom"
							onsubmit={(e) => {
								e.preventDefault();
								void useCustomTag();
							}}
						>
							<TextField
								label="Tag"
								placeholder="b8105 or v0.9.0"
								class="mono"
								bind:value={customTag}
							/>
							<Button size="ml" type="submit" disabled={customBusy || !customTag.trim()}>
								{customBusy ? 'Looking…' : 'Use'}
							</Button>
						</form>
					{:else}
						<div class="select">
							<SelectField
								label="Release"
								mono
								size={38}
								options={channelOptions}
								value={target?.tag ?? ''}
								onchange={(v) => (picked = v)}
							/>
						</div>
					{/if}
				</div>
				{#if customError}<span class="error" role="alert">{customError}</span>{/if}
			</div>
		{:else if !releasesLoaded}
			<div class="selector" aria-busy="true">
				<div class="selector-head">
					<Eyebrow>VERSION</Eyebrow>
					<span class="reading" role="status"
						><Spinner size={10} />Reading releases from GitHub…</span
					>
				</div>
				<div class="selector-row">
					<span class="shimmer" style:height="38px" style:width="200px"></span>
					<span class="shimmer select" style:height="38px" style:--delay="0.15s"></span>
				</div>
			</div>
		{/if}
	</Panel>
	<Panel flex="1 1 360px" padding="24px" gap={4}>
		<Eyebrow>UPDATE STEPS</Eyebrow>
		<UpdateSteps {steps} />
		{#if upToDate && !job}
			<span class="ok">Up to date: nothing to do.</span>
		{/if}
	</Panel>
</Band>

{#if timeline && active && target && !problem && timeline.items.length > 0}
	<Timeline {timeline} running={active.tag} targetTag={target.tag} {aheadBy} {keywords} {now} />
{/if}

<div class="bottom">
	<InstalledBuilds
		{builds}
		{switching}
		{busy}
		{now}
		errors={buildErrors}
		onswitch={switchTo}
		ondelete={remove}
	/>

	<Panel flex="1 1 520px" aria-labelledby="co-h">
		<SectionHeading
			id="co-h"
			title="BACKEND"
			subtitle={hardware ? `Detected: ${hardwareLine(hardware)}` : undefined}
		/>
		{#if options.length > 0}
			<div class="backends">
				{#each options as o (o.backend)}
					<SelectCard
						variant="compact"
						selected={o.backend === backend}
						disabled={!!o.reason}
						onclick={() => (backendChosen = o.backend)}
					>
						<span class="backend-name"
							>{backendLabel(o.backend)}{o.variant ? ` ${o.variant}` : ''}</span
						>
						<span class="meta">{o.reason ?? formatBytes(o.size)}</span>
					</SelectCard>
				{/each}
			</div>
			<span class="note">For {target?.tag}. A backend your hardware cannot use says why.</span>
		{:else if problem}
			<span class="note">The backends of a release come from GitHub, which is not answering.</span>
		{:else}
			<span class="note">
				{target
					? `Looking up the backends of ${target.tag}…`
					: 'Pick a version to see its backends.'}
			</span>
		{/if}
	</Panel>
</div>

<style>
	.versions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-18) var(--space-28);
	}

	.version {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		min-width: 0;
	}

	.label {
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}

	.meta {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
		overflow-wrap: anywhere;
	}

	@media (max-width: 480px) {
		.versions svg {
			display: none;
		}
	}

	.buttons {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-8);
	}

	.buttons:empty {
		display: none;
	}

	.progress {
		display: flex;
		flex-direction: column;
		gap: var(--space-10);
	}

	.progress-line {
		display: flex;
		justify-content: space-between;
		gap: var(--space-8);
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		color: var(--fg-secondary);
		overflow-wrap: anywhere;
	}

	.selector {
		display: flex;
		flex-direction: column;
		gap: var(--space-10);
		padding-top: var(--space-14);
		border-top: 1px solid var(--border-row);
	}

	.selector-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-12);
	}

	/* The row reads as one control under its VERSION heading, so its fields' labels are for screen readers. */
	.selector-row :global(.field > span:first-child) {
		position: absolute;
		width: 1px;
		height: 1px;
		overflow: hidden;
		clip: rect(0 0 0 0);
	}

	.selector-head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-8);
	}

	.reading {
		display: inline-flex;
		align-items: center;
		gap: var(--space-8);
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--color-decode);
	}

	.select {
		flex: 1 1 200px;
		min-width: 0;
	}

	.custom {
		flex: 1 1 200px;
		min-width: 0;
		display: flex;
		align-items: center;
		gap: var(--space-8);
	}

	.custom :global(.field) {
		flex: 1;
		min-width: 0;
	}

	.bottom {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-start;
		gap: var(--space-16);
	}

	.backends {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(130px, 1fr));
		gap: var(--space-8);
	}

	.backend-name {
		font-weight: 500;
	}

	.note {
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}

	.ok {
		padding-top: var(--space-8);
		font-size: var(--fs-12);
		color: var(--color-ok);
	}

	.warn {
		font-size: var(--fs-13);
		color: var(--color-warn);
	}

	.error {
		font-size: var(--fs-12);
		color: var(--color-error-soft);
		overflow-wrap: anywhere;
	}

	:global(input.mono) {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}
</style>
