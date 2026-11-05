<script lang="ts">
	import { sourceColor } from './tones';

	interface Props {
		/** `grid` is the Logs viewer's numbered row; `tail` the one-line row of the Overview's log. */
		variant?: 'grid' | 'tail';
		/** Timestamp text. */
		ts: string;
		/** Source tag such as `slot`, `srv`, `router`, `ggml`. Any other value shows muted. */
		source: string;
		msg: string;
		/** Grid only: line number. */
		n?: number;
		/** Grid only: `INFO`, `WARN`, `ERROR`. Warn and error rows are tinted; others show as given. */
		level?: string;
		/** Animate in, for the newest line. */
		rise?: boolean;
		/** Grid only: adds a model column; empty shows blank. */
		model?: string;
		/** Grid only: makes the row a toggle between one truncated line and the whole text. */
		ontoggle?: () => void;
		/** Grid only: wrap the whole text. */
		expanded?: boolean;
	}

	let {
		variant = 'grid',
		ts,
		source,
		msg,
		n,
		level = '',
		rise = false,
		model,
		ontoggle,
		expanded = false
	}: Props = $props();

	const severity = $derived(level.toUpperCase());

	// Selecting text to copy it must not collapse the line under the cursor.
	function click() {
		if (getSelection()?.isCollapsed !== false) ontoggle?.();
	}

	function keydown(event: KeyboardEvent) {
		if (event.key !== 'Enter' && event.key !== ' ') return;
		event.preventDefault();
		ontoggle?.();
	}
</script>

{#if variant === 'tail'}
	<div class="tail ellipsis" class:rise>
		<span class="ts">{ts}</span>
		<span style:color={sourceColor(source)}>{source}</span>
		<span class="msg">{msg}</span>
	</div>
{:else}
	<!-- The role is a button whenever tabindex is set. -->
	<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
	<div
		class="grid"
		class:rise
		class:warn={severity === 'WARN'}
		class:error={severity === 'ERROR'}
		class:with-model={model !== undefined}
		class:toggle={ontoggle}
		role={ontoggle ? 'button' : undefined}
		tabindex={ontoggle ? 0 : undefined}
		aria-expanded={ontoggle ? expanded : undefined}
		onclick={ontoggle ? click : undefined}
		onkeydown={ontoggle ? keydown : undefined}
	>
		<span class="n">{n}</span>
		<span class="time">{ts}</span>
		<span class="level">{level}</span>
		<span class="ellipsis" style:color={sourceColor(source)}>{source}</span>
		{#if model !== undefined}<span class="model ellipsis">{model}</span>{/if}
		<span class="line" class:ellipsis={!expanded} class:whole={expanded}>{msg}</span>
	</div>
{/if}

<style>
	.ts {
		color: var(--fg-timestamp);
	}

	.msg {
		color: var(--fg-log);
	}

	.grid {
		display: grid;
		grid-template-columns: 56px 70px 52px 56px minmax(0, 1fr);
		gap: var(--space-10);
		padding: 3px var(--space-16);
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		line-height: 1.5;
	}

	.with-model {
		grid-template-columns: 56px 70px 52px 56px 110px minmax(0, 1fr);
	}

	.toggle {
		cursor: pointer;
	}

	.toggle:hover {
		background: var(--bg-selected);
	}

	.model {
		color: var(--fg-off);
	}

	.whole {
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}

	.n {
		color: var(--fg-gutter);
		text-align: right;
	}

	.time {
		color: var(--fg-off);
	}

	.level {
		color: var(--fg-faint);
	}

	.line {
		color: var(--fg-log-line);
	}

	.warn {
		background: var(--tint-warn);
	}

	.warn .level {
		color: var(--color-warn);
	}

	.error {
		background: var(--tint-error);
	}

	.error .level {
		color: var(--color-error);
	}

	.tail {
		font-family: var(--font-mono);
		font-size: var(--fs-11-5);
		line-height: 1.75;
	}
</style>
