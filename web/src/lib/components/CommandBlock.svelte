<script lang="ts">
	import CopyButton from './CopyButton.svelte';

	interface Props {
		text: string;
		/** `command` breaks anywhere (shell lines); `message` wraps on words (prompts, messages). */
		kind?: 'command' | 'message';
		/** Mono caps label above the block. */
		label?: string;
		/** Make the label a real h2 with this id, for the panel's `aria-labelledby`. */
		headingId?: string;
		/** Show a Copy button opposite the label. */
		copy?: boolean;
	}

	let { text, kind = 'command', label, headingId, copy = false }: Props = $props();
</script>

<div class="wrap" class:copy>
	{#if label || copy}
		<div class="head">
			{#if headingId}
				<h2 id={headingId} class="label">{label}</h2>
			{:else if label}
				<span class="label">{label}</span>
			{/if}
			{#if copy}<CopyButton {text} />{/if}
		</div>
	{/if}
	<div class="block {kind}">{text}</div>
</div>

<style>
	.wrap {
		display: flex;
		flex-direction: column;
		gap: var(--space-6);
	}

	.wrap.copy {
		gap: var(--space-12);
	}

	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-8);
	}

	.label {
		margin: 0;
		font-family: var(--font-mono);
		font-size: var(--fs-11);
		font-weight: 500;
		letter-spacing: var(--tracking-caps);
		color: var(--fg-muted);
	}

	.block {
		background: var(--bg-deep);
		border: 1px solid var(--border-panel);
		border-radius: var(--radius-5);
		padding: var(--space-12);
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		white-space: pre-wrap;
	}

	.command {
		line-height: 1.7;
		color: var(--fg-secondary);
		word-break: break-all;
	}

	.message {
		line-height: 1.6;
		color: var(--fg-log);
	}
</style>
