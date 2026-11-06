<script lang="ts">
	import '$lib/styles/global.css';
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import Nav from '$lib/components/Nav.svelte';
	import CommandPalette from '$lib/monitor/CommandPalette.svelte';
	import GettingStarted from '$lib/monitor/GettingStarted.svelte';
	import ServerMenu from '$lib/monitor/ServerMenu.svelte';
	import StatusBanner from '$lib/monitor/StatusBanner.svelte';
	import { server } from '$lib/state/server.svelte';

	let { children }: { children: Snippet } = $props();

	let searching = $state(false);

	$effect(() => server.start());
</script>

<div class="app">
	<Nav
		updateAvailable={server.status?.update_available ?? false}
		onsearch={() => (searching = true)}
	>
		{#snippet serverControl()}<ServerMenu />{/snippet}
	</Nav>
	<StatusBanner />
	<main>
		<!-- The Overview shows it whole, and Builds is where the install itself is. -->
		{#if page.route.id !== '/' && page.route.id !== '/builds'}
			<GettingStarted compact />
		{/if}
		{@render children()}
	</main>
	<CommandPalette bind:open={searching} />
</div>

<style>
	.app {
		min-height: 100vh;
		background: var(--bg-ground);
		color: var(--fg-primary);
		font-family: var(--font-sans);
		font-size: var(--fs-14);
	}

	main {
		display: flex;
		flex-direction: column;
		gap: var(--space-16);
		padding: var(--space-24) var(--space-24) var(--space-48);
	}
</style>
