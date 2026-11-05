<script lang="ts">
	import { resolve } from '$app/paths';
	import { LOGS_STREAM, stream, type LogRecord } from '$lib/api/client';
	import Cursor from '$lib/components/Cursor.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import LogLine from '$lib/components/LogLine.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import TextLink from '$lib/components/TextLink.svelte';
	import { clock, logMessage } from './overview';

	/** False freezes the tail; what arrives meanwhile is skipped. */
	let { live }: { live: boolean } = $props();

	const LINES = 9;

	let lines = $state.raw<LogRecord[]>([]);

	$effect(() =>
		stream<LogRecord>(
			LOGS_STREAM,
			(line) => {
				if (live) lines = [...lines, line].slice(-LINES);
			},
			{ event: 'log', query: { backlog: LINES } }
		)
	);
</script>

<Panel tone="deep" flex="3 1 420px" gap={12} aria-labelledby="log-heading">
	<SectionHeading id="log-heading" title="SERVER LOG · TAIL">
		{#snippet actions()}<TextLink href={resolve('/logs')}>Open logs →</TextLink>{/snippet}
	</SectionHeading>
	{#if lines.length === 0}
		<EmptyState title="No log lines yet" detail="The router's output appears here as it runs." />
	{:else}
		<div class="log" aria-live="off">
			{#each lines as line, i (line.id)}
				<LogLine
					variant="tail"
					ts={clock(line.ts)}
					source={line.source}
					msg={logMessage(line.source, line.message)}
					rise={i === lines.length - 1}
				/>
			{/each}
			{#if live}<Cursor />{/if}
		</div>
	{/if}
</Panel>

<style>
	.log {
		display: flex;
		flex-direction: column;
		overflow: hidden;
		min-width: 0;
	}
</style>
