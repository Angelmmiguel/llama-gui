<script lang="ts">
	import Legend from '$lib/components/Legend.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import ScrollX from '$lib/components/ScrollX.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import { tones } from '$lib/components/tones';
	import { formatInt } from '$lib/format';
	import { ago, dotPosition, plainNotes, type Timeline } from './view';

	interface Props {
		timeline: Timeline;
		/** The running build's tag. */
		running: string;
		/** The tag being compared with. */
		targetTag: string;
		/** Commits between the two, null when unknown. */
		aheadBy: number | null;
		/** The words "affects you" looks for. */
		keywords: string[];
		now: number;
	}

	let { timeline, running, targetTag, aheadBy, keywords, now }: Props = $props();

	let picked = $state('');
	const items = $derived(timeline.items);
	const current = $derived(items.find((i) => i.tag === picked) ?? items[items.length - 1]);
	const notes = $derived(current ? plainNotes(current.release.body) : '');
	const words = $derived(keywords.join(', '));
	const summary = $derived(
		[
			aheadBy !== null ? `${formatInt(aheadBy)} commits since yours.` : '',
			timeline.hidden > 0
				? `Showing ${formatInt(items.length)} releases of ${formatInt(items.length + timeline.hidden)}.`
				: ''
		]
			.filter(Boolean)
			.join(' ')
	);

	function dotColor(item: (typeof items)[number]) {
		if (item.matches.length > 0) return tones.decode;
		return item.stable ? tones.primary : 'var(--fg-gutter)';
	}
</script>

<Panel gap={18} aria-labelledby="tl-h">
	<SectionHeading
		id="tl-h"
		title="WHAT YOU'RE MISSING · {running} → {targetTag}"
		subtitle={summary || undefined}
	>
		{#snippet actions()}
			<Legend
				items={[
					{ label: 'Stable release', shape: 'dot', color: tones.primary, size: 12 },
					{ label: `Mentions ${words} (keyword match)`, shape: 'dot', color: tones.decode },
					{ label: 'Other nightly', shape: 'dot', color: 'var(--fg-gutter)', size: 8 }
				]}
			/>
		{/snippet}
	</SectionHeading>

	<ScrollX minWidth={900}>
		<div class="track" role="group" aria-label="Releases since your build">
			<div class="line"></div>
			<span class="dot-wrap you">
				<span class="tag">you · {running}</span>
				<span
					class="dot"
					style:width="10px"
					style:height="10px"
					style:background="var(--fg-primary)"
				></span>
			</span>
			{#each items as item, i (item.tag)}
				{@const size = item.stable ? 14 : 8}
				{@const on = item === current}
				<button
					type="button"
					class="dot-wrap"
					style:left="{dotPosition(i + 1, items.length + 1)}%"
					aria-pressed={on}
					aria-label="{item.tag}{item.stable ? ', stable' : ''}{item.matches.length > 0
						? `, mentions ${item.matches.join(', ')}`
						: ''}"
					onclick={() => (picked = item.tag)}
				>
					<span class="tag" class:on>
						{#if item.stable || on || i === items.length - 1}{item.tag}{/if}
					</span>
					<span
						class="dot"
						class:on
						style:width="{size}px"
						style:height="{size}px"
						style:background={dotColor(item)}
					></span>
				</button>
			{/each}
		</div>
	</ScrollX>

	{#if current}
		<Panel tone="inset" radius={10} row padding="16px 18px" gap={20}>
			<div class="head">
				<span class="rel">{current.tag}</span>
				<span class="when"
					>{current.stable ? 'stable' : 'nightly'} · {ago(current.release.published_at, now)}</span
				>
				{#if current.matches.length > 0}
					<span class="match">Affects you? Keyword match: {current.matches.join(', ')}</span>
				{:else}
					<span class="when">No mention of {words}</span>
				{/if}
			</div>
			<!-- A scrolling box has to be reachable by keyboard. -->
			<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
			<div class="notes" tabindex="0" role="region" aria-label="Notes of {current.tag}">
				{notes || 'This release has no notes.'}
			</div>
		</Panel>
	{/if}
</Panel>

<style>
	.track {
		position: relative;
		height: 64px;
	}

	.line {
		position: absolute;
		left: 0;
		right: 0;
		top: 40px;
		height: 2px;
		background: var(--border-control);
	}

	.dot-wrap {
		position: absolute;
		top: 0;
		height: 64px;
		min-width: 44px;
		transform: translateX(-50%);
		display: flex;
		flex-direction: column;
		align-items: center;
		padding: 0;
		background: transparent;
		border: none;
		cursor: pointer;
	}

	.you {
		left: 0;
		transform: none;
		align-items: flex-start;
		cursor: default;
	}

	.tag {
		height: 22px;
		font-family: var(--font-mono);
		font-size: var(--fs-10);
		color: var(--fg-muted);
		white-space: nowrap;
	}

	.tag.on {
		color: var(--fg-bright);
	}

	.dot {
		position: absolute;
		top: 41px;
		transform: translateY(-50%);
		border-radius: 50%;
	}

	.dot.on {
		box-shadow: 0 0 0 4px rgb(255 255 255 / 15%);
	}

	.head {
		flex: 0 0 180px;
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		align-self: flex-start;
	}

	.rel {
		font-family: var(--font-mono);
		font-size: var(--fs-20);
	}

	.when {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
	}

	.match {
		font-size: var(--fs-12);
		color: var(--color-decode);
	}

	.notes {
		flex: 1 1 400px;
		min-width: 0;
		max-height: 260px;
		overflow-y: auto;
		align-self: flex-start;
		font-size: var(--fs-13);
		line-height: 1.5;
		color: var(--fg-secondary);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
</style>
