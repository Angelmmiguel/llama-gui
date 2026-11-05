<script lang="ts">
	import { resolve } from '$app/paths';
	import { SvelteMap } from 'svelte/reactivity';
	import { page } from '$app/state';
	import {
		ApiError,
		createProfile,
		deleteProfile,
		estimateProfile,
		getHardware,
		listBuilds,
		listLocalModelFiles,
		listModels,
		listProfilesDetail,
		loadModel,
		previewProfile,
		unloadModel,
		updateProfile,
		type BuildManifest,
		type Hardware,
		type LocalModelFile,
		type Profile,
		type ProfileEstimate,
		type RouterModel,
		type SkippedFlag
	} from '$lib/api/client';
	import Button from '$lib/components/Button.svelte';
	import CommandBlock from '$lib/components/CommandBlock.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import Spinner from '$lib/components/Spinner.svelte';
	import StatusDot from '$lib/components/StatusDot.svelte';
	import TextLink from '$lib/components/TextLink.svelte';
	import { errorText, every } from '$lib/monitor/poll';
	import { server } from '$lib/state/server.svelte';
	import MemoryCard from './MemoryCard.svelte';
	import ProfileForm from './ProfileForm.svelte';
	import {
		countChanges,
		fromProfile,
		listMeta,
		listStatus,
		mmprojOf,
		newForm,
		parseCommandLine,
		quantOf,
		resolveModelPath,
		toProfile,
		unsupportedFields,
		type Form
	} from './view';

	const MODELS_MS = 3000;
	const ESTIMATE_MS = 10_000;
	const DEBOUNCE_MS = 250;
	const WAIT_STEP_MS = 500;
	const WAIT_LIMIT_MS = 180_000;
	const NEW_ID = 'new';

	let profiles = $state.raw<Profile[]>([]);
	let skipped = $state.raw<SkippedFlag[]>([]);
	let models = $state.raw<RouterModel[]>([]);
	let local = $state.raw<LocalModelFile[] | null>(null);
	let builds = $state.raw<BuildManifest[]>([]);
	let activeId = $state('');
	let hardware = $state.raw<Hardware | null>(null);
	let ready = $state(false);
	let loadError = $state('');

	let selected = $state('');
	let form = $state<Form | null>(null);
	let baseline = $state.raw<Form | null>(null);
	// Edits survive looking at another profile.
	const drafts = new SvelteMap<string, Form>();

	let command = $state('');
	let validation = $state('');
	let estimate = $state.raw<ProfileEstimate | null>(null);
	let estimateReason = $state('Choose a model file and a context size.');
	let layers = $state(0);

	type Phase = 'idle' | 'saving' | 'unloading' | 'loading' | 'deleting';
	let phase = $state<Phase>('idle');
	let actionError = $state('');
	let actionDone = $state('');
	let confirmingDelete = $state(false);

	let importing = $state(false);
	let importText = $state('');
	let importNotes = $state.raw<string[]>([]);

	const routerUp = $derived(server.state === 'running');
	const activeBuild = $derived(builds.find((b) => b.id === activeId));
	const buildTag = $derived(activeBuild?.tag ?? '');
	const unsupported = $derived(unsupportedFields(activeBuild?.probe.flags));
	const cpuOnly = $derived(
		activeBuild?.backend === 'cpu' ||
			(hardware !== null && hardware.gpus.length === 0 && hardware.render_nodes.length === 0)
	);
	const draft = $derived(form ? toProfile(form) : null);
	const changes = $derived(form && baseline ? countChanges(form, baseline) : 0);
	const isNew = $derived(selected === NEW_ID);
	const dirty = $derived(isNew || changes > 0);
	const busy = $derived(phase !== 'idle');

	const modelById = (id: string) => models.find((m) => m.id === id);
	const isLoaded = (id: string) => {
		const s = modelById(id)?.status;
		return s === 'loaded' || s === 'sleeping' || s === 'loading';
	};
	const savedAlias = $derived(baseline?.alias ?? '');
	const willReload = $derived(!isNew && routerUp && isLoaded(savedAlias));

	const extraError = $derived(validation.includes('extra_args') ? cleanError(validation) : '');
	const otherError = $derived(validation && !extraError ? cleanError(validation) : '');
	const blockers = $derived([...(draft?.errors ?? []), ...(otherError ? [otherError] : [])]);
	const profileSkips = $derived(skipped.filter((s) => s.alias === savedAlias && !isNew));

	const dirtyNote = $derived.by(() => {
		if (isNew) return 'New profile, not saved yet';
		if (changes === 0) return 'No changes';
		const count = `${changes} unsaved change${changes > 1 ? 's' : ''}`;
		return willReload ? `${count} · the model reloads when you save` : count;
	});
	const phaseText = $derived.by(() => {
		switch (phase) {
			case 'saving':
				return 'Saving';
			case 'unloading':
				return `Unloading ${savedAlias}`;
			case 'loading':
				return `Loading ${savedAlias}`;
			case 'deleting':
				return 'Deleting';
		}
		return '';
	});

	function cleanError(message: string): string {
		return message.replace(/^invalid profile: /, '').replace(/^extra_args: /, '');
	}

	function select(id: string, next: Form) {
		if (selected && form && selected !== id && dirty) drafts.set(selected, $state.snapshot(form));
		selected = id;
		form = drafts.get(id) ?? next;
		drafts.delete(id);
		actionError = '';
		actionDone = '';
		confirmingDelete = false;
		estimate = null;
		layers = 0;
	}

	function pick(p: Profile) {
		if (p.id === selected) return;
		baseline = fromProfile(p);
		select(p.id, fromProfile(p));
	}

	function startNew(modelFile = '') {
		const next = newForm(modelFile);
		if (modelFile && local) next.mmproj = mmprojOf(modelFile, local);
		baseline = next;
		select(NEW_ID, next);
	}

	async function refreshProfiles(): Promise<Profile[]> {
		const out = await listProfilesDetail();
		profiles = out.profiles;
		skipped = out.skipped;
		return out.profiles;
	}

	$effect(() => {
		void (async () => {
			const [p, l, b, h] = await Promise.allSettled([
				refreshProfiles(),
				listLocalModelFiles(),
				listBuilds(),
				getHardware()
			]);
			if (p.status === 'rejected') loadError = errorText(p.reason);
			// A 404 is a server without the Library: the form then takes a typed path.
			local = l.status === 'fulfilled' ? l.value : null;
			if (b.status === 'fulfilled') {
				builds = b.value.builds;
				activeId = b.value.active;
			}
			if (h.status === 'fulfilled') hardware = h.value;
			ready = true;
			const wanted = page.url.searchParams.get('new');
			if (wanted !== null) startNew(resolveModelPath(wanted, local ?? []));
			else if (profiles.length > 0) pick(profiles[0]);
		})();
	});

	$effect(() =>
		every(MODELS_MS, async (signal) => {
			const list = await listModels().catch(() => null);
			if (list && !signal.aborted) models = list;
		})
	);

	// Both come from the API, so what is shown cannot drift from the Go renderer.
	$effect(() => {
		const body = draft?.profile;
		if (!body) return;
		let stale = false;
		const timer = setTimeout(async () => {
			try {
				const out = await previewProfile(body);
				if (stale) return;
				command = out.command;
				validation = out.error ?? '';
			} catch (e) {
				if (!stale) validation = errorText(e);
			}
		}, DEBOUNCE_MS);
		return () => {
			stale = true;
			clearTimeout(timer);
		};
	});

	$effect(() => {
		const body = draft?.profile;
		if (!body) return;
		if (!body.model_file || body.ctx_size <= 0) {
			estimate = null;
			estimateReason = 'Choose a model file and a context size.';
			return;
		}
		let stop: (() => void) | undefined;
		const timer = setTimeout(() => {
			// Repeated because other models come and go while the form is open.
			stop = every(ESTIMATE_MS, async (signal) => {
				try {
					const out = await estimateProfile(body);
					if (signal.aborted) return;
					estimate = out;
					layers = out.layers;
				} catch (e) {
					if (signal.aborted) return;
					estimate = null;
					estimateReason = errorText(e);
				}
			});
		}, DEBOUNCE_MS);
		return () => {
			clearTimeout(timer);
			stop?.();
		};
	});

	async function waitForModel(
		alias: string,
		done: (m: RouterModel | undefined) => boolean
	): Promise<void> {
		const deadline = Date.now() + WAIT_LIMIT_MS;
		while (Date.now() < deadline) {
			const list = await listModels();
			models = list;
			const m = list.find((x) => x.id === alias);
			if (m?.failed) {
				throw new Error(
					`${alias} failed to load${m.exit_code ? ` (exit code ${m.exit_code})` : ''}: see Logs`
				);
			}
			if (done(m)) return;
			await new Promise((r) => setTimeout(r, WAIT_STEP_MS));
		}
		throw new Error(`Timed out waiting for ${alias}`);
	}

	async function unload(alias: string) {
		phase = 'unloading';
		await unloadModel(alias);
		await waitForModel(alias, (m) => !m || m.status === 'unloaded');
	}

	async function save() {
		if (!form || !draft || busy) return;
		actionError = '';
		actionDone = '';
		const oldAlias = savedAlias;
		const body = draft.profile;
		const reload = willReload;
		phase = 'saving';
		try {
			// A renamed model's old name is gone once the preset is rewritten.
			if (reload && body.alias !== oldAlias) await unload(oldAlias);
			phase = 'saving';
			const out = isNew ? await createProfile(body) : await updateProfile(body.id, body);
			await refreshProfiles();
			baseline = fromProfile(out);
			form = fromProfile(out);
			selected = out.id;
			if (!reload) {
				actionDone = routerUp ? 'Saved.' : 'Saved. The router picks it up when it starts.';
				return;
			}
			const started = Date.now();
			if (body.alias === oldAlias) await unload(body.alias);
			phase = 'loading';
			await loadModel(body.alias);
			await waitForModel(body.alias, (m) => m?.status === 'loaded');
			actionDone = `Saved and reloaded in ${Math.round((Date.now() - started) / 1000)} s.`;
		} catch (e) {
			actionError = errorText(e);
		} finally {
			phase = 'idle';
		}
	}

	function revert() {
		if (baseline) form = { ...baseline };
		actionError = '';
		actionDone = '';
	}

	async function remove() {
		if (!form?.id) return;
		const alias = savedAlias;
		const id = form.id;
		actionError = '';
		phase = 'deleting';
		try {
			if (isLoaded(alias)) await unload(alias);
			phase = 'deleting';
			await deleteProfile(id);
			const left = await refreshProfiles();
			confirmingDelete = false;
			drafts.delete(id);
			selected = '';
			form = null;
			baseline = null;
			if (left.length > 0) pick(left[0]);
		} catch (e) {
			actionError = e instanceof ApiError ? e.message : errorText(e);
		} finally {
			phase = 'idle';
		}
	}

	function applyImport() {
		const { patch, extra, notes } = parseCommandLine(importText);
		if (!form) startNew();
		if (!form) return;
		Object.assign(form, patch, { extra: extra.join(' ') });
		importNotes = notes;
		if (notes.length === 0) importing = false;
	}

	function renameAlongAlias() {
		if (form && baseline && baseline.name === baseline.alias) form.name = form.alias;
	}
	$effect(renameAlongAlias);
</script>

<PageHeader
	title="Profiles"
	subtitle="How each model is launched: context, slots, GPU layers, KV cache and defaults"
>
	{#snippet actions()}
		<Button onclick={() => (importing = !importing)} aria-expanded={importing}>
			Import from command line
		</Button>
	{/snippet}
</PageHeader>

{#if importing}
	<Panel tone="inset" aria-label="Import from command line" gap={10}>
		<label class="import-label" for="import-text">
			Paste a llama-server command. Known flags fill the form below, anything else goes to the extra
			arguments.
		</label>
		<textarea id="import-text" rows="4" spellcheck="false" bind:value={importText}></textarea>
		{#each importNotes as note (note)}<span class="warn">{note}</span>{/each}
		<div class="buttons">
			<Button size="sm" disabled={!importText.trim()} onclick={applyImport}>Fill the form</Button>
			<Button size="sm" variant="ghost" onclick={() => (importing = false)}>Close</Button>
		</div>
	</Panel>
{/if}

<div class="layout">
	<Panel as="nav" flex="1 1 230px" padding="10px" gap={4} aria-label="Profiles">
		{#each profiles as p (p.id)}
			{@const status = listStatus(p, modelById(p.alias), routerUp)}
			<button
				type="button"
				class="item"
				class:current={selected === p.id}
				aria-current={selected === p.id ? 'page' : undefined}
				onclick={() => pick(p)}
			>
				<span class="item-name">
					<StatusDot color={status.color} size={7} />
					<span>{p.name}</span>
					{#if drafts.has(p.id)}<span class="edited">edited</span>{/if}
				</span>
				<span class="item-meta">{listMeta(p, quantOf(p.model_file, local ?? []), status.word)}</span
				>
			</button>
		{/each}
		{#if isNew}
			<div class="item current" aria-current="page">
				<span class="item-name">{form?.alias || 'New profile'}</span>
				<span class="item-meta">not saved</span>
			</div>
		{/if}
		<Button size="xl" variant="ghost" dashed block onclick={() => startNew()}>+ New profile</Button>
	</Panel>

	{#if !ready}
		<div class="fill"><Panel><EmptyState title="Loading profiles" /></Panel></div>
	{:else if loadError}
		<div class="fill">
			<Panel><EmptyState title="Could not read the profiles" detail={loadError} /></Panel>
		</div>
	{:else if !form}
		<div class="fill">
			<Panel>
				<EmptyState
					title="No profiles yet"
					detail="A profile says how to launch a model. The router only offers models that have one."
				>
					{#snippet actions()}
						<Button size="sm" onclick={() => startNew()}>+ New profile</Button>
						<TextLink href={resolve('/library')}>Find a model in the Library</TextLink>
					{/snippet}
				</EmptyState>
			</Panel>
		</div>
	{:else}
		<ProfileForm bind:form {local} {layers} {unsupported} {buildTag} {cpuOnly} {extraError} />

		<aside class="side" aria-label="What this profile does">
			<MemoryCard {estimate} reason={estimateReason} ctk={form.ctk} ctv={form.ctv} />

			<Panel gap={12} aria-labelledby="cmd-h">
				<CommandBlock text={command} label="RESULTING COMMAND" headingId="cmd-h" copy />
			</Panel>

			{#if !activeBuild}
				<span class="note">
					No build installed, so flag support cannot be checked yet.
					<TextLink href={resolve('/builds')}>Install one</TextLink>
				</span>
			{/if}
			{#each profileSkips as s (`${s.alias}/${s.flag}`)}
				<span class="warn">
					{s.flag
						? `--${s.flag} is left out of the preset: ${s.reason}`
						: `Left out of the preset: ${s.reason}`}
				</span>
			{/each}

			<div class="actions">
				{#if busy}
					<span class="progress" role="status"><Spinner />{phaseText}…</span>
				{:else if actionDone && changes === 0}
					<span class="ok" role="status">{actionDone}</span>
				{:else}
					<span class="note" class:dirty={changes > 0}>{dirtyNote}</span>
				{/if}
				{#each blockers as blocker (blocker)}<span class="error" role="alert">{blocker}</span
					>{/each}
				{#if actionError}<span class="error" role="alert">{actionError}</span>{/if}
				<div class="buttons">
					<Button
						variant="primary"
						size="xl"
						fill
						disabled={busy || !dirty || blockers.length > 0 || extraError !== ''}
						onclick={save}
					>
						{isNew ? 'Create profile' : willReload ? 'Save & reload model' : 'Save'}
					</Button>
					{#if !isNew}
						<Button variant="ghost" size="xl" disabled={busy || changes === 0} onclick={revert}>
							Revert
						</Button>
					{/if}
				</div>
				{#if !isNew}
					{#if confirmingDelete}
						<div class="confirm" role="group" aria-label="Confirm deleting">
							<span class="note">
								Delete {savedAlias}? {isLoaded(savedAlias) ? 'It is unloaded first. ' : ''}The model
								file stays on disk.
							</span>
							<div class="buttons">
								<Button variant="danger" size="sm" disabled={busy} onclick={remove}>
									Delete profile
								</Button>
								<Button variant="ghost" size="sm" onclick={() => (confirmingDelete = false)}>
									Keep it
								</Button>
							</div>
						</div>
					{:else}
						<div>
							<Button
								variant="danger"
								size="sm"
								disabled={busy}
								onclick={() => (confirmingDelete = true)}
							>
								Delete
							</Button>
						</div>
					{/if}
				{/if}
			</div>
		</aside>
	{/if}
</div>

<style>
	.layout {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-start;
		gap: var(--space-16);
	}

	.fill {
		flex: 4 1 560px;
		min-width: 0;
	}

	.side {
		flex: 1.4 1 340px;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-16);
	}

	.item {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: var(--space-4);
		width: 100%;
		min-height: 52px;
		padding: var(--space-10) var(--space-12);
		border: none;
		border-radius: var(--radius-5);
		background: transparent;
		color: var(--fg-primary);
		font: inherit;
		font-size: var(--fs-13);
		text-align: left;
	}

	.current {
		background: var(--bg-selected);
		box-shadow: inset 2px 0 0 var(--color-decode);
	}

	.item-name {
		display: flex;
		align-items: center;
		gap: var(--space-8);
		font-weight: 500;
		overflow-wrap: anywhere;
	}

	.item-meta {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
		overflow-wrap: anywhere;
	}

	.edited {
		font-size: var(--fs-11);
		font-weight: 400;
		color: var(--color-decode);
	}

	.actions {
		display: flex;
		flex-direction: column;
		gap: var(--space-8);
	}

	.buttons {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-8);
	}

	.confirm {
		display: flex;
		flex-direction: column;
		gap: var(--space-8);
	}

	.progress {
		display: flex;
		align-items: center;
		gap: var(--space-8);
		font-size: var(--fs-12);
		color: var(--color-decode);
	}

	.note {
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}

	.dirty {
		color: var(--color-decode);
	}

	.ok {
		font-size: var(--fs-12);
		color: var(--color-ok);
	}

	.warn {
		font-size: var(--fs-12);
		color: var(--color-warn);
	}

	.error {
		font-size: var(--fs-12);
		color: var(--color-error-soft);
		overflow-wrap: anywhere;
	}

	.import-label {
		font-size: var(--fs-12);
		color: var(--fg-tertiary);
	}

	textarea {
		width: 100%;
		box-sizing: border-box;
		padding: var(--space-10);
		background: var(--bg-ground);
		border: 1px solid var(--border-control);
		border-radius: var(--radius-5);
		color: var(--fg-primary);
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		resize: vertical;
	}
</style>
