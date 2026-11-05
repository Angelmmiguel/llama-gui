<script lang="ts">
	import { resolve } from '$app/paths';
	import type { Profile } from '$lib/api/client';
	import Chip from '$lib/components/Chip.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import TextLink from '$lib/components/TextLink.svelte';
	import { formatTokens } from '$lib/format';
	import { profileGroups } from './overview';

	/** Undefined when there are no profiles. */
	let { profile }: { profile: Profile | undefined } = $props();

	const groups = $derived(profile ? profileGroups(profile) : []);
	const note = $derived.by(() => {
		const parallel = profile?.parallel ?? 0;
		const split =
			profile && parallel > 1
				? ` ${formatTokens(profile.ctx_size / parallel)} context per slot = ${profile.ctx_size} ÷ ${parallel}.`
				: '';
		return `Changes apply when the model reloads.${split}`;
	});
</script>

<Panel flex="2 1 340px" gap={12} aria-labelledby="profile-heading">
	<SectionHeading
		id="profile-heading"
		title={profile ? `LAUNCH PROFILE · ${profile.alias}` : 'LAUNCH PROFILE'}
	>
		{#snippet actions()}<TextLink href={resolve('/profiles')}>Edit →</TextLink>{/snippet}
	</SectionHeading>
	{#if !profile}
		<EmptyState title="No profiles yet" detail="A profile says how a model is launched." />
	{:else}
		<dl>
			{#each groups as g (g.group)}
				<div class="group">
					<dt>{g.group}</dt>
					<dd>
						{#each g.flags as f (f.k)}<Chip k={f.k} v={f.v} />{/each}
					</dd>
				</div>
			{/each}
		</dl>
		<span class="foot hint">{note}</span>
	{/if}
</Panel>

<style>
	dl {
		display: flex;
		flex-direction: column;
	}

	.group {
		display: grid;
		grid-template-columns: 76px minmax(0, 1fr);
		gap: var(--space-10);
		align-items: start;
		padding: 9px 0;
		border-top: 1px solid var(--border-row);
	}

	dt {
		font-size: var(--fs-12);
		color: var(--fg-muted);
		padding-top: 3px;
	}

	dd {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-6);
	}

	.foot {
		margin-top: auto;
	}
</style>
