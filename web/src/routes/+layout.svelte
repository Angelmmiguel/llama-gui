<script lang="ts">
	import '$lib/styles/global.css';
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import Nav from '$lib/components/Nav.svelte';
	import GettingStarted from '$lib/monitor/GettingStarted.svelte';
	import { server } from '$lib/state/server.svelte';

	let { children }: { children: Snippet } = $props();

	$effect(() => server.start());
</script>

<div class="app">
	<Nav serverState={server.state} uptimeSeconds={server.uptimeSeconds} />
	<main>
		<!-- The Overview shows it whole, and Builds is where the install itself is. -->
		{#if page.route.id !== '/' && page.route.id !== '/builds'}
			<GettingStarted compact />
		{/if}
		{@render children()}
	</main>
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
