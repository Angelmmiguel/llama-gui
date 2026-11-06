package api

import (
	"net/http"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/telemetry"
)

func (a *api) telemetry(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, a.Telemetry.Snapshot())
}

func (a *api) streamTelemetry(w http.ResponseWriter, r *http.Request) {
	ch, cancel := a.Telemetry.Subscribe()
	defer cancel()
	stream := startSSE(w)
	if stream.send("", 0, a.Telemetry.Snapshot()) != nil {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-a.Done:
			return
		case snap := <-ch:
			if stream.send("", 0, snap) != nil {
				return
			}
		}
	}
}

// historyRanges are the chart ranges, each with a step that leaves a readable number of points.
var historyRanges = map[string]struct{ span, step time.Duration }{
	"5m":  {5 * time.Minute, telemetry.RollupPeriod},
	"15m": {15 * time.Minute, telemetry.RollupPeriod},
	"1h":  {time.Hour, 30 * time.Second},
	"6h":  {6 * time.Hour, 3 * time.Minute},
	"24h": {24 * time.Hour, 12 * time.Minute},
}

type historyPoint struct {
	Decode     float64 `json:"decode"`
	DecodePeak float64 `json:"decode_peak"`
	Prefill    float64 `json:"prefill"`
	TTFT       float64 `json:"ttft"`
	RPM        float64 `json:"rpm"`
	KV         float64 `json:"kv"`
}

func (a *api) telemetryHistory(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("range")
	span, known := historyRanges[name]
	if !known {
		badRequest(w, "range: %q is not 5m, 15m, 1h, 6h or 24h", name)
		return
	}
	// Only finished periods: the current one is still filling, and would read as a sudden drop.
	until := time.Now().UTC().Truncate(span.step)
	since := until.Add(-span.span)
	samples, err := a.Store.TelemetryHistory(r.Context(), since, until, span.step)
	if err != nil {
		a.fail(w, err)
		return
	}
	points := make([]historyPoint, span.span/span.step)
	for _, s := range samples {
		if i := int(s.At.Sub(since) / span.step); i >= 0 && i < len(points) {
			points[i] = historyPoint{s.Decode, s.DecodePeak, s.Prefill, s.TTFT, s.RPM, s.KV}
		}
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"range": name, "step_seconds": int(span.step / time.Second), "until": until, "points": points,
	})
}
