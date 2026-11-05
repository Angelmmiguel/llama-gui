<script lang="ts">
	import type { DownloadJob } from '$lib/api/client';
	import Button from '$lib/components/Button.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import ProgressBar from '$lib/components/ProgressBar.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import { tones } from '$lib/components/tones';
	import { formatBytes, formatDuration } from '$lib/format';
	import { isActive, jobName, jobPercent, orderJobs } from './view';

	interface Props {
		jobs: readonly DownloadJob[];
		oncancel: (job: DownloadJob) => void;
	}

	let { jobs, oncancel }: Props = $props();

	const shown = $derived(orderJobs(jobs));
</script>

<Panel aria-labelledby="dl-h">
	<SectionHeading title="DOWNLOADS" id="dl-h" />
	{#if shown.length === 0}
		<EmptyState
			title="Nothing downloading"
			detail="Models you download from Hugging Face show here."
		/>
	{:else}
		<ul class="jobs">
			{#each shown as job (job.id)}
				<li class="job">
					<div class="spread name">
						<span>{jobName(job)}</span>
						<span class="mono">{job.variant}</span>
					</div>
					{#if job.state === 'downloading'}
						<ProgressBar
							value={jobPercent(job)}
							color={tones.prefill}
							flow
							label={`${jobName(job)}: ${jobPercent(job)}%`}
						/>
						<div class="spread mono">
							<span>
								{formatBytes(job.done)} / {formatBytes(job.total)} · {jobPercent(job)}%
							</span>
							<span>
								{#if job.bytes_per_sec > 0}{formatBytes(job.bytes_per_sec)}/s{/if}
								{#if job.eta_seconds > 0}· {formatDuration(job.eta_seconds)} left{/if}
							</span>
						</div>
					{:else if job.state === 'failed'}
						<span class="mono failed">failed · {job.error}</span>
					{:else}
						<span class="mono">{job.state} · {formatBytes(job.total)}</span>
					{/if}
					{#if isActive(job)}
						<div>
							<Button variant="ghost" size="xs" onclick={() => oncancel(job)}>Cancel</Button>
						</div>
					{/if}
				</li>
			{/each}
		</ul>
	{/if}
</Panel>

<style>
	.jobs {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
	}

	.job {
		display: flex;
		flex-direction: column;
		gap: var(--space-8);
		padding-block: var(--space-10);
		border-top: 1px solid var(--border-row);
	}

	.job:first-child {
		padding-top: 0;
		border-top: none;
	}

	.job:last-child {
		padding-bottom: 0;
	}

	.spread {
		display: flex;
		flex-wrap: wrap;
		justify-content: space-between;
		gap: var(--space-2) var(--space-8);
	}

	.name {
		font-size: var(--fs-13);
		min-width: 0;
		overflow-wrap: anywhere;
	}

	.mono {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
	}

	.name .mono {
		font-size: var(--fs-12);
	}

	.failed {
		color: var(--color-error-soft);
		overflow-wrap: anywhere;
	}
</style>
