<script lang="ts">
	import {
		createProfile,
		listModels,
		listProfiles,
		loadModel,
		unloadModel,
		type Profile
	} from '$lib/api/client';
	import Button from '$lib/components/Button.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import SelectField from '$lib/components/SelectField.svelte';
	import Spinner from '$lib/components/Spinner.svelte';
	import TextField from '$lib/components/TextField.svelte';
	import { formatInt, formatMs } from '$lib/format';
	import { errorText } from '$lib/monitor/poll';
	import { speculationName } from '$lib/monitor/overview';
	import {
		acceptedShare,
		baselineOf,
		DEFAULT_MAX_TOKENS,
		DEFAULT_PROMPT,
		runComparison,
		verdict,
		type CompareApi,
		type CompareRun
	} from './compare';

	let profiles = $state.raw<Profile[]>([]);
	let first = $state('');
	let second = $state('');
	let prompt = $state(DEFAULT_PROMPT);
	let maxTokens = $state(String(DEFAULT_MAX_TOKENS));
	let step = $state('');
	let error = $state('');
	let runs = $state.raw<CompareRun[]>([]);
	let controller = $state.raw<AbortController | null>(null);

	async function refresh() {
		profiles = await listProfiles();
		const speculative = profiles.find((p) => p.spec_type);
		second ||= speculative?.alias ?? profiles[1]?.alias ?? '';
		first ||=
			profiles.find((p) => !p.spec_type && p.model_file === speculative?.model_file)?.alias ??
			profiles.find((p) => p.alias !== second)?.alias ??
			'';
	}

	$effect(() => {
		void refresh().catch((e: unknown) => (error = errorText(e)));
	});

	const running = $derived(controller !== null);
	const options = $derived(
		profiles.map((p) => ({
			value: p.alias,
			label: p.spec_type ? `${p.alias} · ${speculationName(p.spec_type)}` : p.alias
		}))
	);
	const picked = $derived(profiles.find((p) => p.alias === second));
	const baseline = $derived(picked ? baselineOf(picked, profiles) : undefined);
	const hasBaseline = $derived(
		profiles.some((p) => !p.spec_type && p.model_file === picked?.model_file)
	);
	const tokens = $derived(Number(maxTokens));
	const ready = $derived(
		!running && first && second && first !== second && prompt.trim() && tokens > 0
	);
	const summary = $derived(verdict(runs));

	async function makeBaseline() {
		if (!baseline) return;
		error = '';
		try {
			const made = await createProfile(baseline);
			await refresh();
			first = made.alias;
		} catch (e) {
			error = errorText(e);
		}
	}

	const api: CompareApi = {
		models: () => listModels(),
		load: loadModel,
		unload: unloadModel,
		chat: async (body, signal) => {
			const res = await fetch('/v1/chat/completions', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(body),
				signal
			});
			if (!res.ok) throw new Error(`The server answered ${res.status}: ${await res.text()}`);
			return res.json();
		},
		wait: (ms, signal) =>
			new Promise((done, fail) => {
				const timer = setTimeout(done, ms);
				signal.addEventListener('abort', () => {
					clearTimeout(timer);
					fail(signal.reason);
				});
			})
	};

	async function run() {
		const current = new AbortController();
		controller = current;
		error = '';
		runs = [];
		try {
			runs = await runComparison(
				api,
				[first, second],
				prompt.trim(),
				tokens,
				current.signal,
				(s) => {
					step = s;
				}
			);
		} catch (e) {
			if (!current.signal.aborted) error = errorText(e);
		} finally {
			controller = null;
			step = '';
		}
	}
</script>

<Panel gap={14} aria-labelledby="compare-heading">
	<SectionHeading
		id="compare-heading"
		title="COMPARE TWO PROFILES"
		subtitle="The same prompt through each, one at a time. This is how to measure speculative decoding, which llama-bench cannot run."
	/>
	{#if profiles.length === 0}
		<span class="note">Create a profile first.</span>
	{:else}
		<div class="choices">
			<SelectField label="First" mono {options} bind:value={first} disabled={running} />
			<SelectField label="Then" mono {options} bind:value={second} disabled={running} />
			<TextField
				label="Max tokens"
				inputmode="numeric"
				class="mono"
				size={38}
				disabled={running}
				bind:value={maxTokens}
			/>
		</div>
		<label class="prompt">
			Prompt
			<textarea rows="2" disabled={running} bind:value={prompt}></textarea>
		</label>
		{#if baseline && !hasBaseline}
			<div class="offer">
				<span class="note">{second} has nothing to compare against yet.</span>
				<Button variant="ghost" size="sm" disabled={running} onclick={makeBaseline}>
					Create {baseline.alias}
				</Button>
			</div>
		{/if}
		<div class="actions">
			{#if running}
				<Button variant="ghost" size="sm" onclick={() => controller?.abort()}>Cancel</Button>
				<span class="note step" role="status"><Spinner size={10} />{step}</span>
			{:else}
				<Button variant="primary" size="sm" disabled={!ready} onclick={run}>Compare</Button>
				<span class="note">
					Both are unloaded while it runs, and what was in memory comes back afterwards. Other
					models stay loaded.
				</span>
			{/if}
		</div>
		{#if error}<p class="error" role="alert">{error}</p>{/if}
		{#if runs.length > 0}
			{#if summary}<p class="verdict">{summary}</p>{/if}
			<table>
				<thead>
					<tr>
						<th>PROFILE</th><th>TOK/S</th><th>TOKENS</th><th>GENERATING</th><th>PROMPT</th>
						<th>DRAFTS KEPT</th>
					</tr>
				</thead>
				<tbody>
					{#each runs as r (r.alias)}
						<tr>
							<td>{r.alias}</td>
							<td>{r.tokensPerSecond.toFixed(1)}</td>
							<td>{formatInt(r.predicted)}</td>
							<td>{formatMs(r.predictedMs)}</td>
							<td>{formatMs(r.promptMs)}</td>
							<td>{acceptedShare(r)}</td>
						</tr>
					{/each}
				</tbody>
			</table>
			<span class="note">A single run on one prompt: try the kind of work you do with it.</span>
		{/if}
	{/if}
</Panel>

<style>
	.choices {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
		gap: var(--space-12);
	}

	.prompt {
		display: flex;
		flex-direction: column;
		gap: var(--space-6);
		font-size: var(--fs-12);
		color: var(--fg-tertiary);
	}

	textarea {
		box-sizing: border-box;
		width: 100%;
		resize: vertical;
		padding: var(--space-10);
		background: var(--bg-ground);
		border: 1px solid var(--border-control);
		border-radius: var(--radius-5);
		color: var(--fg-primary);
		font-family: var(--font-sans);
		font-size: var(--fs-13);
		line-height: 1.5;
	}

	.offer,
	.actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-8) var(--space-12);
	}

	.note {
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}

	.step {
		display: inline-flex;
		align-items: center;
		gap: var(--space-8);
		color: var(--color-decode);
	}

	.error {
		margin: 0;
		font-size: var(--fs-13);
		color: var(--color-error-soft);
		overflow-wrap: anywhere;
	}

	.verdict {
		margin: 0;
		font-size: var(--fs-16);
		font-weight: 500;
	}

	table {
		width: 100%;
		border-collapse: collapse;
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}

	th {
		padding-bottom: var(--space-8);
		font-size: var(--fs-10);
		font-weight: 400;
		letter-spacing: 0.06em;
		color: var(--fg-faint);
		text-align: right;
	}

	td {
		padding: var(--space-8) 0;
		border-top: 1px solid var(--border-row);
		text-align: right;
	}

	th:first-child,
	td:first-child {
		text-align: left;
		overflow-wrap: anywhere;
	}
</style>
