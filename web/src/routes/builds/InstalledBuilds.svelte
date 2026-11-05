<script lang="ts">
	import type { BuildManifest } from '$lib/api/client';
	import Button from '$lib/components/Button.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import Tag from '$lib/components/Tag.svelte';
	import { backendLabel } from '$lib/monitor/overview';
	import { installedMeta, rollbackId } from './view';

	interface Props {
		builds: BuildManifest[];
		/** The build a switch is under way to. */
		switching: string;
		/** Refuses switching and deleting while it is set. */
		busy: boolean;
		now: number;
		/** Why the last action on a build failed, by build id. */
		errors: Record<string, string>;
		onswitch: (id: string) => void;
		ondelete: (id: string) => void;
	}

	let { builds, switching, busy, now, errors, onswitch, ondelete }: Props = $props();

	let confirming = $state('');
	const rollback = $derived(rollbackId(builds));
</script>

<Panel flex="1 1 520px" gap={12} aria-labelledby="in-h">
	<SectionHeading id="in-h" title="INSTALLED BUILDS" />
	{#if builds.length === 0}
		<EmptyState
			title="No builds installed"
			detail="Pick a version and a backend above. The router needs a build before it can start."
		/>
	{:else}
		<ul class="list">
			{#each builds as b (b.id)}
				<li class="build">
					<span class="tag">{b.tag}</span>
					<span class="what">
						<span class="kind">
							Prebuilt · {backendLabel(b.backend)}{b.variant ? ` ${b.variant}` : ''} · {b.channel}
						</span>
						<span class="meta">{installedMeta(b, now, b.id === rollback)}</span>
						{#if errors[b.id]}<span class="error" role="alert">{errors[b.id]}</span>{/if}
					</span>
					<span class="actions">
						{#if b.active}
							<Tag tone="ok" size="md">ACTIVE</Tag>
						{:else if confirming === b.id}
							<Button
								size="sm"
								variant="danger"
								onclick={() => {
									confirming = '';
									ondelete(b.id);
								}}>Delete {b.tag}?</Button
							>
							<Button size="sm" variant="ghost" onclick={() => (confirming = '')}>Keep</Button>
						{:else}
							{#if b.supported === false}
								<Tag>unsupported: no router mode</Tag>
							{:else}
								<Button size="sm" variant="ghost" disabled={busy} onclick={() => onswitch(b.id)}>
									{switching === b.id ? 'Switching…' : 'Switch'}
								</Button>
							{/if}
							<Button
								size="sm"
								variant="ghost"
								class="danger"
								disabled={busy}
								onclick={() => (confirming = b.id)}>Delete</Button
							>
						{/if}
					</span>
				</li>
			{/each}
		</ul>
		<span class="hint"
			>Keep the previous build: if a new release regresses, roll back in one click.</span
		>
	{/if}
</Panel>

<style>
	.list {
		display: flex;
		flex-direction: column;
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.build {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-8) var(--space-14);
		padding: var(--space-12) 0;
		border-top: 1px solid var(--border-row);
	}

	.tag {
		min-width: 80px;
		font-family: var(--font-mono);
		font-size: var(--fs-15);
	}

	.what {
		flex: 1 1 200px;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
	}

	.kind {
		font-size: var(--fs-13);
	}

	.meta {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-6);
	}

	.actions :global(.danger) {
		color: var(--color-error-soft);
	}

	.hint {
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}

	.error {
		font-size: var(--fs-12);
		color: var(--color-error-soft);
		overflow-wrap: anywhere;
	}
</style>
