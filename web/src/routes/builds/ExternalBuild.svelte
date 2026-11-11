<script lang="ts">
	import type { BuildManifest } from '$lib/api/client';
	import Eyebrow from '$lib/components/Eyebrow.svelte';
	import Metric from '$lib/components/Metric.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import { externalMeta } from './view';

	interface Props {
		build: BuildManifest & { path: string };
		routerState: string;
	}

	let { build, routerState }: Props = $props();
</script>

<Panel padding="24px" gap={18} aria-labelledby="ext-h">
	<SectionHeading
		id="ext-h"
		title="EXTERNAL BUILD"
		subtitle="Provided outside llama-gui, such as by a Nix package"
	/>
	<div class="version">
		<span class="label">Running</span>
		<Metric value={build.tag} size={40} />
		<span class="meta">{externalMeta(build, routerState)}</span>
	</div>
	<div class="field">
		<Eyebrow>DIRECTORY</Eyebrow>
		<span class="meta">{build.path}</span>
	</div>
	{#if build.probe.devices?.length}
		<div class="field">
			<Eyebrow>DEVICES</Eyebrow>
			<ul class="devices">
				{#each build.probe.devices as d (d)}<li class="meta">{d}</li>{/each}
			</ul>
		</div>
	{/if}
	<span class="note">
		LLAMA_GUI_LLAMA_DIR points here, so llama-gui does not install, switch or update builds. Change
		the build where it comes from, then restart llama-gui.
	</span>
</Panel>

<style>
	.version,
	.field {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		min-width: 0;
	}

	.field {
		gap: var(--space-6);
	}

	.label {
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}

	.meta {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
		overflow-wrap: anywhere;
	}

	.devices {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.note {
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}
</style>
