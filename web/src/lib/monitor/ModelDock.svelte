<script lang="ts">
	import { resolve } from '$app/paths';
	import {
		listModels,
		loadModel,
		unloadModel,
		type Gpu,
		type LocalModel,
		type Profile,
		type RouterModel,
		type TelemetryModel
	} from '$lib/api/client';
	import Button from '$lib/components/Button.svelte';
	import ProgressBar from '$lib/components/ProgressBar.svelte';
	import Tag from '$lib/components/Tag.svelte';
	import TextLink from '$lib/components/TextLink.svelte';
	import { tones } from '$lib/components/tones';
	import { formatBytes } from '$lib/format';
	import { diskRows, dockCells, freeMemory, type DiskRow } from './dock';
	import { errorText } from './poll';

	interface Props {
		models: readonly TelemetryModel[];
		estimates: readonly RouterModel[];
		profiles: readonly Profile[];
		/** Null until the first answer, so the empty panel doesn't flash. */
		local: readonly LocalModel[] | null;
		gpus: readonly Gpu[];
		ramAvailable: number;
		/** What the router keeps free on purpose. */
		headroom: number;
		/** False while the router cannot take the action. */
		canAct: boolean;
	}

	let { models, estimates, profiles, local, gpus, ramAvailable, headroom, canAct }: Props =
		$props();

	const UNLOAD_WAIT_MS = 30_000;
	const UNLOAD_POLL_MS = 500;
	const levelColor = { ok: tones.ok, warn: tones.warn, error: tones.errorSoft };
	// A single-page app knows its platform by the time it renders.
	const shortcut = /Mac|iPhone|iPad/.test(navigator.platform) ? '⌘K' : 'Ctrl K';

	let drawer = $state(false);
	let pending = $state.raw<ReadonlySet<string>>(new Set());
	let busy = $state<Record<string, boolean>>({});
	let failure = $state('');

	// Telemetry reports a model as loading within a second, so it takes over from there.
	$effect(() => {
		const known = pending;
		if (known.size === 0) return;
		const still = new Set(
			[...known].filter((id) => models.find((m) => m.id === id)?.status === 'unloaded')
		);
		if (still.size !== known.size) pending = still;
	});

	const cells = $derived(dockCells(models, profiles, local ?? [], pending));
	const free = $derived(freeMemory(gpus, ramAvailable));
	const rows = $derived(
		diskRows({ models, estimates, profiles, local: local ?? [], free, headroom, pending })
	);

	async function run(key: string, work: () => Promise<unknown>) {
		busy[key] = true;
		failure = '';
		try {
			await work();
		} catch (e) {
			failure = errorText(e);
		} finally {
			delete busy[key];
		}
	}

	/** Loading right after an unload would be measured against memory not yet freed. */
	async function unloaded(id: string) {
		const deadline = Date.now() + UNLOAD_WAIT_MS;
		while (Date.now() < deadline) {
			const list = await listModels();
			if (list.find((m) => m.id === id)?.status === 'unloaded') return;
			await new Promise((done) => setTimeout(done, UNLOAD_POLL_MS));
		}
		throw new Error(`${id} did not unload in time, so nothing else was loaded.`);
	}

	function load(row: DiskRow) {
		const id = row.load;
		if (!id) return;
		void run(row.key, async () => {
			if (row.unloadFirst) {
				await unloadModel(row.unloadFirst);
				await unloaded(row.unloadFirst);
			}
			await loadModel(id);
			pending = new Set([...pending, id]);
			drawer = false;
		});
	}

	const buttonLabel = (row: DiskRow) => (row.unloadFirst ? 'Swap & load' : 'Load');
	const profileHref = (path: string) => `${resolve('/profiles')}?new=${encodeURIComponent(path)}`;
</script>

{#snippet rowAction(row: DiskRow, size: 'md' | 'lg')}
	{#if row.path}
		<Button variant="ghost" {size} tone="accent" href={profileHref(row.path)}>Create profile</Button
		>
	{:else}
		<Button
			variant="ghost"
			{size}
			tone="accent"
			disabled={row.disabled || !canAct || busy[row.key]}
			onclick={() => load(row)}
		>
			{busy[row.key] ? 'Working…' : buttonLabel(row)}
		</Button>
	{/if}
{/snippet}

{#if cells.length > 0}
	<section class="dock" aria-labelledby="dock-heading">
		<header>
			<h2 id="dock-heading">MODELS IN MEMORY · {cells.length}</h2>
			<TextLink href={resolve('/library')}>Manage in Library →</TextLink>
		</header>
		<div class="cells">
			{#each cells as cell (cell.id)}
				<div class="cell">
					<div class="title">
						<span
							class="dot"
							style:background={cell.loading ? tones.decode : cell.sleeping ? tones.idle : tones.ok}
						></span>
						<span class="name ellipsis">{cell.id}</span>
						{#if cell.quant}<Tag>{cell.quant}</Tag>{/if}
						{#if cell.speculation}<Tag>{cell.speculation}</Tag>{/if}
						{#if !cell.loading}
							<span class="unload">
								<Button
									variant="ghost"
									size="xs"
									mono
									tone="muted"
									aria-label="Unload {cell.id}"
									disabled={!canAct || busy[cell.id]}
									onclick={() => run(cell.id, () => unloadModel(cell.id))}
								>
									unload
								</Button>
							</span>
						{/if}
					</div>
					{#if cell.loading}
						<div class="loading">
							<ProgressBar value={100} height={6} flow darkStripes label="Loading {cell.id}" />
							<span class="progress">
								Loading weights{cell.fileBytes ? ` · ${formatBytes(cell.fileBytes)}` : ''}
							</span>
						</div>
					{:else}
						<div class="stats">
							<span class="pips" aria-hidden="true">
								{#each [...Array(cell.slots).keys()] as i (i)}
									<span class="pip" style:background={i < cell.busy ? tones.decode : tones.empty}
									></span>
								{/each}
							</span>
							<span>{cell.sleeping ? 'sleeping' : `${cell.busy}/${cell.slots} busy`}</span>
							{#if cell.acceptance}<span>{cell.acceptance} accepted</span>{/if}
							<span class="usage">{formatBytes(cell.memory)} · {cell.role}</span>
						</div>
					{/if}
					{#if cell.error}<span class="error" role="alert">{cell.error}</span>{/if}
				</div>
			{/each}
			<div class="cell add">
				<Button
					variant="primary"
					size="xl"
					block
					aria-expanded={drawer}
					disabled={!canAct}
					onclick={() => (drawer = !drawer)}
				>
					<svg
						aria-hidden="true"
						width="16"
						height="16"
						viewBox="0 0 24 24"
						fill="none"
						stroke="currentColor"
						stroke-width="2.2"
						stroke-linecap="round"><path d="M12 5v14M5 12h14"></path></svg
					>Load model
				</Button>
				<span class="free">
					{formatBytes(free.bytes)}
					{free.kind} free · {shortcut} anywhere
				</span>
			</div>
		</div>
		{#if failure}<p class="error banner" role="alert">{failure}</p>{/if}
		{#if drawer}
			<div class="drawer">
				<div class="drawer-head">
					<h3>ON YOUR DISK · NOT LOADED</h3>
					<TextLink href={resolve('/library')}>Find more on Hugging Face →</TextLink>
				</div>
				{#each rows as row (row.key)}
					<div class="disk-row">
						<div class="disk-name">
							<span class="name">{row.name}</span>
							<span class="meta">{row.meta}</span>
						</div>
						<span class="profile">
							{#if row.profile}profile <code>{row.profile}</code>{:else}no profile yet{/if}
						</span>
						<span class="verdict" style:color={levelColor[row.level]}>{row.verdict}</span>
						<span class="action">{@render rowAction(row, 'md')}</span>
					</div>
				{:else}
					<p class="none">Everything on disk is loaded.</p>
				{/each}
			</div>
		{/if}
	</section>
{:else if local !== null && rows.length > 0}
	<section class="start" aria-labelledby="start-heading">
		<div class="start-text">
			<span class="eyebrow">GET STARTED</span>
			<h2 id="start-heading">No model is loaded</h2>
			<p>
				llama-server is running and waiting for a model. Load one from your disk, or find one on
				Hugging Face that fits your {formatBytes(free.bytes)} of free {free.kind}. Once it's loaded,
				this panel becomes the model strip, in the same place.
			</p>
			<div class="start-actions">
				<Button variant="primary" size="xl" href={resolve('/library')}>Browse the Library</Button>
			</div>
			<span class="meta">Or put a .gguf file in the models directory.</span>
			{#if failure}<span class="error" role="alert">{failure}</span>{/if}
		</div>
		<div class="start-list">
			<h3>READY ON YOUR DISK</h3>
			{#each rows as row (row.key)}
				<div class="start-row">
					<div class="disk-name">
						<span class="name">{row.name}</span>
						<span class="meta" style:color={levelColor[row.level]}>{row.verdict}</span>
					</div>
					<span class="action">{@render rowAction(row, 'lg')}</span>
				</div>
			{/each}
		</div>
	</section>
{/if}

<style>
	.dock,
	.start {
		background: var(--bg-panel);
		border: 1px solid var(--border-panel);
		border-radius: var(--radius-7);
	}

	.dock {
		display: flex;
		flex-direction: column;
		overflow: hidden;
	}

	header {
		flex-wrap: wrap;
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-10);
		padding: var(--space-12) var(--space-16);
		border-bottom: 1px solid var(--border-panel);
	}

	h2,
	h3 {
		margin: 0;
	}

	header h2,
	h3 {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		font-weight: 500;
		letter-spacing: var(--tracking-caps);
		color: var(--fg-muted);
	}

	.cells {
		display: flex;
		flex-wrap: wrap;
		gap: 1px;
		background: var(--border-panel);
	}

	.cell {
		flex: 1 1 240px;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-10);
		padding: 14px var(--space-16);
		background: var(--bg-panel);
	}

	.add {
		flex: 0 1 230px;
		min-width: 200px;
		justify-content: center;
		gap: var(--space-6);
		padding: var(--space-12) var(--space-16);
	}

	@media (max-width: 640px) {
		.add {
			flex: 1 1 100%;
		}
	}

	.add svg {
		flex: none;
	}

	.free {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		text-align: center;
		color: var(--fg-muted);
	}

	.title {
		display: flex;
		align-items: center;
		gap: var(--space-8);
		min-width: 0;
	}

	.dot {
		flex: none;
		width: 8px;
		height: 8px;
		border-radius: 50%;
	}

	.name {
		font-weight: 500;
	}

	.unload {
		flex: none;
		margin-left: auto;
	}

	.stats {
		display: flex;
		align-items: center;
		gap: var(--space-10);
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
	}

	.pips {
		display: flex;
		gap: 3px;
	}

	.pip {
		width: 7px;
		height: 12px;
		border-radius: var(--radius-1);
	}

	.usage {
		margin-left: auto;
		text-align: right;
	}

	.loading {
		display: flex;
		flex-direction: column;
		gap: var(--space-6);
	}

	.progress {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-secondary);
	}

	.error {
		font-size: var(--fs-12);
		color: var(--color-error-soft);
		overflow-wrap: anywhere;
	}

	.banner {
		margin: 0;
		padding: var(--space-10) var(--space-16);
		border-top: 1px solid var(--border-panel);
	}

	.drawer {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		padding: var(--space-16);
		border-top: 1px solid var(--border-panel);
		background: var(--bg-inset);
	}

	.drawer-head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-10);
		padding-bottom: var(--space-8);
	}

	.disk-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-10) 18px;
		padding: var(--space-10) 0;
		border-top: 1px solid var(--border-row);
	}

	.disk-name {
		flex: 1 1 260px;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 3px;
		overflow-wrap: anywhere;
	}

	.meta {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
	}

	.profile {
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}

	.profile code {
		font-family: var(--font-mono);
		color: var(--fg-secondary);
	}

	.verdict {
		flex: 1 1 220px;
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}

	.action {
		flex: none;
		min-width: 110px;
		display: flex;
		justify-content: flex-end;
	}

	.action :global(a),
	.action :global(button) {
		width: 100%;
	}

	.none {
		margin: 0;
		padding: var(--space-10) 0;
		border-top: 1px solid var(--border-row);
		font-size: var(--fs-13);
		color: var(--fg-muted);
	}

	.start {
		display: flex;
		flex-wrap: wrap;
		gap: 28px 40px;
		padding: 28px;
	}

	.start-text {
		flex: 1 1 340px;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 14px;
	}

	.eyebrow {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		letter-spacing: var(--tracking-caps);
		color: var(--color-decode);
	}

	.start h2 {
		font-size: 24px;
		font-weight: 600;
		letter-spacing: -0.015em;
	}

	.start p {
		margin: 0;
		font-size: var(--fs-14);
		line-height: 1.6;
		color: var(--fg-tertiary);
	}

	.start-actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-8);
		padding-top: var(--space-4);
	}

	.start-list {
		flex: 1.4 1 420px;
		min-width: 0;
		display: flex;
		flex-direction: column;
	}

	.start-list h3 {
		padding-bottom: var(--space-8);
	}

	.start-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-8) var(--space-16);
		padding: var(--space-12) 0;
		border-top: 1px solid var(--border-row);
	}

	.start-row .disk-name {
		flex-basis: 220px;
	}

	.start-row .action {
		min-width: 96px;
	}
</style>
