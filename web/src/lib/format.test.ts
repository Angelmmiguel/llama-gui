import { describe, expect, it } from 'vitest';
import {
	formatBytes,
	formatClock,
	formatDayClock,
	formatDuration,
	formatGB,
	formatInt,
	formatMs,
	formatRate,
	formatTokPerSec,
	formatTokens,
	formatUptime
} from './format';

describe('formatTokens', () => {
	it.each([
		[0, '0'],
		[512, '512'],
		[999, '999'],
		[1000, '1.0k'],
		[1200, '1.2k'],
		[62880, '62.9k'],
		[999_949, '999.9k'],
		[999_950, '1.0M'],
		[1_500_000, '1.5M']
	])('%d is %s', (n, want) => expect(formatTokens(n)).toBe(want));
});

describe('formatBytes', () => {
	it.each([
		[0, '0 B'],
		[512, '512 B'],
		[1536, '1.5 KB'],
		[84 * 1024 ** 2, '84.0 MB'],
		[17.3 * 1024 ** 3, '17.3 GB'],
		[186 * 1024 ** 3, '186 GB'],
		[-1024, '-1.0 KB']
	])('%d is %s', (n, want) => expect(formatBytes(n)).toBe(want));

	it('honours explicit digits', () => {
		expect(formatBytes(1.39 * 1024 ** 4, 2)).toBe('1.39 TB');
	});

	it('gives a bare figure in GB', () => {
		expect(formatGB(36.5 * 1024 ** 3)).toBe('36.5');
		expect(formatGB(0.27 * 1024 ** 3, 2)).toBe('0.27');
	});
});

describe('formatMs', () => {
	it.each([
		[40, '40 ms'],
		[999, '999 ms'],
		[1240, '1.24 s'],
		[13700, '13.7 s']
	])('%d is %s', (n, want) => expect(formatMs(n)).toBe(want));
});

describe('formatDuration', () => {
	it.each([
		[42, '42 s'],
		[125, '2m 05s'],
		[3720, '1h 02m'],
		[3 * 86400 + 14 * 3600, '3d 14h']
	])('%d is %s', (n, want) => expect(formatDuration(n)).toBe(want));
});

describe('formatUptime', () => {
	it.each([
		[0, '00:00:00'],
		[7, '00:00:07'],
		[14 * 3600 + 22 * 60 + 7, '14:22:07'],
		[3 * 86400 + 14 * 3600 + 22 * 60 + 7, '3d 14:22:07']
	])('%d is %s', (n, want) => expect(formatUptime(n)).toBe(want));
});

describe('rates', () => {
	it('keeps one decimal below a thousand', () => {
		expect(formatRate(38.44)).toBe('38.4');
		expect(formatTokPerSec(38.44)).toBe('38.4 tok/s');
	});

	it('groups thousands without decimals', () => {
		expect(formatRate(2700.4)).toBe('2,700');
		expect(formatTokPerSec(2700.4)).toBe('2,700 tok/s');
		expect(formatInt(2485)).toBe('2,485');
	});
});

it('every formatter shows a dash for missing or broken values', () => {
	for (const f of [
		formatTokens,
		formatBytes,
		formatGB,
		formatMs,
		formatDuration,
		formatUptime,
		formatRate,
		formatTokPerSec,
		formatInt
	]) {
		expect(f(null)).toBe('—');
		expect(f(undefined)).toBe('—');
		expect(f(NaN)).toBe('—');
	}
});

describe('clock times', () => {
	const iso = new Date(2026, 10, 5, 14, 3, 8).toISOString();

	it('reads the local clock', () => {
		expect(formatClock(iso)).toBe('14:03:08');
		expect(formatDayClock(iso)).toBe('11-05 14:03');
	});

	it('shows missing for nonsense', () => {
		expect(formatClock('nope')).toBe('—');
		expect(formatDayClock(null)).toBe('—');
	});
});
