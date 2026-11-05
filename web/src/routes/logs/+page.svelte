<script lang="ts">
	import PageHeader from '$lib/components/PageHeader.svelte';
</script>

{#snippet subtitle()}
	llama-server output, parsed by source and level · verbosity <span class="mono">-lv 3</span>
{/snippet}

<PageHeader title="Logs" {subtitle} />
