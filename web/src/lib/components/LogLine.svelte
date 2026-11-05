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
	}

	let { variant = 'grid', ts, source, msg, n, level = '', rise = false }: Props = $props();

	const severity = $derived(level.toUpperCase());
</script>

{#if variant === 'tail'}
	<div class="tail ellipsis" class:rise>
		<span class="ts">{ts}</span>
		<span style:color={sourceColor(source)}>{source}</span>
		<span class="msg">{msg}</span>
	</div>
{:else}
	<div class="grid" class:rise class:warn={severity === 'WARN'} class:error={severity === 'ERROR'}>
		<span class="n">{n}</span>
		<span class="time">{ts}</span>
		<span class="level">{level}</span>
		<span style:color={sourceColor(source)}>{source}</span>
		<span class="line ellipsis">{msg}</span>
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
