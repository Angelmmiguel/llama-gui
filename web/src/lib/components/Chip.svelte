<script lang="ts">
	interface Props {
		/** The dim key, such as a flag: `-ngl`. */
		k: string;
		/** The value; omit for bare flags such as `--jinja`. */
		v?: string | number;
	}

	let { k, v }: Props = $props();
</script>

<span class="chip"><span class="k">{k}</span>{v === undefined || v === '' ? '' : ` ${v}`}</span>

<style>
	.chip {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
		background: var(--track);
		border-radius: var(--radius-3);
		padding: 3px 7px;
	}

	.k {
		color: var(--fg-muted);
	}
</style>
