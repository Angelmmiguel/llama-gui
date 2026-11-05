import { describe, expect, it } from 'vitest';
import {
	busyPercent,
	contextCells,
	HEATMAP_CELLS,
	headCell,
	heatCells,
	MAP_CELLS,
	peakSpeed,
	promptText,
	samplingParams,
	slotFilename
} from './slots';

describe('contextCells', () => {
	it('fills in token order', () => {
		const cells = contextCells({ cached: 480, prompt: 240, generated: 240 }, 1920);
		expect(cells).toHaveLength(MAP_CELLS);
		const count = (k: string) => cells.filter((c) => c === k).length;
		expect([count('cached'), count('prompt'), count('generated'), count('empty')]).toEqual([
			48, 24, 24, 96
		]);
		expect(cells[0]).toBe('cached');
		expect(cells[48]).toBe('prompt');
		expect(cells[72]).toBe('generated');
		expect(cells[96]).toBe('empty');
	});

	it('is empty without a context', () => {
		expect(
			contextCells({ cached: 10, prompt: 0, generated: 0 }, 0).every((c) => c === 'empty')
		).toBe(true);
	});

	it('finds the head', () => {
		expect(headCell(0, 1920)).toBe(-1);
		expect(headCell(1, 1920)).toBe(0);
		expect(headCell(11, 1920)).toBe(1);
		expect(headCell(5000, 1920)).toBe(MAP_CELLS - 1);
	});
});

describe('heatCells', () => {
	const decode = (speed: number) => ({ state: 'decode', speed });
	const idle = { state: 'idle', speed: 0 };

	it('always has 90 cells, the newest on the right', () => {
		const cells = heatCells([{ state: 'prefill', speed: 0 }, decode(40)], 1000, 40);
		expect(cells).toHaveLength(HEATMAP_CELLS);
		expect(cells[0]).toEqual({ key: 911, state: 'idle', opacity: 1 });
		expect(cells[88].state).toBe('prefill');
		expect(cells[89]).toEqual({ key: 1000, state: 'decode', opacity: 1 });
	});

	it('keeps a second under one key as time moves', () => {
		const samples = [decode(40), idle];
		const before = heatCells(samples, 1000, 40);
		const after = heatCells([...samples, idle], 1001, 40);
		expect(after[87]).toEqual(before[88]);
	});

	it('brightens with speed against a shared peak', () => {
		expect(heatCells([decode(0)], 1, 40)[89].opacity).toBe(0.45);
		expect(heatCells([decode(20)], 1, 40)[89].opacity).toBe(0.75);
		expect(peakSpeed([{ samples: [decode(5), idle] }, { samples: [decode(9)] }])).toBe(9);
		expect(peakSpeed([])).toBe(0);
	});

	it('counts busy seconds', () => {
		expect(busyPercent([{ samples: [decode(1), idle] }, { samples: [idle, idle] }])).toBe(25);
		expect(busyPercent([])).toBe(0);
	});
});

describe('samplingParams', () => {
	it('renames, cleans floats and skips what is not there', () => {
		expect(
			samplingParams({ temperature: 0.800000011920929, top_k: 40, other: 1, n_predict: -1 })
		).toEqual([
			{ k: 'temp', v: '0.8' },
			{ k: 'top_k', v: '40' },
			{ k: 'n_predict', v: '-1' }
		]);
		expect(samplingParams(undefined)).toEqual([]);
	});
});

describe('slotFilename', () => {
	it('yields a name the API accepts', () => {
		expect(slotFilename('qwen3-coder', 2)).toBe('qwen3-coder-slot2.bin');
		expect(slotFilename('org:model.v1', 0)).toBe('org-model.v1-slot0.bin');
		expect(slotFilename('::', 1)).toBe('model-slot1.bin');
		expect(slotFilename('m'.repeat(200), 1)).toMatch(/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/);
	});
});

describe('promptText', () => {
	it('reads messages', () => {
		const body = JSON.stringify({
			messages: [
				{ role: 'system', content: 'be brief' },
				{ role: 'user', content: [{ type: 'text', text: 'hi' }, { type: 'image_url' }] }
			]
		});
		expect(promptText(body)).toBe('system: be brief\nuser: hi [attachment]');
	});

	it('reads the other request shapes', () => {
		expect(promptText('{"prompt":"go"}')).toBe('go');
		expect(promptText('{"input_prefix":"a","input_suffix":"b"}')).toBe('a…b');
		expect(promptText('{"input":"chunk"}')).toBe('chunk');
		expect(promptText('{"other":1}')).toBe('{"other":1}');
	});

	it('shows a truncated body as it is and caps a long one', () => {
		expect(promptText('{"messages":[{"ro')).toBe('{"messages":[{"ro');
		expect(promptText(JSON.stringify({ prompt: 'x'.repeat(5000) }))).toHaveLength(4001);
	});
});
