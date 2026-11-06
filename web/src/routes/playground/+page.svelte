<script lang="ts">
	import { tick } from 'svelte';
	import { resolve } from '$app/paths';
	import {
		getLibraryLocal,
		getModelProps,
		listModels,
		listProfiles,
		loadModel,
		type LocalModel,
		type Profile,
		type RouterModel
	} from '$lib/api/client';
	import Button from '$lib/components/Button.svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import Eyebrow from '$lib/components/Eyebrow.svelte';
	import ProgressBar from '$lib/components/ProgressBar.svelte';
	import Segmented from '$lib/components/Segmented.svelte';
	import SelectField from '$lib/components/SelectField.svelte';
	import StackedBar from '$lib/components/StackedBar.svelte';
	import Switch from '$lib/components/Switch.svelte';
	import TextField from '$lib/components/TextField.svelte';
	import TextLink from '$lib/components/TextLink.svelte';
	import { tones } from '$lib/components/tones';
	import { formatInt } from '$lib/format';
	import { diskRows, dockCells, freeMemory, roleOf, type DiskRow } from '$lib/monitor/dock';
	import { quantOf } from '$lib/monitor/overview';
	import { errorText, every } from '$lib/monitor/poll';
	import {
		cacheView,
		curlFor,
		delayBars,
		NUMBER_FIELDS,
		reasoningEfforts,
		speedOf,
		statsLine,
		timingsGrid,
		tokensOf,
		withProfileDefaults
	} from '$lib/playground/chat';
	import { playground as chat } from '$lib/state/playground.svelte';
	import { telemetry } from '$lib/state/telemetry.svelte';

	const CONFIG_MS = 10_000;
	const STARTERS = [
		'Write a haiku about GPUs',
		'Explain the KV cache in two lines',
		'Return {"ok": true} as JSON'
	];
	const levelColor = { ok: tones.muted, warn: tones.warn, error: tones.errorSoft };

	let profiles = $state.raw<Profile[]>([]);
	let estimates = $state.raw<RouterModel[]>([]);
	let local = $state.raw<LocalModel[]>([]);
	let headroom = $state(0);
	let view = $state<'text' | 'tokens'>('text');
	let tab = $state<'timings' | 'request' | 'raw'>('timings');
	let loadError = $state('');
	let requested = $state.raw<ReadonlySet<string>>(new Set());
	let thread = $state<HTMLElement>();
	// What the selected model's template takes, null when it takes none or isn't known yet.
	let efforts = $state.raw<string[] | null>(null);

	$effect(() => telemetry.subscribe());

	$effect(() =>
		every(CONFIG_MS, async (signal) => {
			const [p, m, l] = await Promise.all([
				listProfiles().catch(() => null),
				listModels().catch(() => null),
				getLibraryLocal({}, signal).catch(() => null)
			]);
			if (signal.aborted) return;
			if (p) profiles = p;
			if (m) estimates = m;
			if (l) {
				local = l.models;
				headroom = l.hardware.headroom_bytes;
			}
		})
	);

	const snap = $derived(telemetry.latest);
	const models = $derived(snap?.models ?? []);
	const ready = $derived(models.filter((m) => m.status === 'loaded' || m.status === 'sleeping'));
	const selected = $derived(ready.find((m) => m.id === chat.model));

	// A model asked for here is the one to chat with once it's up, otherwise keep to what still runs.
	$effect(() => {
		if (chat.streaming) return;
		const wanted = ready.find((m) => requested.has(m.id));
		if (wanted) {
			chat.model = wanted.id;
			requested = new Set();
		} else if (!selected) {
			chat.model = ready[0]?.id ?? '';
		}
	});

	const selectedId = $derived(selected?.id);
	$effect(() => {
		const id = selectedId;
		efforts = null;
		if (!id) {
			chat.settings.reasoningEffort = '';
			return;
		}
		const controller = new AbortController();
		getModelProps(id, controller.signal)
			.then(reasoningEfforts)
			.catch(() => null)
			.then((levels) => {
				if (controller.signal.aborted) return;
				efforts = levels;
				if (!levels?.includes(chat.settings.reasoningEffort)) chat.settings.reasoningEffort = '';
			});
		return () => controller.abort();
	});

	const loading = $derived(dockCells(models, profiles, local).filter((c) => c.loading));
	const rows = $derived(
		diskRows({
			models,
			estimates,
			profiles,
			local,
			free: freeMemory(snap?.gpus ?? [], snap?.ram.available ?? 0),
			headroom,
			pending: requested
		}).filter((r) => r.load)
	);

	const profileOf = (id: string) => profiles.find((p) => p.alias === id);
	function modelMeta(id: string, slots: number) {
		const p = profileOf(id);
		const perSlot = slots > 0 ? models.find((m) => m.id === id)?.slots[0]?.n_ctx : undefined;
		return [
			quantOf(p?.model_file),
			`${slots} slot${slots === 1 ? '' : 's'}`,
			perSlot ? `${formatInt(perSlot)} each` : undefined,
			roleOf(p) === 'chat' ? undefined : roleOf(p)
		]
			.filter(Boolean)
			.join(' · ');
	}

	const headerMeta = $derived.by(() => {
		if (!selected) return 'waiting for a model';
		const temp = chat.settings.temperature;
		return [
			quantOf(profileOf(selected.id)?.model_file),
			chat.settings.stream ? 'streaming' : 'non-streaming',
			temp ? `temp ${Number(temp).toFixed(1)}` : 'profile sampling'
		]
			.filter(Boolean)
			.join(' · ');
	});

	const slotOptions = $derived([
		{ value: '', label: 'any free slot' },
		...(selected?.slots ?? []).map((s) => ({ value: String(s.id), label: `slot ${s.id}` }))
	]);

	const last = $derived(chat.messages.findLast((m) => m.role === 'assistant'));
	const grid = $derived(timingsGrid(last));
	const cache = $derived(cacheView(last));
	const bars = $derived(delayBars(last?.gaps));
	const tokensUsed = $derived(
		last?.timings
			? (last.timings.cache_n ?? 0) + (last.timings.prompt_n ?? 0) + (last.timings.predicted_n ?? 0)
			: 0
	);
	const ctxSize = $derived(selected?.slots[0]?.n_ctx ?? 0);
	const requestJson = $derived(chat.lastRequest ? JSON.stringify(chat.lastRequest, null, 2) : '');
	const rawJson = $derived(last?.raw ? JSON.stringify(last.raw, null, 2) : '// no response yet');
	const curl = $derived(chat.lastRequest ? curlFor(location.origin, chat.lastRequest) : '');
	const temperature = $derived(chat.settings.temperature || '0.8');

	// Stay at the newest token, unless someone has scrolled up to read.
	$effect(() => {
		void chat.messages;
		const el = thread;
		if (!el || el.scrollHeight - el.scrollTop - el.clientHeight > 120) return;
		void tick().then(() => el.scrollTo({ top: el.scrollHeight }));
	});

	async function load(row: DiskRow) {
		const id = row.load;
		if (!id) return;
		loadError = '';
		requested = new Set([...requested, id]);
		try {
			await loadModel(id);
		} catch (e) {
			loadError = errorText(e);
			requested = new Set([...requested].filter((r) => r !== id));
		}
	}

	function onkeydown(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			chat.send();
		}
	}
</script>

<div class="playground">
	<aside class="pane settings" aria-labelledby="settings-heading">
		<h2 id="settings-heading">REQUEST SETTINGS</h2>

		<div class="field">
			<span class="label" id="model-label">Model</span>
			{#if ready.length > 0}
				<div class="models" role="radiogroup" aria-labelledby="model-label">
					{#each ready as m (m.id)}
						<button
							type="button"
							role="radio"
							aria-checked={chat.model === m.id}
							class={['model', { on: chat.model === m.id }]}
							disabled={chat.streaming}
							onclick={() => (chat.model = m.id)}
						>
							<span class="model-name"><span class="dot"></span>{m.id}</span>
							<span class="mono-note">{modelMeta(m.id, m.slots.length)}</span>
						</button>
					{/each}
				</div>
			{:else}
				<p class="none">No model loaded. Pick one in the chat area to start.</p>
			{/if}
		</div>

		<SelectField
			label="Slot"
			flag="id_slot"
			mono
			options={slotOptions}
			bind:value={chat.settings.slot}
		/>

		{#if efforts}
			<SelectField
				label="Reasoning effort"
				flag="reasoning_effort"
				mono
				options={[
					{ value: '', label: 'template default' },
					...efforts.map((value) => ({ value, label: value }))
				]}
				bind:value={chat.settings.reasoningEffort}
			/>
		{/if}

		<label class="field label">
			System prompt
			<textarea rows="4" bind:value={chat.settings.system}></textarea>
		</label>

		<div class="sampling">
			<div class="sampling-head">
				<span class="eyebrow">SAMPLING</span>
				<button
					type="button"
					class="reset"
					onclick={() => (chat.settings = withProfileDefaults(chat.settings))}
				>
					Use profile defaults
				</button>
			</div>
			<label class="field label">
				<span class="spread">
					<span>Temperature</span>
					<span class="value">{chat.settings.temperature || 'profile'}</span>
				</span>
				<input
					type="range"
					min="0"
					max="2"
					step="0.1"
					value={temperature}
					oninput={(e) => (chat.settings.temperature = e.currentTarget.value)}
				/>
			</label>
			<div class="numbers">
				{#each NUMBER_FIELDS as key (key)}
					<TextField
						label={key}
						inputmode="decimal"
						placeholder="profile"
						class="mono"
						bind:value={chat.settings[key]}
					/>
				{/each}
			</div>
		</div>

		<div class="switches">
			<Switch label="Stream tokens" flag="stream" bind:checked={chat.settings.stream} />
			<Switch
				label="Reuse prompt cache"
				flag="cache_prompt"
				bind:checked={chat.settings.cachePrompt}
			/>
		</div>
	</aside>

	<section class="chat" aria-label="Chat">
		<header>
			<div class="title">
				<h1>Playground</h1>
				<span class="meta">{chat.model ? `${chat.model} · ` : ''}{headerMeta}</span>
			</div>
			<div class="header-actions">
				<Segmented
					label="Message view"
					bind:value={view}
					options={[
						{ value: 'text', label: 'Text' },
						{ value: 'tokens', label: 'Tokens' }
					]}
				/>
				<Button size="md" disabled={chat.streaming} onclick={() => chat.clear()}>New chat</Button>
			</div>
		</header>

		{#if !selected}
			<div class="empty">
				<div class="empty-inner">
					<div class="empty-text">
						<Eyebrow color={tones.decode}>NO MODEL LOADED</Eyebrow>
						<h2>Load a model to start chatting</h2>
						<p>
							It loads with its saved profile, the same as from the Overview, and stays loaded for
							the rest of the server.
						</p>
					</div>
					{#if loading.length > 0 || rows.length > 0}
						<div class="load-list">
							{#each loading as cell (cell.id)}
								<div class="load-row">
									<div class="load-name">
										<span class="model-name">{cell.id}</span>
										<ProgressBar
											value={100}
											height={6}
											flow
											darkStripes
											label="Loading {cell.id}"
										/>
										<span class="mono-note">Loading weights</span>
									</div>
								</div>
							{/each}
							{#each rows as row (row.key)}
								<div class="load-row">
									<div class="load-name">
										<span class="model-name">{row.name}</span>
										<span class="mono-note">{row.meta}</span>
										<span class="mono-note" style:color={levelColor[row.level]}>{row.verdict}</span>
									</div>
									<Button
										variant="ghost"
										size="lg"
										tone="accent"
										disabled={row.disabled}
										onclick={() => load(row)}
									>
										Load
									</Button>
								</div>
							{/each}
						</div>
					{/if}
					{#if loadError}<p class="error" role="alert">{loadError}</p>{/if}
					<TextLink href={resolve('/library')}>Nothing here? Find a model in the Library →</TextLink
					>
				</div>
			</div>
		{:else}
			<div class="thread" bind:this={thread} aria-live="off">
				{#if chat.messages.length === 0}
					<div class="welcome">
						<span class="welcome-title">Ask {selected.id} anything</span>
						<span class="welcome-note">
							Every reply shows llama.cpp's timings, so you can check speed and caching while you
							test.
						</span>
						<div class="starters">
							{#each STARTERS as text (text)}
								<button type="button" class="starter" onclick={() => chat.send(text)}>{text}</button
								>
							{/each}
						</div>
					</div>
				{/if}
				{#each chat.messages as m, i (i)}
					{#if m.role === 'user'}
						<div class="message mine">
							<span class="who">you</span>
							<div class="bubble">{m.text}</div>
						</div>
					{:else}
						{@const live = !m.done}
						<div class="message">
							<span class="who">{m.model}</span>
							{#if m.reasoning}
								<details class="thinking">
									<summary>
										{m.thoughtMs === undefined
											? 'Thinking…'
											: `Thought for ${(m.thoughtMs / 1000).toFixed(1)} s`}
									</summary>
									<div class="thinking-text">{m.reasoning}</div>
								</details>
							{/if}
							{#if view === 'tokens' && m.chunks?.length}
								<div class="reply">
									{#each m.chunks as piece, j (j)}<span class={['token', j % 2 ? 'odd' : 'even']}
											>{piece}</span
										>{/each}
								</div>
							{:else if m.text || live}
								<div class="reply">
									{m.text}{#if live}<span class="blink cursor">▍</span>{/if}
								</div>
							{/if}
							{#if m.error}<p class="error" role="alert">{m.error}</p>{/if}
							{#if live}
								<span class="live">
									{m.chunks?.length ? 'streaming' : 'waiting for the first token'} · {formatInt(
										tokensOf(m)
									)} tokens{speedOf(m) > 0 ? ` · ${speedOf(m).toFixed(1)} tok/s` : ''}
								</span>
							{:else if !m.error}
								<div class="stats">
									<span>{statsLine(m)}</span>
									<CopyButton text={m.text} label="Copy" size="xs" variant="ghost" />
									<Button
										variant="ghost"
										size="xs"
										disabled={chat.streaming}
										onclick={() => chat.regenerate(i)}
									>
										Regenerate
									</Button>
								</div>
							{:else}
								<div class="stats">
									<Button
										variant="ghost"
										size="xs"
										disabled={chat.streaming}
										onclick={() => chat.regenerate(i)}
									>
										Try again
									</Button>
								</div>
							{/if}
						</div>
					{/if}
				{/each}
			</div>

			<div class="composer-wrap">
				<div class="composer">
					<label class="visually-hidden" for="composer">Message</label>
					<textarea
						id="composer"
						rows="3"
						placeholder="Message {selected.id} · Enter to send, Shift+Enter for a new line"
						bind:value={chat.draft}
						{onkeydown}></textarea>
					<div class="composer-foot">
						<span class="mono-note">
							{#if tokensUsed && ctxSize}
								{formatInt(tokensUsed)} / {formatInt(ctxSize)} tokens in this chat
							{/if}
						</span>
						{#if chat.streaming}
							<Button variant="danger" size="ml" onclick={() => chat.stop()}>Stop</Button>
						{:else}
							<Button
								variant="primary"
								size="ml"
								disabled={!chat.draft.trim()}
								onclick={() => chat.send()}
							>
								Send
							</Button>
						{/if}
					</div>
				</div>
			</div>
		{/if}
	</section>

	<aside class="pane inspector" aria-labelledby="inspector-heading">
		<h2 id="inspector-heading">LAST RESPONSE</h2>
		<Segmented
			label="Inspector"
			surface="ground"
			bind:value={tab}
			options={[
				{ value: 'timings', label: 'Timings' },
				{ value: 'request', label: 'Request' },
				{ value: 'raw', label: 'Raw' }
			]}
		/>

		{#if tab === 'timings'}
			<div class="grid">
				{#each grid as t (t.k)}
					<div class="cell">
						<span class="key">{t.k}</span>
						<span class="number">{t.v}</span>
					</div>
				{/each}
			</div>
			<div class="block">
				<span class="eyebrow small">PROMPT TOKENS · CACHED vs NEW</span>
				<StackedBar
					height={10}
					label="Share of the prompt that came from the cache"
					segments={[
						{ value: cache.cached, color: tones.cached },
						{ value: cache.fresh, color: tones.prefill }
					]}
				/>
				<span class="note">{cache.note}</span>
			</div>
			<div class="block">
				<span class="eyebrow small">DELAY BEFORE EACH TOKEN</span>
				<div class="bars" role="img" aria-label="Delay before each token of the last response">
					{#each bars as bar, i (i)}
						<span
							class="bar"
							style:height="{bar.height}%"
							style:background={bar.spike ? tones.prefill : tones.decode}
						></span>
					{/each}
				</div>
				{#if bars.length === 0}<span class="note">Shown for streamed replies.</span>{/if}
			</div>
		{:else if tab === 'request'}
			<span class="note">POST /v1/chat/completions</span>
			<pre>{requestJson || '// nothing sent yet'}</pre>
			{#if curl}<CopyButton text={curl} label="Copy as cURL" size="lg" />{/if}
		{:else}
			<span class="note">
				{chat.settings.stream ? 'Final chunk of the stream' : 'The reply'}, as the server sent it
			</span>
			<pre>{rawJson}</pre>
		{/if}
	</aside>
</div>

<style>
	/* Full bleed: panes meet the page edges, separated by hairlines. */
	.playground {
		display: flex;
		flex-wrap: wrap;
		gap: 1px;
		margin: calc(-1 * var(--space-24)) calc(-1 * var(--space-24)) calc(-1 * var(--space-48));
		min-height: calc(100vh - 61px);
		background: var(--border-panel);
	}

	.pane {
		min-width: 0;
		max-width: 100%;
		box-sizing: border-box;
		display: flex;
		flex-direction: column;
		padding: var(--space-20);
		background: var(--bg-inset);
	}

	@media (max-width: 900px) {
		.chat {
			order: -1;
		}
	}

	.settings {
		flex: 1 1 280px;
		gap: 18px;
	}

	.inspector {
		flex: 1.2 1 320px;
		gap: var(--space-16);
	}

	.chat {
		flex: 3 1 560px;
		min-width: 0;
		min-height: 640px;
		display: flex;
		flex-direction: column;
		background: var(--bg-ground);
	}

	h1,
	h2,
	p {
		margin: 0;
	}

	.pane h2,
	.eyebrow {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		font-weight: 500;
		letter-spacing: var(--tracking-caps);
		color: var(--fg-muted);
	}

	.eyebrow.small {
		font-size: var(--fs-10);
		letter-spacing: 0.06em;
	}

	.field {
		display: flex;
		flex-direction: column;
		gap: var(--space-6);
	}

	.label {
		font-size: var(--fs-12);
		color: var(--fg-tertiary);
	}

	.models {
		display: flex;
		flex-direction: column;
		gap: var(--space-6);
	}

	.model {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 3px;
		min-height: 52px;
		padding: var(--space-8) var(--space-12);
		border: 1px solid var(--border-control);
		border-radius: var(--radius-5);
		background: transparent;
		color: var(--fg-primary);
		font-size: var(--fs-13);
		text-align: left;
		cursor: pointer;
		overflow-wrap: anywhere;
	}

	.model.on {
		background: var(--bg-selected);
		border-color: var(--color-decode);
	}

	.model:disabled {
		cursor: not-allowed;
	}

	.model-name {
		display: flex;
		align-items: center;
		gap: var(--space-8);
		font-weight: 500;
		overflow-wrap: anywhere;
	}

	.dot {
		flex: none;
		width: 7px;
		height: 7px;
		border-radius: var(--radius-round);
		background: var(--color-ok);
	}

	.mono-note {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
	}

	.none {
		padding: var(--space-12);
		border: 1px dashed var(--border-ghost);
		border-radius: var(--radius-5);
		font-size: var(--fs-13);
		color: var(--fg-tertiary);
	}

	textarea {
		box-sizing: border-box;
		width: 100%;
		color: var(--fg-primary);
		font-family: var(--font-sans);
	}

	.settings textarea {
		resize: vertical;
		padding: var(--space-10);
		background: var(--bg-ground);
		border: 1px solid var(--border-control);
		border-radius: var(--radius-5);
		font-size: var(--fs-13);
		line-height: 1.5;
	}

	.sampling {
		display: flex;
		flex-direction: column;
		gap: var(--space-12);
	}

	.sampling-head,
	.spread {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: var(--space-8);
	}

	.reset {
		min-height: 28px;
		padding: 0 var(--space-8);
		border: none;
		background: transparent;
		color: var(--color-decode);
		font-size: var(--fs-12);
		cursor: pointer;
	}

	.value {
		font-family: var(--font-mono);
		color: var(--fg-primary);
	}

	input[type='range'] {
		width: 100%;
		accent-color: var(--color-decode);
	}

	.numbers {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--space-10);
	}

	.switches {
		display: flex;
		flex-direction: column;
		gap: var(--space-8);
	}

	header {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-10);
		padding: 14px var(--space-24);
		border-bottom: 1px solid var(--border-panel);
	}

	.title {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	h1 {
		font-size: 18px;
		font-weight: 600;
	}

	.meta {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		color: var(--fg-muted);
		overflow-wrap: anywhere;
	}

	.header-actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-8);
	}

	.empty {
		flex: 1;
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 32px var(--space-24);
	}

	.empty-inner {
		width: 100%;
		max-width: 560px;
		display: flex;
		flex-direction: column;
		gap: 18px;
	}

	.empty-text {
		display: flex;
		flex-direction: column;
		gap: var(--space-8);
	}

	.empty h2 {
		font-size: 22px;
		font-weight: 600;
	}

	.empty p {
		font-size: var(--fs-14);
		line-height: 1.6;
		color: var(--fg-tertiary);
	}

	.load-list {
		display: flex;
		flex-direction: column;
		padding: var(--space-4) var(--space-16);
		background: var(--bg-panel);
		border: 1px solid var(--border-panel);
		border-radius: var(--radius-7);
	}

	.load-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-8) var(--space-16);
		padding: var(--space-12) 0;
		border-top: 1px solid var(--border-row);
	}

	.load-row:first-child {
		border-top: none;
	}

	.load-name {
		flex: 1 1 220px;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 3px;
	}

	.thread {
		flex: 1;
		min-height: 360px;
		max-height: calc(100vh - 260px);
		overflow-y: auto;
		display: flex;
		flex-direction: column;
		gap: 22px;
		padding: var(--space-24);
	}

	.welcome {
		margin: auto;
		max-width: 440px;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: var(--space-12);
		text-align: center;
	}

	.welcome-title {
		font-size: var(--fs-16);
		font-weight: 500;
	}

	.welcome-note {
		font-size: var(--fs-13);
		color: var(--fg-muted);
	}

	.starters {
		display: flex;
		flex-wrap: wrap;
		justify-content: center;
		gap: var(--space-6);
	}

	.starter {
		min-height: 36px;
		padding: 0 var(--space-12);
		border: 1px solid var(--border-ghost);
		border-radius: 18px;
		background: transparent;
		color: var(--fg-secondary);
		font-size: var(--fs-12);
		cursor: pointer;
	}

	.message {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: var(--space-6);
		max-width: 92%;
	}

	.mine {
		align-items: flex-end;
		align-self: flex-end;
		max-width: 76%;
	}

	.who {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-faint);
	}

	.bubble {
		padding: var(--space-10) 14px;
		border-radius: 12px 12px 4px 12px;
		background: var(--bg-selected);
		font-size: var(--fs-14);
		line-height: 1.55;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}

	.reply {
		font-size: var(--fs-14);
		line-height: 1.65;
		color: var(--fg-primary);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}

	.cursor {
		color: var(--color-decode);
	}

	.token {
		border-radius: var(--radius-2);
	}

	.even {
		background: color-mix(in srgb, var(--color-decode) 16%, transparent);
	}

	.odd {
		background: color-mix(in srgb, var(--color-prefill) 16%, transparent);
	}

	.thinking {
		max-width: 100%;
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}

	.thinking summary {
		width: fit-content;
		min-height: 28px;
		line-height: 28px;
		cursor: pointer;
	}

	.thinking summary:hover {
		color: var(--fg-secondary);
	}

	.thinking-text {
		margin-top: var(--space-4);
		padding-left: var(--space-10);
		border-left: 2px solid var(--border-ghost);
		line-height: 1.55;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}

	.stats {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-6) 14px;
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
	}

	.live {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--color-decode);
	}

	.error {
		font-size: var(--fs-13);
		color: var(--color-error-soft);
		overflow-wrap: anywhere;
	}

	.composer-wrap {
		padding: 14px var(--space-24) var(--space-20);
		border-top: 1px solid var(--border-panel);
	}

	.composer {
		display: flex;
		flex-direction: column;
		gap: var(--space-8);
		padding: var(--space-12);
		background: var(--bg-panel);
		border: 1px solid var(--border-control);
		border-radius: var(--radius-7);
	}

	.composer textarea {
		resize: none;
		border: none;
		outline: none;
		background: transparent;
		font-size: var(--fs-14);
		line-height: 1.5;
	}

	.composer-foot {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-8);
	}

	.visually-hidden {
		position: absolute;
		width: 1px;
		height: 1px;
		overflow: hidden;
		clip: rect(0 0 0 0);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 1px;
		background: var(--border-panel);
		border: 1px solid var(--border-panel);
		border-radius: var(--radius-5);
		overflow: hidden;
	}

	.cell {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
		padding: var(--space-10) var(--space-12);
		background: var(--bg-panel);
		font-family: var(--font-mono);
	}

	.key {
		font-size: var(--fs-10);
		color: var(--fg-muted);
	}

	.number {
		font-size: 15px;
		overflow-wrap: anywhere;
	}

	.block {
		display: flex;
		flex-direction: column;
		gap: var(--space-6);
	}

	.note {
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}

	.bars {
		display: flex;
		align-items: flex-end;
		gap: 1px;
		height: 64px;
		border-bottom: 1px solid var(--border-raised);
	}

	.bar {
		flex: 1 1 0;
		min-width: 0;
	}

	pre {
		margin: 0;
		max-height: 520px;
		overflow: auto;
		padding: var(--space-12);
		background: var(--bg-deep);
		border: 1px solid var(--border-panel);
		border-radius: var(--radius-5);
		font-family: var(--font-mono);
		font-size: 11.5px;
		line-height: 1.6;
		color: var(--fg-secondary);
		white-space: pre-wrap;
		word-break: break-word;
	}
</style>
