import { describe, expect, it } from 'vitest';
import type { LocalModelFile, Profile, ProfileEstimate } from '$lib/api/client';
import {
	aliasFromFile,
	countChanges,
	formatCtx,
	fromProfile,
	kvNote,
	listMeta,
	listStatus,
	memoryView,
	modelChoices,
	modelMainFile,
	newForm,
	nglView,
	parseCommandLine,
	perSlot,
	quantOf,
	resolveModelPath,
	tokenize,
	toProfile,
	mlockFlag,
	unsupportedFields
} from './view';

const GIB = 1024 ** 3;

const base: Profile = {
	id: 'p1',
	name: 'Coder',
	alias: 'coder',
	model_file: 'coder-Q4_K_M.gguf',
	ctx_size: 65536,
	parallel: 4,
	ngl: '99',
	temp: 0,
	cache_reuse: 256,
	idle_unload_seconds: 600,
	on_demand: true,
	extra_args: ['--foo', 'bar']
};

describe('form', () => {
	it('round-trips a profile', () => {
		const { profile, errors } = toProfile(fromProfile(base));
		expect(errors).toEqual([]);
		expect(JSON.parse(JSON.stringify(profile))).toEqual({ ...base, extra_args: ['--foo', 'bar'] });
	});

	it('keeps zero where zero means something', () => {
		const { profile } = toProfile(fromProfile({ ...base, temp: 0, top_k: 0 }));
		expect(profile.temp).toBe(0);
		expect(profile.top_k).toBe(0);
	});

	it('leaves unset settings to upstream', () => {
		const { profile } = toProfile(
			fromProfile({ id: 'x', name: 'x', alias: 'x', model_file: 'x.gguf', ctx_size: 1024 })
		);
		for (const key of [
			'parallel',
			'jinja',
			'ngl',
			'flash_attn',
			'batch',
			'temp',
			'extra_args'
		] as const) {
			expect(profile[key]).toBeUndefined();
		}
	});

	it('reports what is not a number and leaves it out', () => {
		const form = fromProfile(base);
		form.threads = 'many';
		form.temp = '-1';
		const { profile, errors } = toProfile(form);
		expect(profile.threads).toBeUndefined();
		expect(errors).toHaveLength(2);
		expect(errors[0]).toContain('Threads');
	});

	it('wants idle minutes while idle unload is on', () => {
		const form = fromProfile(base);
		form.idleMinutes = '';
		expect(toProfile(form).errors).toHaveLength(1);
		form.idleUnload = false;
		expect(toProfile(form).errors).toEqual([]);
		expect(toProfile(form).profile.idle_unload_seconds).toBeUndefined();
	});

	it('counts changes, ignoring hidden minutes', () => {
		const a = fromProfile(base);
		expect(countChanges(a, fromProfile(base))).toBe(0);
		expect(countChanges({ ...a, ctx: '8192', parallel: 2 }, a)).toBe(2);
		const off = { ...a, idleUnload: false };
		expect(countChanges({ ...off, idleMinutes: '5' }, off)).toBe(0);
	});

	it('starts a new profile from a model path', () => {
		const form = newForm('dir/Some Model-00001-of-00002.gguf');
		expect(form.id).toBeUndefined();
		expect(form.alias).toBe('Some-Model');
		expect(form.onDemand).toBe(true);
		expect(form.parallel).toBe(1);
	});
});

describe('notes', () => {
	it.each([
		[65536, 4, 16384, false, 'Good'],
		[65536, 1, 65536, false, 'Room'],
		[8192, 4, 2048, true, 'Tight'],
		[32768, 0, 32768, false, 'left to the server']
	])('slots of %i over %i', (ctx, np, tokens, tight, note) => {
		const got = perSlot(ctx, np);
		expect(got.tokens).toBe(tokens);
		expect(got.tight).toBe(tight);
		expect(got.note).toContain(note);
	});

	it.each([
		['q8_0', 'q8_0', 'off', true, 'flash attention'],
		['f16', 'f16', 'off', false, 'halves'],
		['q4_0', 'f16', 'on', true, 'q4_0 keys'],
		['q8_0', 'q8_0', 'on', false, 'halves'],
		['', '', '', false, 'halves']
	])('KV %s/%s with -fa %s', (k, v, fa, warn, text) => {
		const got = kvNote(k, v, fa);
		expect(got.warn).toBe(warn);
		expect(got.text).toContain(text);
	});

	it.each([
		['99', 48, 48, '48 / 48 · all on GPU'],
		['48', 48, 48, '48 / 48 · all on GPU'],
		['12', 48, 12, '12 / 48 · 75% on CPU, slower decode'],
		['0', 48, 0, '0 / 48 · CPU only'],
		['', 48, 48, 'auto · up to the server'],
		['all', 48, 48, 'all on GPU'],
		['12', 0, 12, '12 · layer count unknown']
	])('-ngl %s of %i', (ngl, layers, value, label) => {
		expect(nglView(ngl, layers)).toEqual({ value, label });
	});

	it('formats context', () => {
		expect(formatCtx(65536)).toBe('64k');
		expect(formatCtx(5000)).toBe('5000');
	});
});

describe('build support', () => {
	it('treats no flags as unknown, not as nothing supported', () => {
		expect(unsupportedFields([]).size).toBe(0);
		expect(unsupportedFields(null).size).toBe(0);
	});

	it('names what the build lacks', () => {
		const got = unsupportedFields(['--ctx-size', '--jinja']);
		expect(got.has('ctx')).toBe(false);
		expect(got.has('jinja')).toBe(false);
		expect(got.has('cacheReuse')).toBe(true);
	});

	it('finds keeping weights in RAM under either flag', () => {
		expect(unsupportedFields(['--ctx-size']).has('mlock')).toBe(true);
		expect(unsupportedFields(['--mlock']).has('mlock')).toBe(false);
		expect(unsupportedFields(['--load-mode']).has('mlock')).toBe(false);
	});

	it('names the flag the build uses for keeping weights in RAM', () => {
		expect(mlockFlag(['--load-mode'])).toBe('--load-mode mmap+mlock');
		expect(mlockFlag(['--mlock'])).toBe('--mlock');
		expect(mlockFlag(['--mlock', '--load-mode'])).toBe('--mlock');
		expect(mlockFlag(null)).toBe('--mlock');
	});
});

describe('local models', () => {
	const root: LocalModelFile = {
		id: 'a',
		name: 'a',
		path: 'a-Q8_0.gguf',
		files: ['a-Q8_0.gguf'],
		quant: 'Q8_0'
	};
	const dir: LocalModelFile = {
		id: 'b',
		name: 'b',
		path: 'org_b',
		files: ['mmproj-f16.gguf', 'b-00002-of-00002.gguf', 'b-00001-of-00002.gguf'],
		mmproj: 'mmproj-f16.gguf'
	};

	it('finds the main file', () => {
		expect(modelMainFile(root)).toBe('a-Q8_0.gguf');
		expect(modelMainFile(dir)).toBe('org_b/b-00001-of-00002.gguf');
	});

	it('resolves a Library link to its file', () => {
		expect(resolveModelPath('org_b', [root, dir])).toBe('org_b/b-00001-of-00002.gguf');
		expect(resolveModelPath('elsewhere.gguf', [root])).toBe('elsewhere.gguf');
	});

	it('keeps a file that left the disk selectable', () => {
		const got = modelChoices([root], 'gone.gguf');
		expect(got[0]).toEqual({ value: 'gone.gguf', label: 'gone.gguf (not on disk)' });
		expect(modelChoices([root], 'a-Q8_0.gguf')).toHaveLength(1);
	});

	it('reads the quantization', () => {
		expect(quantOf('a-Q8_0.gguf', [root])).toBe('Q8_0');
		expect(quantOf('gpt-oss-20b-mxfp4.gguf', [])).toBe('MXFP4');
		expect(quantOf('m.Q5_K_M.gguf', [])).toBe('Q5_K_M');
		expect(quantOf('plain.gguf', [])).toBe('');
	});

	it('makes an alias of a file name', () => {
		expect(aliasFromFile('')).toBe('');
		expect(aliasFromFile('sub/My Model.Q4.gguf')).toBe('My-Model.Q4');
		expect(aliasFromFile('_x.gguf')).toBe('x');
	});
});

describe('list', () => {
	const memory = {} as never;
	it.each([
		[{ status: 'loaded', id: 'a', memory }, true, 'loaded'],
		[{ status: 'loading', id: 'a', memory }, true, 'loading'],
		[{ status: 'unloaded', failed: true, id: 'a', memory }, true, 'failed'],
		[undefined, true, 'on demand'],
		[{ status: 'loaded', id: 'a', memory }, false, 'router stopped']
	])('status %#', (model, up, word) => {
		expect(listStatus(base, model, up).word).toBe(word);
	});

	it('writes the meta line', () => {
		expect(listMeta(base, 'Q4_K_M', 'loaded')).toBe('Q4_K_M · 64k × 4 · loaded');
		expect(listMeta({ ...base, parallel: undefined }, '', 'on demand')).toBe('64k · on demand');
	});
});

describe('memoryView', () => {
	const use = (over = {}) => ({
		capacity: 48 * GIB,
		other: 0,
		weights: 0,
		kv: 0,
		compute: 0,
		...over
	});
	const estimate: ProfileEstimate = {
		source: 'estimate',
		rough: false,
		layers: 48,
		router_up: true,
		gpu: use({ other: 12 * GIB, weights: 12 * GIB, kv: 3 * GIB, compute: GIB }),
		ram: use({ capacity: 64 * GIB, other: 16 * GIB }),
		verdict: 'fits',
		verdict_on: 'gpu',
		free_bytes: 20 * GIB
	};

	it('shows the GPU first and places every segment on its track', () => {
		const got = memoryView(estimate, 'q8_0', 'q8_0');
		expect(got.headline).toBe('16.0');
		expect(got.headlineUnit).toBe('GB on GPU');
		expect(got.verdict).toBe('fits · 20.0 GB free');
		expect(got.bars[0].label).toBe('VRAM · 48 GB (includes other loaded models: 12.0 GB)');
		expect(got.bars[0].segments[0].value).toBeCloseTo(25);
		expect(got.bars[1].label).toBe('System RAM · 64 GB');
		expect(got.rows[1].label).toBe('KV cache (q8_0/q8_0)');
	});

	it('uses RAM wording only without a GPU', () => {
		const got = memoryView(
			{
				...estimate,
				gpu: null,
				ram: use({ capacity: 64 * GIB, weights: 8 * GIB }),
				verdict_on: 'ram'
			},
			'',
			''
		);
		expect(got.headlineUnit).toBe('GB in RAM');
		expect(got.bars).toHaveLength(1);
		const text = JSON.stringify(got);
		expect(text).not.toContain('VRAM');
		expect(text).not.toContain('GPU');
	});

	it('labels the source and its caveats', () => {
		expect(memoryView({ ...estimate, source: 'measured' }, '', '').source).toContain('Measured');
		expect(memoryView(estimate, '', '').source).toContain('Estimate');
		const down = memoryView({ ...estimate, router_up: false, rough: true }, '', '');
		expect(down.caveat).toContain('stopped');
		expect(down.caveat).toContain('rule of thumb');
		expect(down.bars[0].label).toContain('other models unknown');
	});

	it('says where it does not fit', () => {
		const over = { ...estimate, verdict: 'over', free_bytes: -2 * GIB };
		expect(memoryView(over, '', '').verdict).toBe('over by 2.0 GB');
		expect(memoryView({ ...over, verdict_on: 'ram' }, '', '').verdict).toBe('over by 2.0 GB RAM');
	});
});

describe('tokenize', () => {
	it.each([
		['a b', ['a', 'b']],
		['a \\\n  b', ['a', 'b']],
		["-m 'my model.gguf'", ['-m', 'my model.gguf']],
		['-m "a \\"b\\""', ['-m', 'a "b"']],
		['--x ""', ['--x', '']],
		['a\\ b', ['a b']],
		['', []]
	])('%j', (line, want) => {
		expect(tokenize(line)).toEqual(want);
	});
});

describe('parseCommandLine', () => {
	it.each<[string, string, Record<string, unknown>, string[]]>([
		['short flags', '-c 8192 -np 2 -ngl 40', { ctx: '8192', parallel: 2, ngl: '40' }, []],
		['long flags', '--ctx-size 8192 --parallel 2', { ctx: '8192', parallel: 2 }, []],
		['equals', '--ctx-size=8192 --top-k=20', { ctx: '8192', topK: '20' }, []],
		[
			'KV',
			'-ctk q8_0 -ctv q4_0 --cache-reuse 256',
			{ ctk: 'q8_0', ctv: 'q4_0', cacheReuse: '256' },
			[]
		],
		[
			'GPU',
			'-sm row -ts 4,1 -fa on',
			{ splitMode: 'row', tensorSplit: '4,1', flashAttn: 'on' },
			[]
		],
		['bare -fa', '-fa --mlock', { flashAttn: 'on', mlock: true }, []],
		['load mode that locks', '--load-mode mmap+mlock', { mlock: true }, []],
		['short load mode that locks', '-lm mlock', { mlock: true }, []],
		['load mode that does not lock', '--load-mode dio', {}, ['--load-mode', 'dio']],
		['jinja', '--jinja', { jinja: true }, []],
		['no jinja', '--no-jinja', { jinja: false }, []],
		['batching', '-b 2048 -ub 512 -t 16', { batch: '2048', ubatch: '512', threads: '16' }, []],
		[
			'sampling',
			'--temp 0.7 --top-p 0.8 --top-k 20 --min-p 0 --repeat-penalty 1.05',
			{ temp: '0.7', topP: '0.8', topK: '20', minP: '0', repeatPenalty: '1.05' },
			[]
		],
		['unknown with value', '--pooling mean -c 1', { ctx: '1' }, ['--pooling', 'mean']],
		['unknown switch', '--embeddings -c 1', { ctx: '1' }, ['--embeddings']],
		['unknown with equals', '--seed=1 7', {}, ['--seed=1', '7']],
		['unknown short', '-x 1', {}, ['-x', '1']],
		['a negative value is a value', '-ngl -1', { ngl: '-1' }, []]
	])('%s', (_, line, patch, extra) => {
		const got = parseCommandLine(`llama-server ${line}`);
		expect(got.patch).toEqual(patch);
		expect(got.extra).toEqual(extra);
	});

	it('takes a command as the page prints it', () => {
		const got = parseCommandLine(
			'llama-server \\\n  -m /models/a.gguf --alias a \\\n  -c 4096 -np 2 \\\n  --jinja --pooling mean'
		);
		expect(got.patch).toMatchObject({
			modelFile: 'a.gguf',
			alias: 'a',
			ctx: '4096',
			parallel: 2,
			jinja: true
		});
		expect(got.extra).toEqual(['--pooling', 'mean']);
		expect(got.notes[0]).toContain('relative');
	});

	it('names the alias after the file when there is none', () => {
		expect(parseCommandLine('-m sub/m-Q4.gguf').patch).toEqual({
			modelFile: 'sub/m-Q4.gguf',
			alias: 'm-Q4'
		});
	});

	it('leaves out what the router owns', () => {
		const got = parseCommandLine('--host 0.0.0.0 --port=8080 -c 1');
		expect(got.patch).toEqual({ ctx: '1' });
		expect(got.extra).toEqual([]);
		expect(got.notes).toHaveLength(2);
	});

	it('reports a flag missing its value', () => {
		expect(parseCommandLine('-c').notes[0]).toContain('-c');
	});
});
