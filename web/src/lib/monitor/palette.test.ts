import { describe, expect, it } from 'vitest';
import type { RouterModel } from '$lib/api/client';
import {
	filterCommands,
	modelCommands,
	moveActive,
	PAGES,
	pageCommands,
	parseCommandId
} from './palette';

const model = (id: string, status: string) => ({ id, status }) as RouterModel;

describe('filterCommands', () => {
	const commands = pageCommands();
	const labels = (query: string) => filterCommands(commands, query).map((c) => c.label);

	it('returns everything for an empty or blank query', () => {
		expect(filterCommands(commands, '')).toHaveLength(PAGES.length);
		expect(filterCommands(commands, '   ')).toHaveLength(PAGES.length);
	});

	it('matches a substring anywhere, ignoring case', () => {
		expect(labels('BENCH')).toEqual(['Bench']);
		expect(labels('rofil')).toEqual(['Profiles']);
	});

	it('matches keywords, but ranks them below labels', () => {
		const list = [
			{ id: 'a', label: 'Other', hint: 'x', keywords: 'bench' },
			{ id: 'b', label: 'A bench', hint: 'x' },
			{ id: 'c', label: 'Bench', hint: 'x' }
		];
		expect(filterCommands(list, 'bench').map((c) => c.id)).toEqual(['c', 'b', 'a']);
	});

	it('needs every word, in any order', () => {
		const list = modelCommands([model('qwen3-coder', 'unloaded'), model('gemma', 'unloaded')]);
		expect(filterCommands(list, 'coder load').map((c) => c.label)).toEqual(['Load qwen3-coder']);
		expect(filterCommands(list, 'load nothing')).toEqual([]);
	});

	it('keeps the order of equals', () => {
		const list = modelCommands([model('b', 'unloaded'), model('a', 'unloaded')]);
		expect(filterCommands(list, 'load').map((c) => c.label)).toEqual(['Load b', 'Load a']);
	});

	it('does not treat regex characters specially', () => {
		expect(labels('.*')).toEqual([]);
	});
});

describe('modelCommands', () => {
	it('offers what the state calls for, unloading anything that holds memory', () => {
		const list = modelCommands([
			model('a', 'loaded'),
			model('b', 'unloaded'),
			model('c', 'loading'),
			model('d', 'someday')
		]);
		expect(list.map((c) => c.label)).toEqual(['Unload a', 'Load b', 'Unload c', 'Unload d']);
		expect(list[0].hint).toBe('Model · loaded');
	});
});

it('parseCommandId keeps colons in the target', () => {
	expect(parseCommandId('load:org/repo:Q4')).toEqual({ kind: 'load', target: 'org/repo:Q4' });
	expect(parseCommandId('restart')).toEqual({ kind: 'restart', target: '' });
});

it('moveActive wraps, and survives an empty list', () => {
	expect(moveActive(0, -1, 3)).toBe(2);
	expect(moveActive(2, 1, 3)).toBe(0);
	expect(moveActive(1, 1, 3)).toBe(2);
	expect(moveActive(0, 1, 0)).toBe(0);
});
