export interface ChartPaths {
	/** Stroke path, empty when there is nothing to draw. */
	line: string;
	/** The line closed along the bottom edge, for a fill. */
	area: string;
	/** The y of the last point, in view coordinates. */
	endY: number;
}

/**
 * Maps values onto a w×h view, evenly spaced across the full width. Values are
 * clamped to the range, and non-finite ones are dropped because telemetry can
 * have gaps. A single value draws as a flat line.
 */
export function chartPaths(
	values: readonly number[],
	w: number,
	h: number,
	min?: number,
	max?: number
): ChartPaths {
	const finite = values.filter(Number.isFinite);
	if (finite.length === 0) return { line: '', area: '', endY: h };

	const lo = min ?? Math.min(...finite);
	const hi = max ?? Math.max(...finite);
	const span = hi - lo || 1;
	const y = (v: number) => h - ((Math.max(lo, Math.min(hi, v)) - lo) / span) * h;

	const points = finite.length === 1 ? [finite[0], finite[0]] : finite;
	const last = points.length - 1;
	const line =
		'M' + points.map((v, i) => `${((i / last) * w).toFixed(1)},${y(v).toFixed(1)}`).join(' L');
	return { line, area: `${line} L${w},${h} L0,${h} Z`, endY: y(points[last]) };
}
