package llama

import (
	"bytes"
	"encoding/json"
)

// Timings is the timings object of the last SSE chunk or of a JSON completion.
type Timings struct {
	CacheN             int     `json:"cache_n"`
	PromptN            int     `json:"prompt_n"`
	PromptMS           float64 `json:"prompt_ms"`
	PromptPerSecond    float64 `json:"prompt_per_second"`
	PredictedN         int     `json:"predicted_n"`
	PredictedMS        float64 `json:"predicted_ms"`
	PredictedPerSecond float64 `json:"predicted_per_second"`
}

// ParseTimings extracts "timings" from a JSON object.
func ParseTimings(b []byte) (Timings, bool) {
	if !bytes.Contains(b, []byte(`"timings"`)) {
		return Timings{}, false
	}
	var d struct {
		Timings *Timings `json:"timings"`
	}
	if json.Unmarshal(b, &d) != nil || d.Timings == nil {
		return Timings{}, false
	}
	return *d.Timings, true
}

// ScanSSELine finds timings in one SSE line of the form "data: {...}".
func ScanSSELine(line []byte) (Timings, bool) {
	rest, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
	if !ok {
		return Timings{}, false
	}
	return ParseTimings(bytes.TrimSpace(rest))
}
