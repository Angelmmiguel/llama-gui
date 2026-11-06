<script lang="ts">
	import { resolve } from '$app/paths';
	import { ApiError, getRequest, listRequests, slotAction, type SlotAction } from '$lib/api/client';
	import Button from '$lib/components/Button.svelte';
	import Chip from '$lib/components/Chip.svelte';
	import Columns from '$lib/components/Columns.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import Eyebrow from '$lib/components/Eyebrow.svelte';
	import Legend from '$lib/components/Legend.svelte';
	import LiveToggle from '$lib/components/LiveToggle.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import ScrollX from '$lib/components/ScrollX.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import SelectCard from '$lib/components/SelectCard.svelte';
	import StateLabel from '$lib/components/StateLabel.svelte';
	import StatGrid from '$lib/components/StatGrid.svelte';
	import TextLink from '$lib/components/TextLink.svelte';
	import { tones } from '$lib/components/tones';
	import { formatInt } from '$lib/format';
	import {
		allSlots,
		prefillPercent,
		slotFill,
		stateTone,
		type ModelSlot
	} from '$lib/monitor/overview';
	import { errorText } from '$lib/monitor/poll';
	import {
		busyPercent,
		contextCells,
		headCell,
		heatCells,
		MAP_CELLS,
		peakSpeed,
		promptText,
		samplingParams,
		slotFilename
	} from '$lib/monitor/slots';
	import { server } from '$lib/state/server.svelte';
	import { telemetry } from '$lib/state/telemetry.svelte';

	const keyOf = (s: { model: string; id: number }) => `${s.model}/${s.id}`;

	let picked = $state<string | null>(null);
	let working = $state(false);
	let notice = $state<{ text: string; failed: boolean } | null>(null);
	let saveUnsupported = $state(false);
	let prompt = $state<{ key: string; text: string } | null>(null);

	$effect(() => telemetry.subscribe());

	const snap = $derived(telemetry.latest);
	const routerUp = $derived(snap?.server.state === 'running');
	const slots = $derived(snap ? allSlots(snap.models) : []);
	const modelCount = $derived(new Set(slots.map((s) => s.model)).size);
	const capture = $derived(server.status?.capture === 'bodies');

	const selected = $derived(slots.find((s) => keyOf(s) === picked) ?? slots[0]);

	const subtitle = $derived.by(() => {
		if (slots.length === 0) return undefined;
		const sizes = new Set(slots.map((s) => s.n_ctx));
		const context =
			sizes.size === 1
				? `${formatInt(slots[0].n_ctx)} tokens of context each`
				: `${formatInt(Math.min(...sizes))} to ${formatInt(Math.max(...sizes))} tokens of context each`;
		const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;
		return `${plural(slots.length, 'slot')} across ${plural(modelCount, 'model')} · ${context}`;
	});

	const empty = $derived.by(() => {
		if (!snap) return { title: 'Connecting to telemetry' };
		if (server.status && !server.status.active_build) {
			return {
				title: 'No llama.cpp build yet',
				detail: 'Slots exist while models are loaded, which takes a build first.'
			};
		}
		if (!routerUp) {
			return {
				title: `The router is ${snap.server.state}`,
				detail: 'Slots exist while models are loaded. Start the router from the Overview.'
			};
		}
		if (snap.models.every((m) => m.status !== 'loaded')) {
			return { title: 'No models loaded', detail: 'Load a model from the Overview to get slots.' };
		}
		return { title: 'No slots', detail: 'The loaded models report no slots yet.' };
	});

	// Seconds, from the sampler's clock: the key that lets each lane scroll.
	const tick = $derived(snap ? Math.round(Date.parse(snap.time) / 1000) : 0);
	const history = $derived(snap?.slot_history ?? []);
	const peak = $derived(peakSpeed(history));
	const lanes = $derived(
		slots.map((s) => ({
			slot: s,
			cells: heatCells(
				history.find((h) => h.model === s.model && h.slot === s.id)?.samples ?? [],
				tick,
				peak
			)
		}))
	);
	const busyPct = $derived(busyPercent(history));

	// Uniform slots can say how many tokens a square is; mixed ones can only say the share.
	const tokensPerCell = $derived.by(() => {
		const sizes = new Set(slots.map((s) => s.n_ctx));
		return sizes.size === 1 ? Math.round(slots[0].n_ctx / MAP_CELLS) : null;
	});

	function speedLabel(s: ModelSlot): string {
		if (s.state === 'decode') return `${s.speed.toFixed(1)} tok/s`;
		if (s.state === 'prefill') return `${prefillPercent(s)}% prefill`;
		return s.total > 0 ? 'cached' : 'empty';
	}

	const sampling = $derived(samplingParams(selected?.params));
	const stats = $derived.by(() => {
		const s = selected;
		if (!s) return [];
		const fill = slotFill(s);
		return [
			{ label: 'n_past', value: formatInt(s.total) },
			{ label: 'context free', value: formatInt(Math.max(0, s.n_ctx - s.total)) },
			{ label: 'cached prefix', value: formatInt(fill.cached) },
			{ label: 'prompt tokens', value: formatInt(fill.prompt) },
			{ label: 'generated', value: formatInt(fill.generated) },
			{ label: 'speed', value: s.state === 'decode' ? `${s.speed.toFixed(1)} t/s` : '—' }
		];
	});

	// A request is stored when it ends, so look again as the slot changes state, and go by slot
	// rather than task id, as the running task has no row yet: this shows the latest finished.
	const selectedKey = $derived(selected ? keyOf(selected) : null);
	const selectedModel = $derived(selected?.model);
	const selectedId = $derived(selected?.id);
	const selectedTask = $derived(selected?.task);
	const selectedState = $derived(selected?.state);

	$effect(() => {
		const key = selectedKey;
		const task = selectedTask;
		const model = selectedModel;
		const id = selectedId;
		if (!capture || key === null || model === undefined) return;
		void task;
		void selectedState;
		let stale = false;
		void (async () => {
			try {
				const { requests } = await listRequests({ model, limit: 50 });
				const latest = requests.find((r) => r.slot === id);
				const text = latest ? (await getRequest(latest.id)).body?.request : undefined;
				if (!stale) prompt = text ? { key, text: promptText(text) } : null;
			} catch {
				if (!stale) prompt = null;
			}
		})();
		return () => {
			stale = true;
		};
	});

	const shownPrompt = $derived(prompt?.key === selectedKey ? prompt?.text : undefined);

	/** Runs an action, and notes when the router has no `--slot-save-path` to act on. */
	async function run(s: ModelSlot, action: SlotAction): Promise<void> {
		const file =
			action === 'save' || action === 'restore' ? slotFilename(s.model, s.id) : undefined;
		try {
			await slotAction(s.model, s.id, action, file);
		} catch (e) {
			if (e instanceof ApiError && e.status === 501) saveUnsupported = true;
			throw e;
		}
	}

	const DONE: Record<SlotAction, string> = {
		save: 'Saved to',
		restore: 'Restored from',
		erase: 'Erased the cache of',
		cancel: 'Cancelled the task on'
	};

	async function act(s: ModelSlot, action: SlotAction) {
		working = true;
		notice = null;
		try {
			await run(s, action);
			const target =
				action === 'save' || action === 'restore'
					? slotFilename(s.model, s.id)
					: `slot ${s.id} of ${s.model}`;
			notice = { text: `${DONE[action]} ${target}.`, failed: false };
		} catch (e) {
			notice = { text: `${action}: ${errorText(e)}`, failed: true };
		} finally {
			working = false;
		}
	}

	async function actAll(targets: ModelSlot[], action: SlotAction, done: string) {
		working = true;
		notice = null;
		const results = await Promise.allSettled(targets.map((s) => run(s, action)));
		const failures = results.filter((r) => r.status === 'rejected');
		if (failures.length > 0) {
			notice = {
				text: `${failures.length} of ${targets.length} failed: ${errorText(failures[0].reason)}`,
				failed: true
			};
		} else {
			notice = {
				text: `${done} ${targets.length} slot${targets.length === 1 ? '' : 's'}.`,
				failed: false
			};
		}
		working = false;
	}

	const idleSlots = $derived(slots.filter((s) => s.state === 'idle' && s.total > 0));
	const selectedBusy = $derived(selected !== undefined && selected.state !== 'idle');

	const stateLegend = [
		{ label: 'Prefill', color: tones.prefill },
		{ label: 'Decode', color: tones.decode },
		{ label: 'Idle', color: 'var(--track-idle)', shape: 'filled-outline' as const }
	];
	const mapLegend = [
		{ label: 'Cached', color: tones.cached },
		{ label: 'Prompt', color: tones.prefill },
		{ label: 'Generated', color: tones.decode }
	];
</script>

<PageHeader title="Slots" {subtitle}>
	{#snippet actions()}
		<Button
			disabled={working || !routerUp || slots.length === 0 || saveUnsupported}
			onclick={() => actAll(slots, 'save', 'Saved')}
		>
			Save all to disk
		</Button>
		<Button
			disabled={working || idleSlots.length === 0}
			onclick={() => actAll(idleSlots, 'erase', 'Erased')}
		>
			Erase idle slots
		</Button>
		<LiveToggle bind:live={telemetry.live} />
	{/snippet}
</PageHeader>

{#if notice}
	<p class={['notice', { failed: notice.failed }]} role={notice.failed ? 'alert' : 'status'}>
		{notice.text}
	</p>
{/if}

{#if slots.length === 0}
	<Panel>
		<EmptyState title={empty.title} detail={empty.detail}>
			{#snippet actions()}
				{#if snap}<TextLink href={resolve('/')}>Overview →</TextLink>{/if}
			{/snippet}
		</EmptyState>
	</Panel>
{:else}
	<Panel gap={14} aria-labelledby="heatmap-heading">
		<SectionHeading
			id="heatmap-heading"
			title="SLOT ACTIVITY · LAST 90 s"
			subtitle="One cell per second. Brighter amber = faster decoding. Scrolls left as time passes."
		>
			{#snippet actions()}<Legend items={stateLegend} />{/snippet}
		</SectionHeading>
		<ScrollX minWidth={900} gap={5}>
			{#each lanes as lane (keyOf(lane.slot))}
				<div class="lane">
					<span class="lane-name ellipsis">
						slot {lane.slot.id} <span class="muted">{lane.slot.model}</span>
					</span>
					<div class="heat">
						{#each lane.cells as cell (cell.key)}
							<div class="heat-cell {cell.state}" style:opacity={cell.opacity}></div>
						{/each}
					</div>
					<span class="lane-state" style:color={stateTone(lane.slot.state)}>{lane.slot.state}</span>
				</div>
			{/each}
			<div class="lane foot" aria-hidden="true">
				<span>busy {busyPct}% of last 90 s</span>
				<div class="axis"><span>−90s</span><span>−60s</span><span>−30s</span><span>now</span></div>
				<span></span>
			</div>
		</ScrollX>
	</Panel>

	<Columns>
		<Panel flex="3 1 700px" gap={14} aria-labelledby="maps-heading">
			<SectionHeading
				id="maps-heading"
				title="CONTEXT MAPS"
				subtitle={tokensPerCell
					? `Each square ≈ ${tokensPerCell} tokens of the slot's KV cache, filled in order. Select a slot to inspect it.`
					: `Each square is 1/${MAP_CELLS} of the slot's KV cache, filled in order. Select a slot to inspect it.`}
			>
				{#snippet actions()}<Legend items={mapLegend} />{/snippet}
			</SectionHeading>
			<div class="cards">
				{#each slots as s (keyOf(s))}
					{@const head = s.state === 'decode' ? headCell(s.total, s.n_ctx) : -1}
					<SelectCard
						selected={selected && keyOf(selected) === keyOf(s)}
						onclick={() => (picked = keyOf(s))}
					>
						<span class="card-head">
							<span class="card-title">
								<span class="card-id">slot {s.id}</span>
								<span class="card-model ellipsis">{s.model}</span>
							</span>
							<StateLabel color={stateTone(s.state)}>{s.state}</StateLabel>
						</span>
						<span class="map" aria-hidden="true">
							{#each contextCells(slotFill(s), s.n_ctx) as kind, i (i)}
								<span class={['map-cell', kind, { blink: i === head }]}></span>
							{/each}
						</span>
						<span class="card-foot">
							<span>n_past <span class="bright">{formatInt(s.total)}</span></span>
							<span>{speedLabel(s)}</span>
						</span>
					</SelectCard>
				{/each}
			</div>
		</Panel>

		{#if selected}
			<Panel as="aside" flex="1 1 340px" gap={16} aria-labelledby="detail-heading">
				<div class="detail-head">
					<Eyebrow>SELECTED SLOT</Eyebrow>
					<h2 id="detail-heading">Slot {selected.id} · {selected.model}</h2>
					<StateLabel color={stateTone(selected.state)}>
						{selected.state}{selected.task === null ? '' : ` · task ${selected.task}`}
					</StateLabel>
				</div>
				<StatGrid items={stats} columns={2} valueSize={15} />
				<div class="block">
					<Eyebrow>SAMPLING</Eyebrow>
					{#if sampling.length > 0}
						<div class="chips">
							{#each sampling as p (p.k)}<Chip k={p.k} v={p.v} />{/each}
						</div>
					{:else}
						<span class="hint">Known once this slot has run a task.</span>
					{/if}
				</div>
				<div class="block">
					<Eyebrow>LATEST PROMPT</Eyebrow>
					{#if !capture}
						<span class="hint"
							>Prompts are not kept: set <code>LLAMA_GUI_CAPTURE=bodies</code> to see them.</span
						>
					{:else if shownPrompt}
						<div class="prompt">{shownPrompt}</div>
					{:else}
						<span class="hint">No captured request on this slot yet.</span>
					{/if}
				</div>
				<div class="buttons">
					<Button
						variant="ghost"
						size="lg"
						block
						disabled={working || saveUnsupported}
						onclick={() => act(selected, 'save')}
					>
						Save KV to file
					</Button>
					<Button
						variant="ghost"
						size="lg"
						block
						disabled={working || saveUnsupported || selectedBusy}
						onclick={() => act(selected, 'restore')}
					>
						Restore from file
					</Button>
					<Button
						variant="ghost"
						size="lg"
						block
						disabled={working || selectedBusy}
						onclick={() => act(selected, 'erase')}
					>
						Erase cache
					</Button>
					<Button
						variant="danger"
						size="lg"
						block
						disabled={working || !selectedBusy}
						onclick={() => act(selected, 'cancel')}
					>
						Cancel task
					</Button>
				</div>
				<span class="hint">
					{#if saveUnsupported}
						Save and restore are off: the router runs without <code>--slot-save-path</code>.
					{:else}
						Save and restore use <code>{slotFilename(selected.model, selected.id)}</code> in the slot
						save directory.
					{/if}
				</span>
			</Panel>
		{/if}
	</Columns>
{/if}

<style>
	code {
		font-family: var(--font-mono);
	}

	.notice {
		font-size: var(--fs-13);
		color: var(--fg-secondary);
	}

	.failed {
		color: var(--color-error-soft);
	}

	.lane {
		display: grid;
		grid-template-columns: 170px minmax(0, 1fr) 110px;
		align-items: center;
		gap: var(--space-14);
	}

	.lane-name {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}

	.muted {
		color: var(--fg-muted);
	}

	/* A tick swaps one cell per lane: containment keeps that from reaching the page's layout. */
	.heat {
		display: flex;
		gap: var(--space-2);
		height: 22px;
		contain: strict;
	}

	.heat-cell {
		flex: 1 1 0;
		min-width: 0;
		border-radius: var(--radius-1);
		background: var(--track-idle);
	}

	.heat-cell.prefill {
		background: var(--color-prefill);
	}

	.heat-cell.decode {
		background: var(--color-decode);
	}

	.lane-state {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		text-align: right;
	}

	.foot {
		padding-top: var(--space-4);
		align-items: start;
		font-family: var(--font-mono);
		font-size: var(--fs-10);
		color: var(--fg-faint);
	}

	.axis {
		display: flex;
		justify-content: space-between;
	}

	.cards {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(min(280px, 100%), 1fr));
		gap: var(--space-12);
	}

	.card-head,
	.card-foot {
		display: flex;
		justify-content: space-between;
		gap: var(--space-8);
		width: 100%;
	}

	.card-head {
		align-items: center;
	}

	.card-title {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: var(--space-2);
		min-width: 0;
	}

	.card-id {
		font-family: var(--font-mono);
		font-size: var(--fs-13);
		color: var(--fg-primary);
	}

	.card-model {
		max-width: 100%;
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}

	.map {
		display: grid;
		grid-template-columns: repeat(16, minmax(0, 1fr));
		gap: var(--space-2);
		width: 100%;
	}

	.map-cell {
		height: 11px;
		border-radius: var(--radius-1);
		background: var(--track-idle);
	}

	.map-cell.cached {
		background: var(--color-cached);
	}

	.map-cell.prompt {
		background: var(--color-prefill);
	}

	.map-cell.generated {
		background: var(--color-decode);
	}

	.card-foot {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
	}

	.bright {
		color: var(--fg-primary);
	}

	.detail-head,
	.block {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: var(--space-6);
	}

	.block {
		gap: var(--space-8);
		align-items: stretch;
	}

	h2 {
		font-size: var(--fs-18);
		font-weight: 600;
		overflow-wrap: anywhere;
	}

	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-6);
	}

	.prompt {
		max-height: 240px;
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

	.buttons {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--space-8);
	}

	.hint {
		line-height: 1.5;
	}
</style>
