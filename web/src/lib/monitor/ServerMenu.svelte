<script lang="ts">
	import { tick } from 'svelte';
	import { resolve } from '$app/paths';
	import { restartServer, startServer, stopServer } from '$lib/api/client';
	import Button from '$lib/components/Button.svelte';
	import StatusDot from '$lib/components/StatusDot.svelte';
	import TextLink from '$lib/components/TextLink.svelte';
	import { tones } from '$lib/components/tones';
	import { formatUptime } from '$lib/format';
	import { server } from '$lib/state/server.svelte';
	import { backendLabel } from './overview';
	import { errorText, every } from './poll';

	const OPEN_POLL_MS = 1000;
	const FOCUSABLE = 'a[href], button:not([disabled])';

	const STATE_COLORS: Record<string, string> = {
		running: tones.ok,
		starting: tones.decode,
		stopping: tones.decode,
		crashed: tones.error,
		unreachable: tones.error
	};

	let open = $state(false);
	let confirming = $state(false);
	let working = $state<'start' | 'stop' | 'restart' | null>(null);
	let problem = $state('');

	let root = $state<HTMLElement>();
	let trigger = $state<HTMLButtonElement>();
	let panel = $state<HTMLElement>();

	const serverState = $derived(server.state);
	const label = $derived(serverState.charAt(0).toUpperCase() + serverState.slice(1));
	const stateColor = $derived(STATE_COLORS[serverState] ?? tones.off);
	const uptime = $derived(server.uptimeSeconds);
	const status = $derived(server.status);
	const build = $derived(status?.active_build ?? null);
	const loaded = $derived(status?.models_loaded ?? 0);

	const canStart = $derived(
		Boolean(build) && (serverState === 'stopped' || serverState === 'crashed')
	);
	const canStop = $derived(['running', 'starting', 'crashed'].includes(serverState));
	const canRestart = $derived(
		Boolean(build) && (serverState === 'running' || serverState === 'crashed')
	);

	function focusables(): HTMLElement[] {
		return panel ? [...panel.querySelectorAll<HTMLElement>(FOCUSABLE)] : [];
	}

	async function focusFirst(selector = FOCUSABLE) {
		await tick();
		panel?.querySelector<HTMLElement>(selector)?.focus();
	}

	function show() {
		open = true;
		confirming = false;
		problem = '';
		void focusFirst();
	}

	function close(restoreFocus: boolean) {
		open = false;
		confirming = false;
		if (restoreFocus) trigger?.focus();
	}

	function onkeydown(e: KeyboardEvent) {
		if (!open) return;
		if (e.key === 'Escape') {
			e.preventDefault();
			e.stopPropagation();
			if (confirming) {
				confirming = false;
				void focusFirst();
			} else {
				close(true);
			}
			return;
		}
		if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
		const items = focusables();
		if (items.length === 0 || !panel?.contains(document.activeElement)) return;
		e.preventDefault();
		const at = items.indexOf(document.activeElement as HTMLElement);
		const step = e.key === 'ArrowDown' ? 1 : -1;
		items[(at + step + items.length) % items.length].focus();
	}

	// Tabbing out or clicking elsewhere closes it, leaving focus where the user put it.
	function onfocusout(e: FocusEvent) {
		const next = e.relatedTarget;
		if (next instanceof Node && !root?.contains(next)) close(false);
	}

	$effect(() => {
		if (!open) return;
		const outside = (e: PointerEvent) => {
			if (e.target instanceof Node && !root?.contains(e.target)) close(false);
		};
		document.addEventListener('pointerdown', outside);
		return () => document.removeEventListener('pointerdown', outside);
	});

	// A state change is what the user opened this to watch.
	$effect(() => {
		if (!open) return;
		return every(OPEN_POLL_MS, () => server.poll());
	});

	async function act(name: 'start' | 'stop' | 'restart', op: () => Promise<unknown>) {
		working = name;
		problem = '';
		try {
			await op();
			confirming = false;
			await server.poll();
		} catch (e) {
			problem = errorText(e);
		} finally {
			working = null;
			if (open && !panel?.contains(document.activeElement)) void focusFirst();
		}
	}

	function askStop() {
		confirming = true;
		void focusFirst('[data-keep]');
	}
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="menu" bind:this={root} {onkeydown} {onfocusout}>
	<button
		type="button"
		class="trigger"
		bind:this={trigger}
		aria-label="Server {serverState}. Open server controls"
		aria-haspopup="dialog"
		aria-expanded={open}
		onclick={() => (open ? close(true) : show())}
	>
		<StatusDot color={stateColor} pulse={serverState === 'running'} />
		<span class="trigger-text">
			<span class="state">{label}</span>
			{#if uptime !== null}<span class="uptime">up {formatUptime(uptime)}</span>{/if}
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

	{#if open}
		<div class="panel" role="dialog" aria-label="Server controls" bind:this={panel}>
			<dl class="facts">
				<dt>ROUTER</dt>
				<dd class="value" style:color={stateColor}>{serverState}</dd>
				{#if status?.pid}
					<dt>PID</dt>
					<dd class="value">{status.pid}</dd>
				{/if}
				<dt>MODELS LOADED</dt>
				<dd class="value">{loaded}</dd>
				{#if status?.restart_count}
					<dt>RESTARTS</dt>
					<dd class="value">{status.restart_count}</dd>
				{/if}
				<dt>ACTIVE BUILD</dt>
				<dd class="value">
					<TextLink href={resolve('/builds')}>
						{build ? `${build.tag} · ${backendLabel(build.backend)}` : 'None. Builds →'}
					</TextLink>
				</dd>
			</dl>
			{#if status?.last_error && serverState !== 'running'}
				<p class="reason">{status.last_error}</p>
			{/if}

			{#if confirming}
				<div class="confirm" role="group" aria-label="Confirm stopping the router">
					<p>
						Stopping interrupts every request in flight{loaded > 0
							? ` and unloads ${loaded} model${loaded === 1 ? '' : 's'}`
							: ''}. Proxied clients get a 503 until it starts again.
					</p>
					<div class="buttons">
						<Button
							size="sm"
							variant="danger"
							fill
							disabled={working !== null}
							onclick={() => act('stop', stopServer)}
						>
							{working === 'stop' ? 'Stopping…' : 'Stop the router'}
						</Button>
						<Button
							size="sm"
							fill
							data-keep
							onclick={() => ((confirming = false), void focusFirst())}
						>
							Keep it running
						</Button>
					</div>
				</div>
			{:else}
				<div class="buttons">
					<Button
						size="sm"
						fill
						disabled={!canStart || working !== null}
						onclick={() => act('start', startServer)}
					>
						{working === 'start' ? 'Starting…' : 'Start'}
					</Button>
					<Button
						size="sm"
						variant="danger"
						fill
						disabled={!canStop || working !== null}
						onclick={askStop}
					>
						Stop
					</Button>
					<Button
						size="sm"
						fill
						disabled={!canRestart || working !== null}
						onclick={() => act('restart', restartServer)}
					>
						{working === 'restart' ? 'Restarting…' : 'Restart'}
					</Button>
				</div>
			{/if}
			{#if problem}<p class="problem" role="alert">{problem}</p>{/if}
		</div>
	{/if}
</div>

<style>
	.menu {
		position: relative;
	}

	.trigger {
		display: flex;
		align-items: center;
		gap: var(--space-10);
		min-height: 40px;
		padding: 0 var(--space-10) 0 var(--space-12);
		background: var(--bg-control);
		border: 1px solid var(--border-control);
		border-radius: var(--radius-5);
		color: var(--fg-primary);
	}

	.trigger-text {
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

	.panel {
		position: absolute;
		top: calc(100% + var(--space-6));
		right: 0;
		z-index: 20;
		width: 320px;
		display: flex;
		flex-direction: column;
		gap: var(--space-12);
		padding: var(--space-14) var(--space-16);
		background: var(--bg-panel);
		border: 1px solid var(--border-ghost);
		border-radius: var(--radius-6);
		box-shadow: 0 12px 32px rgba(0, 0, 0, 0.5);
		font-size: var(--fs-13);
	}

	.facts {
		display: grid;
		grid-template-columns: auto minmax(0, 1fr);
		gap: var(--space-8) var(--space-14);
		align-items: baseline;
	}

	dt {
		font-family: var(--font-mono);
		font-size: var(--fs-10);
		letter-spacing: var(--tracking-column);
		color: var(--fg-faint);
	}

	.value {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		text-align: right;
		overflow-wrap: anywhere;
	}

	.reason {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		color: var(--color-error-soft);
		overflow-wrap: anywhere;
	}

	.confirm {
		display: flex;
		flex-direction: column;
		gap: var(--space-10);
		line-height: 1.5;
		color: var(--fg-secondary);
	}

	.buttons {
		display: flex;
		gap: var(--space-6);
	}

	.problem {
		font-size: var(--fs-12);
		color: var(--color-error-soft);
		overflow-wrap: anywhere;
	}

	@media (max-width: 480px) {
		.panel {
			position: fixed;
			top: auto;
			left: var(--space-16);
			right: var(--space-16);
			width: auto;
		}
	}
</style>
