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
 * have gaps. A single value draws as a flat line. Given slots, the values take that
 * many positions and a shorter line stops early, as one still being measured does.
 */
export function chartPaths(
	values: readonly number[],
	w: number,
	h: number,
	min?: number,
	max?: number,
	slots?: number
): ChartPaths {
	const finite = values.filter(Number.isFinite);
	if (finite.length === 0) return { line: '', area: '', endY: h };

	const lo = min ?? Math.min(...finite);
	const hi = max ?? Math.max(...finite);
	const span = hi - lo || 1;
	const y = (v: number) => h - ((Math.max(lo, Math.min(hi, v)) - lo) / span) * h;

	const points = finite.length === 1 && !slots ? [finite[0], finite[0]] : finite;
	const last = Math.max((slots ?? points.length) - 1, 1);
	const x = (i: number) => (i / last) * w;
	const line = 'M' + points.map((v, i) => `${x(i).toFixed(1)},${y(v).toFixed(1)}`).join(' L');
	const end = slots ? x(points.length - 1).toFixed(1) : w;
	return {
		line,
		area: `${line} L${end},${h} L0,${h} Z`,
		endY: y(points[points.length - 1])
	};
}
