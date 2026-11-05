<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { HTMLButtonAttributes } from 'svelte/elements';

	interface Props extends HTMLButtonAttributes {
		/**
		 * `primary` solid amber, `default` raised (page header actions), `ghost` transparent with
		 * a border, `danger` transparent with red text.
		 */
		variant?: 'primary' | 'default' | 'ghost' | 'danger';
		/**
		 * Minimum height: `xs` 30, `sm` 32 (both 12 px text, radius 6), `md` 36, `ml` 38, `lg` 40
		 * (13 px text), `xl` 44 (14 px text).
		 */
		size?: 'xs' | 'sm' | 'md' | 'ml' | 'lg' | 'xl';
		/** Text color override: `muted` #C9CED6 (Export CSV, unload), `accent` amber (load). */
		tone?: 'muted' | 'accent';
		/** Mono label, as on Overview's load and unload. */
		mono?: boolean;
		/** Dashed border and muted text, for "+ New profile". */
		dashed?: boolean;
		/** Grow to fill a flex row. */
		fill?: boolean;
		/** Full width, for button grids. */
		block?: boolean;
		/** Renders a link that saves the target, for exports. */
		downloadHref?: string;
		/** Renders a link that looks like the button; pass a resolved path. */
		href?: string;
		children: Snippet;
	}

	let {
		variant = 'default',
		size = 'md',
		tone,
		mono = false,
		dashed = false,
		fill = false,
		block = false,
		downloadHref,
		href,
		type = 'button',
		children,
		...rest
	}: Props = $props();

	const classes = $derived([
		'button',
		`variant-${variant}`,
		`size-${size}`,
		tone && `tone-${tone}`,
		{ mono, dashed, fill, block },
		rest.class
	]);
</script>

{#if downloadHref}
	<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- an API route, not a page. -->
	<a href={downloadHref} download title={rest.title} class={classes}>{@render children()}</a>
{:else if href}
	<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- callers resolve the path -->
	<a {href} title={rest.title} class={classes}>{@render children()}</a>
{:else}
	<button {type} {...rest} class={classes}>
		{@render children()}
	</button>
{/if}

<style>
	.button {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: var(--space-6);
		border: 1px solid transparent;
	}

	.button:disabled {
		opacity: 0.6;
	}

	.variant-primary {
		border: none;
		background: var(--color-decode);
		color: var(--fg-on-accent);
		font-weight: 500;
	}

	.variant-primary:hover:not(:disabled) {
		filter: brightness(1.08);
	}

	.variant-default {
		background: var(--bg-control);
		border-color: var(--border-control);
		color: var(--fg-primary);
	}

	.variant-ghost {
		background: transparent;
		border-color: var(--border-ghost);
		color: var(--fg-primary);
	}

	.variant-danger {
		background: transparent;
		border-color: var(--border-danger);
		color: var(--color-error-soft);
	}

	/* The design's small Delete keeps the neutral border. */
	.variant-danger.size-xs,
	.variant-danger.size-sm {
		border-color: var(--border-ghost);
	}

	.variant-default:hover:not(:disabled),
	.variant-ghost:hover:not(:disabled),
	.variant-danger:hover:not(:disabled) {
		border-color: var(--border-raised);
	}

	.size-xs,
	.size-sm {
		padding: 0 var(--space-10);
		border-radius: var(--radius-4);
		font-size: var(--fs-12);
	}

	.size-xs {
		min-height: 30px;
	}

	.size-sm {
		min-height: 32px;
	}

	.size-md,
	.size-ml,
	.size-lg {
		padding: 0 var(--space-12);
		border-radius: var(--radius-5);
		font-size: var(--fs-13);
	}

	.size-md {
		min-height: 36px;
	}

	.size-ml {
		min-height: 38px;
	}

	.size-lg {
		min-height: 40px;
	}

	.size-xl {
		min-height: 44px;
		padding: 0 var(--space-16);
		border-radius: var(--radius-5);
		font-size: var(--fs-14);
	}

	.tone-muted {
		color: var(--fg-secondary);
	}

	.tone-accent {
		color: var(--color-decode);
	}

	.mono {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}

	.dashed {
		border-style: dashed;
		color: var(--fg-tertiary);
	}

	.fill {
		flex: 1;
	}

	.block {
		width: 100%;
	}
</style>
