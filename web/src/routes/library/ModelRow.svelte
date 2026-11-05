<script lang="ts">
	import type { Snippet } from 'svelte';
	import Tag from '$lib/components/Tag.svelte';

	interface Props {
		name: string;
		/** The repository or file, in mono under the name. */
		source: string;
		tags: readonly string[];
		loaded?: boolean;
		params: string;
		context?: string;
		open: boolean;
		ontoggle: () => void;
		/** The right column: the best fit. */
		fit: Snippet;
		children: Snippet;
	}

	let {
		name,
		source,
		tags,
		loaded = false,
		params,
		context,
		open,
		ontoggle,
		fit,
		children
	}: Props = $props();
</script>

<div class="model">
	<button type="button" class="head" aria-expanded={open} onclick={ontoggle}>
		<span class="who">
			<span class="line">
				<span class="name">{name}</span>
				{#if loaded}<Tag tone="ok">LOADED</Tag>{/if}
			</span>
			<span class="source">{source}</span>
			{#if tags.length > 0}
				<span class="tags">
					{#each tags as tag (tag)}<Tag tone="soft">{tag}</Tag>{/each}
				</span>
			{/if}
		</span>
		<span class="specs">
			<span>{params}</span>
			{#if context}<span class="context">{context}</span>{/if}
		</span>
		<span class="bar">{@render fit()}</span>
		<svg
			class="chevron"
			aria-hidden="true"
			width="18"
			height="18"
			viewBox="0 0 24 24"
			fill="none"
			stroke="currentColor"
			stroke-width="2"
			stroke-linecap="round"
			stroke-linejoin="round"
		>
			<path d={open ? 'm7 14 5-5 5 5' : 'm7 10 5 5 5-5'}></path>
		</svg>
	</button>
	{#if open}
		<div class="detail">{@render children()}</div>
	{/if}
</div>

<style>
	.model {
		border-top: 1px solid var(--border-row);
	}

	.head {
		width: 100%;
		display: grid;
		grid-template-columns: minmax(0, 1.6fr) minmax(0, 1fr) minmax(160px, 1fr) 28px;
		align-items: center;
		gap: var(--space-16);
		padding: var(--space-14) var(--space-4);
		background: transparent;
		border: none;
		color: inherit;
		text-align: left;
		font: inherit;
		cursor: pointer;
	}

	.who {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		min-width: 0;
	}

	.line {
		display: flex;
		align-items: center;
		gap: var(--space-8);
		min-width: 0;
	}

	.name {
		font-weight: 500;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.line :global(*:not(.name)) {
		flex: none;
	}

	.source {
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		color: var(--fg-muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.tags {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-4);
	}

	.specs {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		min-width: 0;
	}

	.context {
		color: var(--fg-muted);
		font-size: var(--fs-11);
	}

	.chevron {
		color: var(--fg-muted);
	}

	.detail {
		margin: 0 var(--space-4) var(--space-16);
		background: var(--bg-inset);
		border: 1px solid var(--border-panel);
		border-radius: 10px;
		padding: var(--space-14) var(--space-16);
		display: flex;
		flex-direction: column;
		gap: var(--space-10);
	}

	@media (max-width: 720px) {
		.head {
			grid-template-columns: minmax(0, 1fr) 28px;
			gap: var(--space-8) var(--space-10);
		}

		.who {
			grid-column: 1;
			grid-row: 1;
		}

		.chevron {
			grid-column: 2;
			grid-row: 1;
			align-self: start;
		}

		.specs,
		.bar {
			grid-column: 1 / -1;
		}

		.specs {
			flex-direction: row;
			flex-wrap: wrap;
			gap: var(--space-2) var(--space-10);
		}

		.detail {
			margin-inline: 0;
			padding: var(--space-12);
		}
	}
</style>
