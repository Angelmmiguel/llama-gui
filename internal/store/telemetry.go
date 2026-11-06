package store

import (
	"context"
	"fmt"
	"time"
)

// TelemetrySample is the dashboard's numbers over one period.
type TelemetrySample struct {
	At         time.Time
	Decode     float64
	DecodePeak float64
	Prefill    float64
	TTFT       float64
	RPM        float64
	KV         float64
}

// InsertTelemetrySample stores a sample, replacing any at the same time, as a restart can land in
// the period it left.
func (s *Store) InsertTelemetrySample(ctx context.Context, t TelemetrySample) error {
	_, err := s.write.ExecContext(ctx,
		`INSERT OR REPLACE INTO telemetry_samples (ts, decode, decode_peak, prefill, ttft, rpm, kv) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.At.UnixMicro(), t.Decode, t.DecodePeak, t.Prefill, t.TTFT, t.RPM, t.KV)
	if err != nil {
		return fmt.Errorf("store: insert telemetry sample: %w", err)
	}
	return nil
}

// TelemetryHistory averages the samples in [since, until) over periods of step, oldest first. Each
// result's At is its period's start, and a period with no samples has no result. Quiet periods don't
// count towards TTFT, as they have nothing to measure.
func (s *Store) TelemetryHistory(ctx context.Context, since, until time.Time, step time.Duration) ([]TelemetrySample, error) {
	width := step.Microseconds()
	if width <= 0 {
		return nil, fmt.Errorf("store: telemetry history: step %s is too small", step)
	}
	rows, err := s.read.QueryContext(ctx, `
		SELECT ts / ?1 * ?1 AS period, AVG(decode), MAX(decode_peak), AVG(prefill),
		       COALESCE(AVG(NULLIF(ttft, 0)), 0), AVG(rpm), AVG(kv)
		FROM telemetry_samples
		WHERE ts >= ?2 AND ts < ?3
		GROUP BY period
		ORDER BY period`, width, since.UnixMicro(), until.UnixMicro())
	if err != nil {
		return nil, fmt.Errorf("store: telemetry history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []TelemetrySample{}
	for rows.Next() {
		var t TelemetrySample
		var at int64
		if err := rows.Scan(&at, &t.Decode, &t.DecodePeak, &t.Prefill, &t.TTFT, &t.RPM, &t.KV); err != nil {
			return nil, fmt.Errorf("store: telemetry history: %w", err)
		}
		t.At = time.UnixMicro(at).UTC()
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: telemetry history: %w", err)
	}
	return out, nil
}
