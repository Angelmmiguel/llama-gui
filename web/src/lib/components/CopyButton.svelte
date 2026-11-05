<script lang="ts">
	import Button from './Button.svelte';

	interface Props {
		/** What goes on the clipboard. */
		text: string;
		/** Button text, such as `Copy as cURL`. */
		label?: string;
		variant?: 'default' | 'ghost';
		size?: 'xs' | 'sm' | 'lg';
		/** Text color override, as on Button. */
		tone?: 'muted';
		fill?: boolean;
	}

	let { text, label = 'Copy', variant = 'ghost', size = 'xs', tone, fill }: Props = $props();

	let state = $state<'idle' | 'copied' | 'failed'>('idle');
	let timer: ReturnType<typeof setTimeout> | undefined;

	// navigator.clipboard only exists in secure contexts, and a self-hosted
	// dashboard is often reached over plain HTTP on the LAN.
	async function write(value: string): Promise<boolean> {
		if (navigator.clipboard) {
			try {
				await navigator.clipboard.writeText(value);
				return true;
			} catch {
				// Denied: the legacy path below may still work.
			}
		}
		const area = document.createElement('textarea');
		area.value = value;
		area.style.position = 'fixed';
		area.style.opacity = '0';
		document.body.append(area);
		area.select();
		const ok = document.execCommand('copy');
		area.remove();
		return ok;
	}

	async function copy() {
		state = (await write(text)) ? 'copied' : 'failed';
		clearTimeout(timer);
		timer = setTimeout(() => (state = 'idle'), 1500);
	}
</script>

<Button {variant} {size} tone={tone ?? (size === 'lg' ? undefined : 'muted')} {fill} onclick={copy}>
	<span aria-live="polite"
		>{state === 'copied' ? 'Copied' : state === 'failed' ? 'Copy failed' : label}</span
	>
</Button>
