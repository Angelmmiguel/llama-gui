package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/bench"
	"github.com/Angelmmiguel/llama-gui/internal/store"
)

type fakeBench struct {
	current   *bench.Run
	stored    []bench.Run
	startErr  error
	cancelErr error
	started   []bench.Setup
	cancels   int
	listedFor []string
}

func (f *fakeBench) Start(_ context.Context, s bench.Setup) (bench.Run, error) {
	if f.startErr != nil {
		return bench.Run{}, f.startErr
	}
	f.started = append(f.started, s)
	return bench.Run{ID: 9, State: bench.StateRunning, Setup: s}, nil
}

func (f *fakeBench) Current() (bench.Run, bool) {
	if f.current == nil {
		return bench.Run{}, false
	}
	return *f.current, true
}

func (f *fakeBench) Cancel() error {
	f.cancels++
	return f.cancelErr
}

func (f *fakeBench) List(_ context.Context, model string, _ int) ([]bench.Run, error) {
	f.listedFor = append(f.listedFor, model)
	return f.stored, nil
}

func (f *fakeBench) Get(_ context.Context, id int64) (bench.Run, error) {
	for _, r := range f.stored {
		if r.ID == id {
			return r, nil
		}
	}
	return bench.Run{}, store.ErrNotFound
}

func benchHarness(t *testing.T) (*harness, *fakeBench) {
	t.Helper()
	h := newHarness(t)
	f := &fakeBench{}
	deps := h.deps()
	deps.Bench = f
	h.handler = Handler(deps)
	return h, f
}

func tg(fa string, depth int, tps float64) bench.Result {
	return bench.Result{FlashAttn: fa, CacheK: "f16", CacheV: "f16", Kind: "tg", N: 128, Test: "tg128", Depth: depth, TokensPerSec: tps}
}

const validSetup = `{"model_file":"a.gguf","flash_attn":["on","off"],"cache_types":["f16"],"depths":[0,4096],"repetitions":3}`

func TestBenchRunDetail(t *testing.T) {
	h, f := benchHarness(t)
	f.stored = []bench.Run{{ID: 1, State: bench.StateDone, Build: "b1", Setup: bench.Setup{ModelFile: "a.gguf"},
		Results: []bench.Result{tg("on", 0, 200), tg("off", 0, 100)}}}

	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/bench/runs/1", "")
	if code != http.StatusOK || body["build"] != "b1" {
		t.Fatalf("%d %v", code, body)
	}
	if s, _ := body["takeaway"].(string); !strings.Contains(s, "100% faster") {
		t.Errorf("takeaway %q", s)
	}
	if s, _ := body["markdown"].(string); !strings.Contains(s, "200.00") {
		t.Errorf("markdown %q", s)
	}
	series, _ := body["series"].(map[string]any)
	if decode, _ := series["decode"].([]any); len(decode) != 2 {
		t.Errorf("series %v", series)
	}
	if prefill, ok := series["prefill"].([]any); !ok || len(prefill) != 0 {
		t.Errorf("prefill must be an empty list: %v", series["prefill"])
	}

	h.table(t, []route{
		{"missing", http.MethodGet, "/api/v1/bench/runs/2", "", http.StatusNotFound, ""},
		{"bad id", http.MethodGet, "/api/v1/bench/runs/x", "", http.StatusBadRequest, "run id"},
	})
}

func TestBenchRunningHasNoTakeawayAndReadsLive(t *testing.T) {
	h, f := benchHarness(t)
	f.current = &bench.Run{ID: 4, State: bench.StateRunning, Results: []bench.Result{tg("on", 0, 200), tg("off", 0, 100)}}

	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/bench/runs/4", "")
	if code != http.StatusOK || body["state"] != bench.StateRunning || body["takeaway"] != nil {
		t.Errorf("%d %v", code, body)
	}
	code, body = h.sendJSON(t, http.MethodGet, "/api/v1/bench/current", "")
	run, _ := body["run"].(map[string]any)
	if code != http.StatusOK || run["id"] != 4.0 || run["series"] == nil {
		t.Errorf("%d %v", code, body)
	}
}

func TestBenchCurrentBeforeAnyRun(t *testing.T) {
	h, _ := benchHarness(t)
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/bench/current", "")
	if v, ok := body["run"]; code != http.StatusOK || !ok || v != nil {
		t.Errorf("%d %v", code, body)
	}
}

func TestBenchListFiltersByModel(t *testing.T) {
	h, f := benchHarness(t)
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/bench/runs?model=a.gguf", "")
	if runs, ok := body["runs"].([]any); code != http.StatusOK || !ok || len(runs) != 0 {
		t.Errorf("%d %v", code, body)
	}
	if len(f.listedFor) != 1 || f.listedFor[0] != "a.gguf" {
		t.Errorf("listed %v", f.listedFor)
	}
	h.table(t, []route{{"bad limit", http.MethodGet, "/api/v1/bench/runs?limit=0", "", http.StatusBadRequest, "limit"}})
}

func TestBenchStart(t *testing.T) {
	h, f := benchHarness(t)
	code, body := h.sendJSON(t, http.MethodPost, "/api/v1/bench/runs", validSetup)
	if code != http.StatusAccepted || body["id"] != 9.0 {
		t.Fatalf("%d %v", code, body)
	}
	if len(f.started) != 1 || f.started[0].ModelFile != "a.gguf" || f.started[0].Repetitions != 3 {
		t.Errorf("started %+v", f.started)
	}
	h.table(t, []route{{"unknown field", http.MethodPost, "/api/v1/bench/runs", `{"nope":1}`, http.StatusBadRequest, "invalid JSON"}})
	for _, tc := range []struct {
		err    error
		status int
	}{
		{bench.ErrBusy, http.StatusConflict},
		{bench.ErrUnsupported, http.StatusConflict},
		{bench.ErrInvalid, http.StatusBadRequest},
		{bench.ErrModel, http.StatusNotFound},
	} {
		f.startErr = tc.err
		if code, body := h.sendJSON(t, http.MethodPost, "/api/v1/bench/runs", validSetup); code != tc.status {
			t.Errorf("%v: %d %v", tc.err, code, body)
		}
	}
}

func TestBenchCancel(t *testing.T) {
	h, f := benchHarness(t)
	if w := h.send(t, http.MethodDelete, "/api/v1/bench/current", ""); w.Code != http.StatusNoContent || f.cancels != 1 {
		t.Errorf("%d, %d cancels", w.Code, f.cancels)
	}
	f.cancelErr = bench.ErrNotRunning
	h.table(t, []route{{"nothing running", http.MethodDelete, "/api/v1/bench/current", "", http.StatusConflict, "no run"}})
}

func TestBenchEstimate(t *testing.T) {
	h, _ := benchHarness(t)
	code, body := h.sendJSON(t, http.MethodPost, "/api/v1/bench/estimate", validSetup)
	if code != http.StatusOK || body["tests"] != 8.0 || body["seconds"] != 58.0 {
		t.Fatalf("%d %v", code, body)
	}
	if s, _ := body["command"].(string); !strings.Contains(s, "llama-bench -m a.gguf") || !strings.Contains(s, "-fa on,off") {
		t.Errorf("command %q", s)
	}
	h.table(t, []route{
		{"no model", http.MethodPost, "/api/v1/bench/estimate", `{}`, http.StatusBadRequest, "model file"},
		{"traversal", http.MethodPost, "/api/v1/bench/estimate", `{"model_file":"../a.gguf"}`, http.StatusBadRequest, "model file"},
	})
}

func TestBenchHistory(t *testing.T) {
	h, f := benchHarness(t)
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.stored = []bench.Run{
		{ID: 2, State: bench.StateDone, Build: "b2", StartedAt: day.Add(time.Hour), Setup: bench.Setup{ModelFile: "a.gguf"}, Results: []bench.Result{tg("on", 0, 150)}},
		{ID: 1, State: bench.StateDone, Build: "b1", StartedAt: day, Setup: bench.Setup{ModelFile: "a.gguf"}, Results: []bench.Result{tg("on", 0, 100)}},
	}
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/bench/history?model=a.gguf", "")
	points, _ := body["history"].([]any)
	if code != http.StatusOK || len(points) != 2 {
		t.Fatalf("%d %v", code, body)
	}
	first, _ := points[0].(map[string]any)
	second, _ := points[1].(map[string]any)
	if first["build"] != "b1" || first["delta_percent"] != nil || second["delta_percent"] != 50.0 {
		t.Errorf("%v", points)
	}
	h.table(t, []route{{"no model", http.MethodGet, "/api/v1/bench/history", "", http.StatusBadRequest, "model"}})

	code, body = h.sendJSON(t, http.MethodGet, "/api/v1/bench/history?model=other.gguf", "")
	if list, ok := body["history"].([]any); code != http.StatusOK || !ok || len(list) != 0 {
		t.Errorf("no runs must be an empty list: %d %v", code, body)
	}
}
