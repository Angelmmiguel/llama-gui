import { describe, expect, it } from 'vitest';
import { chartPaths } from './chart';

describe('chartPaths', () => {
	it('spreads points across the width and flips y', () => {
		const { line, area, endY } = chartPaths([0, 50, 100], 100, 10, 0, 100);
		expect(line).toBe('M0.0,10.0 L50.0,5.0 L100.0,0.0');
		expect(area).toBe(`${line} L100,10 L0,10 Z`);
		expect(endY).toBe(0);
	});

	it('stops a line early when it has fewer values than slots', () => {
		const { line, area } = chartPaths([0, 100], 100, 10, 0, 100, 3);
		expect(line).toBe('M0.0,10.0 L50.0,0.0');
		expect(area).toBe(`${line} L50.0,10 L0,10 Z`);
	});

	it('clamps to the range and skips gaps', () => {
		const { line } = chartPaths([-5, NaN, 500], 10, 10, 0, 100);
		expect(line).toBe('M0.0,10.0 L10.0,0.0');
	});

	it('draws a single point as a flat line', () => {
		expect(chartPaths([5], 10, 10, 0, 10).line).toBe('M0.0,5.0 L10.0,5.0');
	});

	it('survives empty data and a flat range', () => {
		expect(chartPaths([], 10, 10).line).toBe('');
		expect(chartPaths([3, 3], 10, 10).endY).toBe(10);
	});
});
