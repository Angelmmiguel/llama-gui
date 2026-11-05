<script lang="ts">
	import { resolve } from '$app/paths';
	import {
		loadModel,
		unloadModel,
		type Gpu,
		type Profile,
		type RouterModel,
		type TelemetryModel
	} from '$lib/api/client';
	import Button from '$lib/components/Button.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import Spinner from '$lib/components/Spinner.svelte';
	import Tag from '$lib/components/Tag.svelte';
	import TextLink from '$lib/components/TextLink.svelte';
	import { tones } from '$lib/components/tones';
	import { modelMeta, quantOf, sortedModels } from './overview';
	import { errorText } from './poll';

	interface Props {
		models: readonly TelemetryModel[];
		estimates: readonly RouterModel[];
		profiles: readonly Profile[];
		gpus: readonly Gpu[];
		/** Seconds since each model's last finished request, when recent. */
		lastRequest: ReadonlyMap<string, number>;
		/** Why there are no models, for the empty state. */
		empty: { title: string; detail?: string };
		/** False while the router cannot take the action. */
		canAct: boolean;
	}

	let { models, estimates, profiles, gpus, lastRequest, empty, canAct }: Props = $props();

	let pending = $state<Record<string, boolean>>({});
	let errors = $state<Record<string, string>>({});

	const rows = $derived(
		sortedModels(models).map((m) => {
			const profile = profiles.find((p) => p.alias === m.id);
			const busy = m.slots.filter((s) => s.state !== 'idle').length;
			return {
				model: m,
				quant: quantOf(profile?.model_file),
				meta: modelMeta({
					model: m,
					estimate: estimates.find((e) => e.id === m.id),
					profile,
					gpus,
					lastRequestAgo: lastRequest.get(m.id)
				}),
				busy,
				// Busy slots first, so the pips read as a level.
				pips: m.slots.map((_, i) => i < busy)
			};
		})
	);

	async function act(id: string, action: (id: string) => Promise<unknown>) {
		pending[id] = true;
		delete errors[id];
		try {
			await action(id);
		} catch (e) {
			errors[id] = errorText(e);
		} finally {
			delete pending[id];
		}
	}
</script>

<Panel flex="3 1 420px" gap={12} aria-labelledby="models-heading">
	<SectionHeading id="models-heading" title="ROUTER · MODELS">
		{#snippet actions()}<TextLink href={resolve('/library')}>Library →</TextLink>{/snippet}
	</SectionHeading>
	{#if rows.length === 0}
		<EmptyState title={empty.title} detail={empty.detail} />
	{:else}
		<div class="list">
			{#each rows as row (row.model.id)}
				{@const m = row.model}
				{@const loaded = m.status === 'loaded'}
				{@const moving = m.status === 'loading' || pending[m.id]}
				<div class="row">
					<div class="text">
						<div class="title">
							<span class="name ellipsis">{m.id}</span>
							{#if row.quant}<Tag>{row.quant}</Tag>{/if}
						</div>
						{#if row.meta}<span class="meta">{row.meta}</span>{/if}
						{#if errors[m.id] || m.error}
							<span class="error" role="alert">{errors[m.id] ?? m.error}</span>
						{/if}
					</div>
					{#if row.pips.length > 0}
						<div class="pips" role="img" aria-label="{row.busy} of {row.pips.length} slots busy">
							{#each row.pips as on, i (i)}
								<span class="pip" style:background={on ? tones.decode : tones.empty}></span>
							{/each}
						</div>
					{/if}
					<Button
						variant="ghost"
						size="sm"
						mono
						tone={loaded ? 'muted' : 'accent'}
						disabled={moving || !canAct}
						onclick={() => act(m.id, loaded ? unloadModel : loadModel)}
					>
						{#if m.status === 'loading'}<Spinner />{/if}
						{m.status === 'loading' ? 'loading' : loaded ? 'unload' : 'load'}
					</Button>
				</div>
			{/each}
		</div>
	{/if}
</Panel>

<style>
	.list {
		display: flex;
		flex-direction: column;
	}

	.row {
		display: flex;
		align-items: center;
		gap: var(--space-12);
		padding: var(--space-12) 0;
		border-top: 1px solid var(--border-row);
	}

	.row :global(button) {
		flex: none;
		min-width: 64px;
	}

	.text {
		flex: 1 1 auto;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.title {
		display: flex;
		align-items: center;
		gap: var(--space-8);
		min-width: 0;
	}

	.name {
		font-weight: 500;
	}

	.meta {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
	}

	.error {
		font-size: var(--fs-12);
		color: var(--color-error-soft);
	}

	.pips {
		display: flex;
		gap: 3px;
	}

	.pip {
		width: 8px;
		height: 14px;
		border-radius: var(--radius-1);
	}
</style>
