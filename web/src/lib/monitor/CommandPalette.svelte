<script lang="ts">
	import { tick } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import {
		listModels,
		loadModel,
		restartServer,
		unloadModel,
		type RouterModel
	} from '$lib/api/client';
	import Spinner from '$lib/components/Spinner.svelte';
	import { server } from '$lib/state/server.svelte';
	import {
		filterCommands,
		modelCommands,
		moveActive,
		pageCommands,
		parseCommandId,
		type PageRoute,
		type PaletteCommand
	} from './palette';
	import { errorText } from './poll';

	interface Props {
		/** Bindable. */
		open?: boolean;
	}

	let { open = $bindable(false) }: Props = $props();

	let dialog = $state<HTMLDialogElement>();
	let input = $state<HTMLInputElement>();
	let list = $state<HTMLElement>();
	let query = $state('');
	let active = $state(0);
	let models = $state.raw<RouterModel[]>([]);
	let working = $state<string | null>(null);
	let problem = $state('');
	let opener: HTMLElement | null = null;

	const commands = $derived<PaletteCommand[]>([
		...pageCommands(),
		...modelCommands(models),
		...(server.status?.active_build
			? [
					{
						id: 'restart',
						label: 'Restart the server',
						hint: 'Server · interrupts requests',
						keywords: 'router llama-server'
					}
				]
			: [])
	]);
	const results = $derived(filterCommands(commands, query));
	const current = $derived(results[Math.min(active, results.length - 1)]);

	$effect(() => {
		const listener = (e: KeyboardEvent) => {
			if ((e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === 'k') {
				e.preventDefault();
				open = !open;
			}
		};
		window.addEventListener('keydown', listener);
		return () => window.removeEventListener('keydown', listener);
	});

	$effect(() => {
		if (!dialog) return;
		if (open && !dialog.open) {
			opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
			query = '';
			active = 0;
			problem = '';
			dialog.showModal();
			void tick().then(() => input?.focus());
			listModels()
				.then((m) => (models = m))
				.catch(() => (models = []));
		} else if (!open && dialog.open) {
			dialog.close();
			opener?.focus();
			opener = null;
		}
	});

	async function scrollToActive() {
		await tick();
		list?.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest' });
	}

	async function run(command: PaletteCommand | undefined) {
		if (!command || working) return;
		const { kind, target } = parseCommandId(command.id);
		working = command.id;
		problem = '';
		try {
			if (kind === 'page') {
				await goto(resolve(target as PageRoute));
			} else if (kind === 'load') {
				await loadModel(target);
			} else if (kind === 'unload') {
				await unloadModel(target);
			} else if (kind === 'restart') {
				await restartServer();
				await server.poll();
			}
			open = false;
		} catch (e) {
			problem = `${command.label}: ${errorText(e)}`;
		} finally {
			working = null;
		}
	}

	function onkeydown(e: KeyboardEvent) {
		switch (e.key) {
			case 'ArrowDown':
			case 'ArrowUp':
				e.preventDefault();
				active = moveActive(
					Math.min(active, results.length - 1),
					e.key === 'ArrowDown' ? 1 : -1,
					results.length
				);
				void scrollToActive();
				break;
			case 'Enter':
				e.preventDefault();
				void run(current);
				break;
			case 'Escape':
				e.preventDefault();
				open = false;
				break;
			case 'Tab':
				// The rows are reached with arrows, so the input is all there is to tab to.
				e.preventDefault();
				input?.focus();
				break;
		}
	}
</script>

<dialog
	bind:this={dialog}
	aria-label="Search or run a command"
	{onkeydown}
	oncancel={(e) => {
		e.preventDefault();
		open = false;
	}}
	onclick={(e) => {
		if (e.target === dialog) open = false;
	}}
>
	{#if open}
		<div class="box">
			<div class="search">
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
				<input
					bind:this={input}
					bind:value={query}
					type="text"
					role="combobox"
					aria-label="Search pages, models and commands"
					aria-expanded="true"
					aria-controls="palette-results"
					aria-autocomplete="list"
					aria-activedescendant={current ? `palette-${current.id}` : undefined}
					placeholder="Pages, models, commands"
					autocomplete="off"
					spellcheck="false"
					oninput={() => {
						active = 0;
						problem = '';
					}}
				/>
				<span class="esc">esc</span>
			</div>
			<ul id="palette-results" role="listbox" aria-label="Results" bind:this={list}>
				{#each results as command, i (command.id)}
					<!-- Focus stays in the input: keyboard use goes through the combobox above. -->
					<!-- svelte-ignore a11y_click_events_have_key_events -->
					<li
						id="palette-{command.id}"
						role="option"
						aria-selected={command === current}
						class:active={command === current}
						onclick={() => run(command)}
						onmousemove={() => (active = i)}
					>
						<span class="label">
							{#if working === command.id}<Spinner size={10} />{/if}
							{command.label}
						</span>
						<span class="hint">{command.hint}</span>
					</li>
				{:else}
					<li class="none" role="presentation">Nothing matches “{query}”.</li>
				{/each}
			</ul>
			{#if problem}<p class="problem" role="alert">{problem}</p>{/if}
		</div>
	{/if}
</dialog>

<style>
	dialog {
		width: min(560px, calc(100vw - 32px));
		max-height: min(480px, calc(100vh - 32px));
		margin: 12vh auto auto;
		padding: 0;
		overflow: hidden;
		background: var(--bg-panel);
		border: 1px solid var(--border-ghost);
		border-radius: var(--radius-7);
		color: var(--fg-primary);
		font-family: var(--font-sans);
		box-shadow: 0 24px 64px rgba(0, 0, 0, 0.6);
	}

	dialog::backdrop {
		background: rgba(8, 9, 11, 0.7);
	}

	.box {
		display: flex;
		flex-direction: column;
		max-height: inherit;
	}

	.search {
		display: flex;
		align-items: center;
		gap: var(--space-10);
		padding: 0 var(--space-14);
		min-height: 48px;
		border-bottom: 1px solid var(--border-panel);
		color: var(--fg-muted);
	}

	input {
		flex: 1;
		min-width: 0;
		min-height: 44px;
		background: transparent;
		border: none;
		outline: none;
		color: var(--fg-primary);
		font-family: inherit;
		font-size: var(--fs-14);
	}

	.esc {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		border: 1px solid var(--border-ghost);
		border-radius: var(--radius-3);
		padding: 1px 5px;
	}

	ul {
		flex: 1;
		min-height: 0;
		margin: 0;
		padding: var(--space-6);
		overflow-y: auto;
		list-style: none;
	}

	li {
		display: flex;
		box-sizing: border-box;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-12);
		min-height: 40px;
		padding: var(--space-6) var(--space-10);
		border-radius: var(--radius-4);
		font-size: var(--fs-13);
		cursor: pointer;
	}

	li.active {
		background: var(--bg-selected);
		color: var(--fg-bright);
	}

	li.none {
		color: var(--fg-muted);
		cursor: default;
	}

	.label {
		display: inline-flex;
		align-items: center;
		gap: var(--space-8);
		min-width: 0;
		overflow-wrap: anywhere;
	}

	.hint {
		flex: none;
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
	}

	.problem {
		padding: var(--space-10) var(--space-16);
		border-top: 1px solid var(--border-panel);
		font-size: var(--fs-12);
		color: var(--color-error-soft);
		overflow-wrap: anywhere;
	}

	@media (max-width: 480px) {
		dialog {
			margin-top: var(--space-16);
		}

		.hint {
			display: none;
		}
	}
</style>
