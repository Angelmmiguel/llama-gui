<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { formatUptime } from '$lib/format';
	import StatusDot from './StatusDot.svelte';
	import { tones } from './tones';

	interface Props {
		/** Router state from `/status`, or `unreachable` and `connecting` from the client. */
		serverState?: string;
		/** Seconds; shown under the state when known. */
		uptimeSeconds?: number | null;
		/** The update dot on Builds. */
		updateAvailable?: boolean;
		onsearch?: () => void;
		onserver?: () => void;
	}

	let {
		serverState = 'connecting',
		uptimeSeconds = null,
		updateAvailable = false,
		onsearch,
		onserver
	}: Props = $props();

	const groups = [
		[
			{ route: '/', label: 'Overview', href: resolve('/') },
			{ route: '/slots', label: 'Slots', href: resolve('/slots') },
			{ route: '/requests', label: 'Requests', href: resolve('/requests') },
			{ route: '/logs', label: 'Logs', href: resolve('/logs') }
		],
		[{ route: '/library', label: 'Library', href: resolve('/library') }],
		[
			{ route: '/profiles', label: 'Profiles', href: resolve('/profiles') },
			{ route: '/builds', label: 'Builds', href: resolve('/builds') },
			{ route: '/bench', label: 'Bench', href: resolve('/bench') }
		]
	];

	const STATE_COLORS: Record<string, string> = {
		running: tones.ok,
		starting: tones.decode,
		stopping: tones.decode,
		crashed: tones.error,
		unreachable: tones.error
	};

	const current = $derived(page.route.id ?? '');
	const label = $derived(serverState.charAt(0).toUpperCase() + serverState.slice(1));
	const stateColor = $derived(STATE_COLORS[serverState] ?? tones.off);

	function isActive(route: string): boolean {
		return route === '/' ? current === '/' : current === route || current.startsWith(`${route}/`);
	}
</script>

<header>
	<div class="bar">
		<a class="home" href={resolve('/')} aria-label="llama-gui home">
			<svg aria-hidden="true" width="22" height="22" viewBox="0 0 24 24" fill="none">
				<rect x="2" y="3" width="20" height="4" rx="1" fill="var(--color-decode)"></rect>
				<rect x="2" y="10" width="14" height="4" rx="1" fill="var(--color-prefill)"></rect>
				<rect x="2" y="17" width="8" height="4" rx="1" fill="var(--color-cached)"></rect>
			</svg>
			<span class="wordmark">llama<span class="suffix">-gui</span></span>
		</a>

		<nav aria-label="Primary">
			{#each groups as group, g (g)}
				<div class="group">
					{#if g > 0}<span class="divider" aria-hidden="true"></span>{/if}
					<div class="links">
						{#each group as item (item.route)}
							{@const active = isActive(item.route)}
							<a
								class="link"
								class:active
								href={item.href}
								aria-current={active ? 'page' : undefined}
							>
								{item.label}
								{#if item.route === '/builds' && updateAvailable}
									<span class="update" role="img" aria-label="update available"></span>
								{/if}
							</a>
						{/each}
					</div>
				</div>
			{/each}
		</nav>

		<div class="tools">
			<button
				type="button"
				class="tool search"
				aria-label="Search or run a command"
				onclick={onsearch}
			>
				<svg
					aria-hidden="true"
					width="14"
					height="14"
					viewBox="0 0 24 24"
					fill="none"
					stroke="currentColor"
					stroke-width="2"
					stroke-linecap="round"
				>
					<circle cx="11" cy="11" r="7"></circle>
					<path d="m20 20-3.5-3.5"></path>
				</svg>
				<span class="search-label">Search</span>
				<span class="kbd">⌘K</span>
			</button>
			<button
				type="button"
				class="tool server"
				aria-label="Server {serverState}. Open server controls"
				onclick={onserver}
			>
				<StatusDot color={stateColor} pulse={serverState === 'running'} />
				<span class="server-text">
					<span class="state">{label}</span>
					{#if uptimeSeconds !== null}
						<span class="uptime">up {formatUptime(uptimeSeconds)}</span>
					{/if}
				</span>
				<svg
					aria-hidden="true"
					width="14"
					height="14"
					viewBox="0 0 24 24"
					fill="none"
					stroke="var(--fg-muted)"
					stroke-width="2"
					stroke-linecap="round"
					stroke-linejoin="round"
				>
					<path d="m7 10 5 5 5-5"></path>
				</svg>
			</button>
		</div>
	</div>
</header>

<style>
	header {
		background: var(--bg-ground);
		border-bottom: 1px solid var(--border-panel);
		font-size: var(--fs-14);
		color: var(--fg-primary);
	}

	.bar {
		display: flex;
		flex-wrap: wrap;
		align-items: stretch;
		gap: 0 var(--space-28);
		padding: 0 var(--space-24);
		min-height: 60px;
	}

	.home {
		display: flex;
		align-items: center;
		gap: var(--space-10);
		min-height: 44px;
		color: var(--fg-primary);
	}

	.wordmark {
		font-family: var(--font-mono);
		font-weight: 500;
		font-size: var(--fs-16);
		letter-spacing: -0.01em;
	}

	.suffix {
		color: var(--fg-muted);
	}

	nav {
		flex: 1 1 520px;
		min-width: 0;
		display: flex;
		align-items: stretch;
		gap: var(--space-18);
		overflow-x: auto;
	}

	.group {
		display: flex;
		align-items: stretch;
		gap: var(--space-18);
	}

	.divider {
		width: 1px;
		align-self: center;
		height: 20px;
		background: var(--border-control);
	}

	.links {
		display: flex;
		align-items: stretch;
		gap: var(--space-2);
	}

	.link {
		display: flex;
		align-items: center;
		gap: var(--space-6);
		min-height: 44px;
		padding: 0 var(--space-10);
		border-top: 2px solid transparent;
		border-bottom: 2px solid transparent;
		color: var(--fg-tertiary);
	}

	.link.active {
		color: var(--fg-bright);
		font-weight: 500;
		border-bottom-color: var(--color-decode);
	}

	.update {
		width: 6px;
		height: 6px;
		border-radius: var(--radius-round);
		background: var(--color-decode);
	}

	.tools {
		display: flex;
		align-items: center;
		gap: var(--space-8);
		padding: var(--space-10) 0;
	}

	.tool {
		display: flex;
		align-items: center;
		gap: var(--space-10);
		min-height: 40px;
		background: var(--bg-control);
		border: 1px solid var(--border-control);
		border-radius: var(--radius-5);
	}

	.search {
		min-width: 160px;
		padding: 0 var(--space-8) 0 var(--space-12);
		color: var(--fg-muted);
		font-size: var(--fs-13);
	}

	.search-label {
		flex: 1;
		text-align: left;
	}

	.kbd {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		border: 1px solid var(--border-ghost);
		border-radius: var(--radius-3);
		padding: 1px 5px;
	}

	.server {
		padding: 0 var(--space-10) 0 var(--space-12);
		color: var(--fg-primary);
	}

	.server-text {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		line-height: 1.25;
	}

	.state {
		font-size: var(--fs-13);
		font-weight: 500;
	}

	.uptime {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
	}
</style>
