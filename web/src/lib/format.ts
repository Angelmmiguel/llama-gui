const MISSING = '—';

function isNumber(n: number | null | undefined): n is number {
	return typeof n === 'number' && Number.isFinite(n);
}

function pad(n: number): string {
	return String(n).padStart(2, '0');
}

/** Integer with thousands separators: `2,485`. */
export function formatInt(n: number | null | undefined): string {
	return isNumber(n) ? Math.round(n).toLocaleString('en-US') : MISSING;
}

/** Token counts: `512`, `1.2k`, `62.9k`, `1.5M`. */
export function formatTokens(n: number | null | undefined): string {
	if (!isNumber(n)) return MISSING;
	const abs = Math.abs(n);
	if (abs < 1000) return String(Math.round(n));
	// Rounding may reach the next unit: 999,950 reads 1.0M rather than 1000.0k.
	if (abs < 999_950) return `${(n / 1000).toFixed(1)}k`;
	return `${(n / 1_000_000).toFixed(1)}M`;
}

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];

/** Binary-scaled sizes in the design's units: `512 B`, `84.0 MB`, `17.3 GB`, `186 GB`. */
export function formatBytes(n: number | null | undefined, digits?: number): string {
	if (!isNumber(n)) return MISSING;
	const sign = n < 0 ? '-' : '';
	let value = Math.abs(n);
	let unit = 0;
	while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
		value /= 1024;
		unit++;
	}
	const places = digits ?? (unit === 0 || value >= 100 ? 0 : 1);
	return `${sign}${value.toFixed(places)} ${BYTE_UNITS[unit]}`;
}

/** Bytes as a bare GB figure, for `36.5 / 48 GB` style pairs. */
export function formatGB(n: number | null | undefined, digits = 1): string {
	return isNumber(n) ? (n / 1024 ** 3).toFixed(digits) : MISSING;
}

/** Sub-minute timings from milliseconds: `40 ms`, `1.24 s`, `13.7 s`. */
export function formatMs(ms: number | null | undefined): string {
	if (!isNumber(ms)) return MISSING;
	if (Math.abs(ms) < 1000) return `${Math.round(ms)} ms`;
	const s = ms / 1000;
	return `${s.toFixed(Math.abs(s) < 10 ? 2 : 1)} s`;
}

/** Long durations from seconds: `42 s`, `2m 05s`, `1h 02m`, `3d 14h`. */
export function formatDuration(seconds: number | null | undefined): string {
	if (!isNumber(seconds) || seconds < 0) return MISSING;
	const total = Math.round(seconds);
	if (total < 60) return `${total} s`;
	if (total < 3600) return `${Math.floor(total / 60)}m ${pad(total % 60)}s`;
	if (total < 86400) return `${Math.floor(total / 3600)}h ${pad(Math.floor((total % 3600) / 60))}m`;
	return `${Math.floor(total / 86400)}d ${pad(Math.floor((total % 86400) / 3600))}h`;
}

/** Uptime clock: `14:22:07`, or `3d 14:22:07` from the first full day. */
export function formatUptime(seconds: number | null | undefined): string {
	if (!isNumber(seconds) || seconds < 0) return MISSING;
	const total = Math.floor(seconds);
	const days = Math.floor(total / 86400);
	const clock = `${pad(Math.floor((total % 86400) / 3600))}:${pad(Math.floor((total % 3600) / 60))}:${pad(total % 60)}`;
	return days > 0 ? `${days}d ${clock}` : clock;
}

/** The bare rate, as tiles show it: `38.4`, `2,700`. */
export function formatRate(n: number | null | undefined): string {
	if (!isNumber(n)) return MISSING;
	return Math.abs(n) < 1000 ? n.toFixed(1) : formatInt(n);
}

/** Rate with its unit: `38.4 tok/s`, `2,700 tok/s`. */
export function formatTokPerSec(n: number | null | undefined): string {
	return isNumber(n) ? `${formatRate(n)} tok/s` : MISSING;
}

/** Local wall clock from an ISO time: `14:32:08`. */
export function formatClock(iso: string | null | undefined): string {
	const d = new Date(iso ?? NaN);
	if (Number.isNaN(d.getTime())) return MISSING;
	return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

/** Local date and minute from an ISO time, for lists spanning days: `11-05 14:32`. */
export function formatDayClock(iso: string | null | undefined): string {
	const d = new Date(iso ?? NaN);
	if (Number.isNaN(d.getTime())) return MISSING;
	return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
