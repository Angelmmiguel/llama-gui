<script lang="ts">
	import { getRequest, listRequests, type RequestDetail, type ServerStatus } from '$lib/api/client';
	import Button from '$lib/components/Button.svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import Eyebrow from '$lib/components/Eyebrow.svelte';
	import StatGrid from '$lib/components/StatGrid.svelte';
	import { tones } from '$lib/components/tones';
	import { formatClock, formatInt, formatMs, formatRate } from '$lib/format';
	import {
		curlCommand,
		median,
		messageText,
		overlappingPrefills,
		SLOW_FACTOR,
		slowTokens,
		statusLevel
	} from './view';

	interface Props {
		id: number | null;
		/** Capture mode from the status, to word the hint for a missing body. */
		capture: ServerStatus['capture'] | undefined;
		/** Tells the page, so the scatter can mark it. */
		onload?: (detail: RequestDetail | null) => void;
	}

	let { id, capture, onload }: Props = $props();

	const REPLAY_TITLE = 'Not available yet: replaying into the Web UI is still being worked out.';
	/** A prefill older than this would have finished long before the first slow token. */
	const OVERLAP_LOOKBACK_MS = 300_000;

	let detail = $state<RequestDetail | null>(null);
	let error = $state('');
	let overlaps = $state<number[]>([]);

	$effect(() => {
		const current = id;
		// A filter clears the selection until its first row is picked: keep this one up until then.
		if (current === null) return;
		let stale = false;
		overlaps = [];
		error = '';
		getRequest(current)
			.then((loaded) => {
				if (stale) return;
				detail = loaded;
				onload?.(loaded);
				return findOverlaps(loaded).then((found) => {
					if (!stale) overlaps = found;
				});
			})
			.catch((e: unknown) => {
				if (stale) return;
				detail = null;
				onload?.(null);
				error = e instanceof Error ? e.message : String(e);
			});
		return () => {
			stale = true;
		};
	});

	/** One query for the slow-token note; only spent on requests that have slow tokens. */
	async function findOverlaps(r: RequestDetail): Promise<number[]> {
		const slow = slowTokens(r.gaps);
		if (slow.length === 0 || !r.model) return [];
		const start = Date.parse(r.started_at);
		const { requests } = await listRequests({
			model: r.model,
			since: new Date(start - OVERLAP_LOOKBACK_MS).toISOString(),
			until: new Date(start + r.total_ms).toISOString(),
			limit: 200
		});
		return overlappingPrefills(r, slow, requests);
	}

	const label = (r: RequestDetail) => r.error || String(r.status);
	const statusColor = { ok: tones.ok, warn: tones.warn, error: tones.errorSoft };

	const gaps = $derived(detail?.gaps ?? []);
	const slow = $derived(new Set(slowTokens(gaps)));
	const tallest = $derived(Math.max(1, ...gaps));

	const tokensNote = $derived.by(() => {
		if (!detail) return '';
		const n = detail.predicted_n;
		return gaps.length > 0 && n > gaps.length + 1
			? `first ${gaps.length + 1} of ${formatInt(n)} tokens`
			: `${formatInt(n)} tokens`;
	});

	const streamNote = $derived.by(() => {
		if (!detail) return '';
		if (gaps.length === 0) {
			if (detail.status >= 400) return `Failed with ${label(detail)} before any tokens streamed.`;
			if (!detail.stream) return 'Not streamed, so there is no timing per token.';
			return 'No tokens were timed for this request.';
		}
		const typical = `median ${formatMs(median(gaps))}, tallest ${formatMs(tallest)}`;
		if (slow.size === 0) return `No slow tokens: ${typical}.`;
		const count = `${slow.size} slow token${slow.size === 1 ? '' : 's'} (over ${SLOW_FACTOR}× the median; ${typical}).`;
		if (overlaps.length === 0) {
			return `${count} No other request on this model was in prefill at those moments.`;
		}
		const ids = overlaps
			.slice(0, 3)
			.map((o) => `#${o}`)
			.join(', ');
		const more = overlaps.length > 3 ? ` and ${overlaps.length - 3} more` : '';
		return `${count} They overlapped the prefill of ${ids}${more} on the same model.`;
	});

	const timings = $derived(
		detail
			? [
					{ label: 'prompt_n', value: formatInt(detail.prompt_n) },
					{ label: 'cache_n', value: formatInt(detail.cache_n) },
					{ label: 'prompt_ms', value: formatMs(detail.prompt_ms) },
					{ label: 'predicted_n', value: formatInt(detail.predicted_n) },
					{ label: 'predicted_ms', value: formatMs(detail.predicted_ms) },
					{ label: 'tok/s', value: formatRate(detail.predicted_per_second) },
					{ label: 'queue', value: formatMs(detail.queue_ms) },
					{ label: 'ttft', value: formatMs(detail.ttft_ms) },
					{ label: 'total', value: formatMs(detail.total_ms) }
				]
			: []
	);

	const bodyHint = $derived(
		capture === 'bodies'
			? 'No body was captured for this request.'
			: 'Bodies are not captured: start with LLAMA_GUI_CAPTURE=bodies to keep them.'
	);
</script>

{#if error}
	<EmptyState title="Could not load this request" detail={error} />
{:else if !detail}
	<EmptyState title="Select a request" detail="Its timings and token stream show here." />
{:else}
	<div class="head">
		<Eyebrow>REQUEST #{detail.id} · {formatClock(detail.started_at)}</Eyebrow>
		<h2 id="detail-heading" class="endpoint">{detail.method} {detail.endpoint}</h2>
		<span class="meta">
			{detail.model || 'unknown model'} · slot {detail.slot ?? '—'} ·
			<span style:color={statusColor[statusLevel(label(detail))]}>{label(detail)}</span>
		</span>
	</div>

	<div class="stack">
		<div class="row">
			<Eyebrow>TOKEN STREAM · ms BETWEEN TOKENS</Eyebrow>
			<span class="tokens">{tokensNote}</span>
		</div>
		{#if gaps.length > 0}
			<div class="bars" role="img" aria-label="Bar chart of the delay before each generated token">
				{#each gaps as gap, i (i)}
					<div
						class="bar"
						class:slow={slow.has(i)}
						style:height="{Math.max((gap / tallest) * 100, 1)}%"
					></div>
				{/each}
			</div>
		{/if}
		<span class="note">{streamNote}</span>
	</div>

	<StatGrid items={timings} columns={3} valueSize={14} labelStyle="mono" />

	{#if detail.body}
		<div class="stack">
			<Eyebrow>MESSAGES</Eyebrow>
			<div class="messages">{messageText(detail.body)}</div>
		</div>
	{/if}

	<div class="actions">
		<Button variant="primary" size="lg" fill disabled title={REPLAY_TITLE}>Replay in Web UI</Button>
		{#if detail.body}
			<CopyButton
				text={curlCommand(detail, detail.body.request, location.origin)}
				label="Copy as cURL"
				size="lg"
				fill
			/>
		{:else}
			<Button variant="ghost" size="lg" fill disabled title={bodyHint}>Copy as cURL</Button>
		{/if}
	</div>
	{#if !detail.body}<span class="note">{bodyHint}</span>{/if}
{/if}

<style>
	.head,
	.stack {
		display: flex;
		flex-direction: column;
		gap: var(--space-8);
	}

	.head {
		gap: var(--space-6);
	}

	.endpoint {
		font-family: var(--font-mono);
		font-size: var(--fs-15);
		font-weight: 500;
		overflow-wrap: anywhere;
	}

	.meta {
		font-size: var(--fs-13);
		color: var(--fg-muted);
	}

	.row {
		display: flex;
		flex-wrap: wrap;
		justify-content: space-between;
		gap: var(--space-8);
	}

	.tokens {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
	}

	.bars {
		display: flex;
		align-items: flex-end;
		gap: 1px;
		height: 96px;
		border-bottom: 1px solid var(--chart-axis);
	}

	.bar {
		flex: 1 1 0;
		min-width: 0;
		background: var(--color-decode);
		border-radius: 1px 1px 0 0;
	}

	.bar.slow {
		background: var(--color-prefill);
	}

	.note {
		font-size: var(--fs-12);
		color: var(--fg-muted);
		line-height: 1.5;
	}

	.messages {
		max-height: 320px;
		overflow: auto;
		padding: var(--space-12);
		background: var(--bg-deep);
		border: 1px solid var(--border-panel);
		border-radius: var(--radius-5);
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		line-height: 1.6;
		color: var(--fg-log);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-8);
	}
</style>
