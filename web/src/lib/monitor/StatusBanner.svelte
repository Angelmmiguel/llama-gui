<script lang="ts">
	import { resolve } from '$app/paths';
	import TextLink from '$lib/components/TextLink.svelte';
	import { server } from '$lib/state/server.svelte';
	import { serverBanner } from './banner';

	const banner = $derived(serverBanner(server.status, server.unreachable));
</script>

{#if banner}
	<div class="banner" role="alert">
		<div class="text">
			<strong>{banner.title}</strong>
			{#if banner.detail}<span class="detail">{banner.detail}</span>{/if}
			{#if banner.note}<span class="note">{banner.note}</span>{/if}
		</div>
		{#if !server.unreachable}<TextLink href={resolve('/logs')}>Logs →</TextLink>{/if}
	</div>
{/if}

<style>
	.banner {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-8) var(--space-16);
		padding: var(--space-10) var(--space-24);
		background: var(--tint-error);
		border-bottom: 1px solid var(--border-danger);
		font-size: var(--fs-13);
	}

	.text {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: var(--space-4) var(--space-12);
		min-width: 0;
	}

	strong {
		font-weight: 500;
		color: var(--color-error-soft);
	}

	.detail {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		color: var(--fg-secondary);
		overflow-wrap: anywhere;
	}

	.note {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}
</style>
