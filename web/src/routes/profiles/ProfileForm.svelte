<script lang="ts">
	import type { LocalModelFile } from '$lib/api/client';
	import Band from '$lib/components/Band.svelte';
	import Button from '$lib/components/Button.svelte';
	import Field from '$lib/components/Field.svelte';
	import Metric from '$lib/components/Metric.svelte';
	import Panel from '$lib/components/Panel.svelte';
	import SectionHeading from '$lib/components/SectionHeading.svelte';
	import Segmented from '$lib/components/Segmented.svelte';
	import SelectField from '$lib/components/SelectField.svelte';
	import Stepper from '$lib/components/Stepper.svelte';
	import Switch from '$lib/components/Switch.svelte';
	import TextField from '$lib/components/TextField.svelte';
	import { formatInt } from '$lib/format';
	import FlagGate from './FlagGate.svelte';
	import {
		CTX_CHOICES,
		formatCtx,
		kvNote,
		mmprojOf,
		modelChoices,
		nglView,
		perSlot,
		type FlagField,
		type Form
	} from './view';

	interface Props {
		/** Bindable: edited in place. */
		form: Form;
		/** The models on disk; null falls back to a text field. */
		local: LocalModelFile[] | null;
		/** The model's layer count, 0 when unknown. */
		layers: number;
		/** Settings the active build lacks. */
		unsupported: Set<FlagField>;
		/** The active build's tag, for the notes. */
		buildTag: string;
		/** Nothing runs on a GPU here, so its section is switched off. */
		cpuOnly: boolean;
		/** The API's complaint about the extra arguments. */
		extraError: string;
	}

	let {
		form = $bindable(),
		local,
		layers,
		unsupported,
		buildTag,
		cpuOnly,
		extraError
	}: Props = $props();

	const KV_TYPES = ['f16', 'q8_0', 'q4_0'];

	const choices = $derived(local && local.length > 0 ? modelChoices(local, form.modelFile) : null);
	const ctxOptions = CTX_CHOICES.map((value) => ({ value, label: formatCtx(value) }));
	const slot = $derived(perSlot(Number(form.ctx) || 0, form.parallel));
	const ngl = $derived(nglView(form.ngl, layers));
	const kv = $derived(kvNote(form.ctk, form.ctv, form.flashAttn));
	const kvOptions = (current: string) =>
		(current && !KV_TYPES.includes(current) ? [...KV_TYPES, current] : KV_TYPES).map((value) => ({
			value
		}));

	const numbers: { key: FlagField; label: string; flag: string }[] = [
		{ key: 'batch', label: 'Batch', flag: '-b' },
		{ key: 'ubatch', label: 'Micro-batch', flag: '-ub' },
		{ key: 'threads', label: 'Threads', flag: '-t' },
		{ key: 'temp', label: 'Temperature', flag: '--temp' },
		{ key: 'topP', label: 'Top-p', flag: '--top-p' },
		{ key: 'topK', label: 'Top-k', flag: '--top-k' },
		{ key: 'minP', label: 'Min-p', flag: '--min-p' },
		{ key: 'repeatPenalty', label: 'Repeat penalty', flag: '--repeat-penalty' }
	];
	type NumberKey =
		'batch' | 'ubatch' | 'threads' | 'temp' | 'topP' | 'topK' | 'minP' | 'repeatPenalty';

	function pickModel(file: string) {
		if (!form.id && local) form.mmproj = mmprojOf(file, local);
	}
</script>

<Band layout="column" flex="3 1 560px" as="div">
	<Panel gap={16} padding="20px 22px" aria-labelledby="f-model">
		<SectionHeading id="f-model" title="MODEL" />
		<div class="grid">
			{#if choices}
				<SelectField
					label="Model file"
					flag="-m"
					mono
					options={choices}
					bind:value={form.modelFile}
					onchange={pickModel}
				/>
			{:else}
				<TextField
					label="Model file"
					flag="-m"
					size={40}
					placeholder="relative to the models directory"
					class="mono"
					bind:value={form.modelFile}
				/>
			{/if}
			<TextField
				label="Alias used in API requests"
				flag="--alias"
				size={40}
				class="mono"
				bind:value={form.alias}
			/>
			{#if form.mmproj}
				<TextField
					label="Vision projector"
					flag="--mmproj"
					size={40}
					class="mono"
					bind:value={form.mmproj}
				/>
			{/if}
		</div>
		<div class="switches">
			<FlagGate off={unsupported.has('jinja')} build={buildTag}>
				<Switch
					label="Use the GGUF chat template"
					flag="--jinja"
					checked={form.jinja !== false}
					onchange={(on) => (form.jinja = on)}
				/>
			</FlagGate>
			<FlagGate off={unsupported.has('mlock')} build={buildTag}>
				<Switch label="Keep weights in RAM" flag="--mlock" bind:checked={form.mlock} />
			</FlagGate>
		</div>
	</Panel>

	<Panel gap={16} aria-labelledby="f-ctx">
		<SectionHeading id="f-ctx" title="CONTEXT &amp; SLOTS" />
		<div class="spread">
			<FlagGate off={unsupported.has('ctx')} build={buildTag}>
				<Field label="Total context" flag="-c" as="div" gap={8}>
					<div class="ctx">
						<Segmented
							label="Total context"
							surface="ground"
							options={ctxOptions}
							value={Number(form.ctx)}
							onchange={(v) => (form.ctx = String(v))}
						/>
						<div class="ctx-free">
							<TextField label="Tokens" inputmode="numeric" width="96px" bind:value={form.ctx} />
						</div>
					</div>
				</Field>
			</FlagGate>
			<FlagGate off={unsupported.has('parallel')} build={buildTag}>
				<Field label="Parallel slots" flag="-np" as="div" gap={8}>
					<Stepper
						value={form.parallel || 1}
						min={1}
						max={64}
						decreaseLabel="Fewer slots"
						increaseLabel="More slots"
						onchange={(v) => (form.parallel = v)}
					/>
				</Field>
			</FlagGate>
			<div class="slot">
				<span class="label">Each slot gets</span>
				<Metric value={formatInt(slot.tokens)} unit="tokens" size={22} unitSize={12} />
				<span class="note" class:warn={slot.tight}>{slot.note}</span>
			</div>
		</div>
	</Panel>

	<Panel gap={16} aria-labelledby="f-gpu">
		<SectionHeading id="f-gpu" title="GPU" />
		{#if cpuOnly}
			<span class="note">Everything runs on the CPU here, so these settings do not apply.</span>
		{:else}
			<FlagGate off={unsupported.has('ngl')} build={buildTag}>
				{#if layers > 0}
					<label class="slider">
						<span class="slider-head">
							<span>Layers on GPU <span class="flag">-ngl</span></span>
							<span class="value">{ngl.label}</span>
						</span>
						<input
							type="range"
							min="0"
							max={layers}
							value={ngl.value}
							oninput={(e) => (form.ngl = e.currentTarget.value)}
						/>
					</label>
				{:else}
					<TextField
						label="Layers on GPU"
						flag="-ngl"
						width="120px"
						placeholder="auto"
						class="mono"
						bind:value={form.ngl}
					/>
				{/if}
				{#if layers > 0 && form.ngl !== 'auto' && form.ngl !== ''}
					<div>
						<Button size="xs" variant="ghost" onclick={() => (form.ngl = 'auto')}>
							Let the server decide
						</Button>
					</div>
				{/if}
			</FlagGate>
			<div class="row">
				<FlagGate off={unsupported.has('splitMode')} build={buildTag}>
					<SelectField
						label="Split mode"
						flag="-sm"
						mono
						size={38}
						minWidth="120px"
						options={[
							{ value: '', label: 'default' },
							{ value: 'layer', label: 'layer' },
							{ value: 'row', label: 'row' },
							{ value: 'tensor', label: 'tensor' },
							{ value: 'none', label: 'none' }
						]}
						bind:value={form.splitMode}
					/>
				</FlagGate>
				<FlagGate off={unsupported.has('tensorSplit')} build={buildTag}>
					<TextField
						label="Tensor split"
						flag="-ts"
						width="100px"
						class="mono"
						placeholder="4,1"
						bind:value={form.tensorSplit}
					/>
				</FlagGate>
				<FlagGate off={unsupported.has('flashAttn')} build={buildTag}>
					<Field label="Flash attention" flag="-fa" as="div">
						<Segmented
							label="Flash attention"
							surface="ground"
							options={[{ value: 'auto' }, { value: 'on' }, { value: 'off' }]}
							value={form.flashAttn || 'auto'}
							onchange={(v) => (form.flashAttn = v)}
						/>
					</Field>
				</FlagGate>
			</div>
		{/if}
	</Panel>

	<Panel gap={16} aria-labelledby="f-kv">
		<SectionHeading id="f-kv" title="KV CACHE" />
		<div class="row">
			<FlagGate off={unsupported.has('ctk')} build={buildTag}>
				<Field label="Keys" flag="-ctk" as="div">
					<Segmented
						label="Key cache type"
						surface="ground"
						options={kvOptions(form.ctk)}
						value={form.ctk || 'f16'}
						onchange={(v) => (form.ctk = v)}
					/>
				</Field>
			</FlagGate>
			<FlagGate off={unsupported.has('ctv')} build={buildTag}>
				<Field label="Values" flag="-ctv" as="div">
					<Segmented
						label="Value cache type"
						surface="ground"
						options={kvOptions(form.ctv)}
						value={form.ctv || 'f16'}
						onchange={(v) => (form.ctv = v)}
					/>
				</Field>
			</FlagGate>
			<FlagGate off={unsupported.has('cacheReuse')} build={buildTag}>
				<TextField
					label="Reuse cached chunks ≥"
					flag="--cache-reuse"
					inputmode="numeric"
					width="100px"
					class="mono"
					bind:value={form.cacheReuse}
				/>
			</FlagGate>
		</div>
		<span class="note" class:warn={kv.warn}>{kv.text}</span>
	</Panel>

	<Panel gap={16} aria-labelledby="f-batch">
		<SectionHeading id="f-batch" title="BATCHING, CPU &amp; SAMPLING DEFAULTS" />
		<div class="numbers">
			{#each numbers as n (n.key)}
				<FlagGate off={unsupported.has(n.key)} build={buildTag}>
					<TextField
						label={n.label}
						flag={n.flag}
						inputmode="decimal"
						placeholder="default"
						class="mono"
						bind:value={form[n.key as NumberKey]}
					/>
				</FlagGate>
			{/each}
		</div>
		<div class="extra">
			<TextField
				label="Extra arguments"
				placeholder="--pooling mean --reasoning-format auto"
				class="mono"
				aria-invalid={extraError ? 'true' : undefined}
				bind:value={form.extra}
			/>
			{#if extraError}<span class="error" role="alert">{extraError}</span>{/if}
		</div>
	</Panel>

	<Panel gap={14} aria-labelledby="f-router">
		<SectionHeading id="f-router" title="ROUTER BEHAVIOUR" />
		<div class="switches">
			<Switch label="Load when the server starts" bind:checked={form.loadAtStart} />
			<div class="idle">
				<Switch label="Unload after idle" bind:checked={form.idleUnload} />
				{#if form.idleUnload}
					<span class="minutes">
						<TextField
							label="Idle minutes"
							inputmode="decimal"
							width="72px"
							class="mono"
							bind:value={form.idleMinutes}
						/>
					</span>
				{/if}
			</div>
			<Switch label="Allow on-demand loading by name" bind:checked={form.onDemand} />
		</div>
	</Panel>
</Band>

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
		gap: var(--space-14);
	}

	.switches {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-10) var(--space-28);
	}

	.spread {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-start;
		gap: var(--space-20) var(--space-28);
	}

	.ctx {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: var(--space-8) var(--space-12);
	}

	.ctx-free :global(.field) {
		flex-direction: row;
		align-items: center;
	}

	.slot {
		display: flex;
		flex-direction: column;
		gap: var(--space-6);
		padding-top: var(--space-2);
	}

	.label {
		font-size: var(--fs-12);
		color: var(--fg-tertiary);
	}

	.slider {
		display: flex;
		flex-direction: column;
		gap: var(--space-8);
		font-size: var(--fs-12);
		color: var(--fg-tertiary);
	}

	.slider input {
		width: 100%;
		min-height: 0;
		padding: 0;
	}

	.slider-head {
		display: flex;
		flex-wrap: wrap;
		justify-content: space-between;
		gap: var(--space-8);
	}

	.flag {
		font-family: var(--font-mono);
		color: var(--fg-faint);
	}

	.value {
		font-family: var(--font-mono);
		color: var(--fg-primary);
	}

	.row {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: var(--space-14) var(--space-28);
	}

	.numbers {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
		gap: var(--space-12);
	}

	.extra {
		display: flex;
		flex-direction: column;
		gap: var(--space-6);
	}

	.idle {
		display: flex;
		align-items: center;
		gap: var(--space-10);
	}

	.minutes :global(.field) {
		flex-direction: row;
		align-items: center;
	}

	.note {
		font-size: var(--fs-12);
		color: var(--fg-muted);
	}

	.warn {
		color: var(--color-warn);
	}

	.error {
		font-size: var(--fs-12);
		color: var(--color-error-soft);
	}

	:global(input.mono) {
		font-family: var(--font-mono);
		font-size: var(--fs-12);
	}
</style>
