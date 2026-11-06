package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/hardware"
	"github.com/Angelmmiguel/llama-gui/internal/llama"
	"github.com/Angelmmiguel/llama-gui/internal/proxy"
)

type fakeRouter struct {
	mu         sync.Mutex
	models     []llama.Model
	modelsErr  error
	slots      map[string][]llama.Slot
	slotsErr   map[string]error
	metrics    map[string]map[string]float64
	metricsErr map[string]error
	asked      []string
}

func (f *fakeRouter) Models(context.Context) ([]llama.Model, error) { return f.models, f.modelsErr }

func (f *fakeRouter) Slots(_ context.Context, m string) ([]llama.Slot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, "slots:"+m)
	return f.slots[m], f.slotsErr[m]
}

func (f *fakeRouter) Metrics(_ context.Context, m string) (map[string]float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, "metrics:"+m)
	return f.metrics[m], f.metricsErr[m]
}

type fakeRequests struct {
	inflight []proxy.Inflight
	assigned []string
}

func (f *fakeRequests) AssignTask(model string, slot int, task int64) bool {
	f.assigned = append(f.assigned, model+"/"+string(rune('0'+slot))+"/"+string(rune('0'+task)))
	return true
}
func (f *fakeRequests) List() []proxy.Inflight { return f.inflight }

type harness struct {
	s      *Sampler
	router *fakeRouter
	reqs   *fakeRequests
	now    time.Time
	up     bool
	events []string
}

func newHarness() *harness {
	h := &harness{
		router: &fakeRouter{
			slots: map[string][]llama.Slot{}, slotsErr: map[string]error{},
			metrics: map[string]map[string]float64{}, metricsErr: map[string]error{},
		},
		reqs: &fakeRequests{},
		now:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		up:   true,
	}
	h.s = New(Options{
		Router:   func() (Router, bool) { return h.router, h.up },
		State:    func() string { return "running" },
		Requests: h.reqs,
		Clock:    func() time.Time { return h.now },
		Hardware: func(context.Context) hardware.Sample {
			return hardware.Sample{GPUs: []hardware.GPU{gpu(95)}, Memory: hardware.Memory{Total: 100, Available: 40}}
		},
		OnEvent: func(kind, _, _ string, _ map[string]string) { h.events = append(h.events, kind) },
	})
	return h
}

func gpu(usedPct uint64) hardware.GPU {
	total := uint64(100)
	return hardware.GPU{Name: "g", MemoryTotal: &total, MemoryUsed: &usedPct}
}

func (h *harness) tick() *Snapshot {
	h.s.Tick(context.Background())
	snap := h.s.Snapshot()
	h.now = h.now.Add(time.Second)
	return snap
}

func loaded(id string) llama.Model {
	return llama.Model{ID: id, Status: "loaded", Port: 54000, Memory: llama.Memory{Effective: llama.MemoryEstimate{
		Devices: []llama.Device{{Kind: "gpu", Name: "g", Model: 10, Context: 5, Compute: 1}},
	}}}
}

func TestSlotStatesAndSpeed(t *testing.T) {
	h := newHarness()
	h.router.models = []llama.Model{loaded("a")}
	set := func(s llama.Slot) { h.router.slots["a"] = []llama.Slot{s} }

	set(llama.Slot{ID: 0, NCtx: 100})
	if got := h.tick().Models[0].Slots[0]; got.State != StateIdle || got.Task != nil {
		t.Errorf("idle: %+v", got)
	}

	set(llama.Slot{ID: 0, NCtx: 100, IsProcessing: true, TaskID: 5, HasTask: true, NPromptTokens: 40, NCached: 10, NProcessed: 30,
		Params: map[string]any{"temperature": 0.7, "prompt": "secret"}})
	got := h.tick().Models[0].Slots[0]
	if got.State != StatePrefill || got.Speed != 0 || got.Cached != 10 || got.Prompt != 30 || *got.Task != 5 {
		t.Errorf("prefill: %+v", got)
	}
	if got.Params["temperature"] != 0.7 || got.Params["prompt"] != nil {
		t.Errorf("params: %v", got.Params)
	}

	set(llama.Slot{ID: 0, NCtx: 100, IsProcessing: true, TaskID: 5, HasTask: true, NPromptTokens: 60, NCached: 10, NProcessed: 30, NDecoded: 20})
	if got := h.tick().Models[0].Slots[0]; got.State != StateDecode || got.Speed != 20 || got.Generated != 20 {
		t.Errorf("decode: %+v", got)
	}

	set(llama.Slot{ID: 0, NCtx: 100, IsProcessing: true, TaskID: 5, HasTask: true, NPromptTokens: 70, NCached: 10, NProcessed: 30, NDecoded: 30})
	if got := h.tick().Models[0].Slots[0]; got.Speed != 10 {
		t.Errorf("decode delta: %+v", got)
	}

	// A new task's tokens all happened since the last sample, whatever the old one reached.
	set(llama.Slot{ID: 0, NCtx: 100, IsProcessing: true, TaskID: 6, HasTask: true, NPromptTokens: 7, NDecoded: 4})
	if got := h.tick().Models[0].Slots[0]; got.Speed != 4 {
		t.Errorf("new task: %+v", got)
	}

	set(llama.Slot{ID: 0, NCtx: 100, TaskID: 6, HasTask: true, NPromptTokens: 11})
	snap := h.tick()
	if got := snap.Models[0].Slots[0]; got.State != StateIdle || got.Speed != 0 || got.Cached != 11 {
		t.Errorf("idle after: %+v", got)
	}
	hist := snap.SlotHistory[0]
	if hist.Model != "a" || hist.Slot != 0 || len(hist.Samples) != 6 || hist.Samples[1].State != StatePrefill || hist.Samples[2].Speed != 20 {
		t.Errorf("history: %+v", hist)
	}
	if want := "a/0/5 a/0/6"; strings.Join(h.reqs.assigned, " ") != want {
		t.Errorf("assigned %v, want %s", h.reqs.assigned, want)
	}
}

func TestLingeringTaskIsNotAssigned(t *testing.T) {
	h := newHarness()
	h.router.models = []llama.Model{loaded("a")}
	h.router.slots["a"] = []llama.Slot{{ID: 1, TaskID: 9, HasTask: true}}
	h.tick()
	h.tick()
	if len(h.reqs.assigned) != 0 {
		t.Errorf("assigned %v", h.reqs.assigned)
	}
}

func TestThroughputHandlesCounterResetsAndNewModels(t *testing.T) {
	h := newHarness()
	h.router.models = []llama.Model{loaded("a"), loaded("b")}
	set := func(m string, predicted, prompt float64) {
		h.router.metrics[m] = map[string]float64{"tokens_predicted_total": predicted, "prompt_tokens_total": prompt}
	}
	set("a", 1000, 500) // history from before we started must not count
	set("b", 0, 0)
	if got := h.tick().Throughput; got.Decode != 0 || got.Prefill != 0 {
		t.Errorf("first: %+v", got)
	}
	set("a", 1010, 520)
	set("b", 5, 0)
	if got := h.tick().Throughput; got.Decode != 15 || got.Prefill != 20 {
		t.Errorf("sum: %+v", got)
	}
	set("a", 3, 2) // reloaded behind our back
	set("b", 5, 0)
	if got := h.tick().Throughput; got.Decode != 3 || got.Prefill != 2 {
		t.Errorf("reset: %+v", got)
	}

	// Unloaded and loaded again: it starts over rather than computing a delta.
	h.router.models = []llama.Model{loaded("b")}
	h.tick()
	h.router.models = []llama.Model{loaded("a"), loaded("b")}
	set("a", 50, 50)
	if got := h.tick().Throughput; got.Decode != 0 {
		t.Errorf("reload: %+v", got)
	}
}

func TestRateSpansAFailedRead(t *testing.T) {
	h := newHarness()
	h.router.models = []llama.Model{loaded("a")}
	h.router.metrics["a"] = map[string]float64{"tokens_predicted_total": 0}
	h.tick()
	h.router.metricsErr["a"] = errors.New("busy")
	if got := h.tick(); got.Models[0].Error != "busy" || got.Throughput.Decode != 0 {
		t.Errorf("failed: %+v", got)
	}
	h.router.metricsErr["a"] = nil
	h.router.metrics["a"] = map[string]float64{"tokens_predicted_total": 20}
	if got := h.tick().Throughput.Decode; got != 10 {
		t.Errorf("decode = %v, want 20 tokens over 2 s", got)
	}
}

func TestOnlyLoadedModelsArePolledAndFailuresArePerModel(t *testing.T) {
	h := newHarness()
	h.router.models = []llama.Model{
		loaded("a"), loaded("b"),
		{ID: "off", Status: "unloaded"}, {ID: "boot", Status: "loading"},
	}
	h.router.slots["a"] = []llama.Slot{{ID: 0, NCtx: 10, NPromptTokens: 2}}
	h.router.slots["b"] = []llama.Slot{{ID: 0, NCtx: 30, NPromptTokens: 4}}
	h.router.metrics["b"] = map[string]float64{"requests_deferred": 2, "requests_processing": 1}
	h.tick()
	for _, q := range h.router.asked {
		if strings.HasSuffix(q, ":off") || strings.HasSuffix(q, ":boot") {
			t.Errorf("polled %s", q)
		}
	}

	h.router.slotsErr["a"] = errors.New("down")
	snap := h.tick()
	if snap.Models[0].Error != "down" || len(snap.Models[0].Slots) != 1 {
		t.Errorf("a keeps its last slots: %+v", snap.Models[0])
	}
	if snap.Models[1].Error != "" || snap.Models[2].Status != "unloaded" || len(snap.Models[2].Slots) != 0 {
		t.Errorf("rest: %+v", snap.Models)
	}
	if snap.KV != (KV{Used: 6, Total: 40}) {
		t.Errorf("kv: %+v", snap.KV)
	}
	if snap.Requests.Deferred != 2 || snap.Requests.Processing != 1 {
		t.Errorf("requests: %+v", snap.Requests)
	}
	if snap.Models[0].Memory.Total != 16 {
		t.Errorf("memory: %+v", snap.Models[0].Memory)
	}
}

func TestModelListFailureKeepsTheLastList(t *testing.T) {
	h := newHarness()
	h.router.models = []llama.Model{loaded("a")}
	h.tick()
	h.router.modelsErr = errors.New("timeout")
	snap := h.tick()
	if snap.Server.Error != "timeout" || len(snap.Models) != 1 {
		t.Errorf("%+v", snap)
	}
	h.up = false
	snap = h.tick()
	if len(snap.Models) != 0 || len(snap.SlotHistory) != 0 {
		t.Errorf("down: %+v", snap)
	}
}

func TestAggregates(t *testing.T) {
	h := newHarness()
	h.s.ObserveRequest(h.now.Add(-4*time.Minute), 100)
	h.s.ObserveRequest(h.now.Add(-10*time.Second), 200)
	h.s.ObserveRequest(h.now.Add(-5*time.Second), 300)
	h.s.ObserveRequest(h.now.Add(-5*time.Second), 0) // an error never had a first token
	h.s.ObserveRequest(h.now.Add(-time.Hour), 9000)
	got := h.tick().Requests
	if got.PerMinute != 3 || got.TTFTP50Ms != 200 || math.Abs(got.TTFTP95Ms-290) > 1e-9 {
		t.Errorf("%+v", got)
	}
}

func TestObservationsAreBounded(t *testing.T) {
	h := newHarness()
	for range maxObservations + 10 {
		h.s.ObserveRequest(h.now, 1)
	}
	if len(h.s.observed) != maxObservations {
		t.Errorf("kept %d", len(h.s.observed))
	}
}

func TestSeriesAreBoundedRings(t *testing.T) {
	h := newHarness()
	h.router.models = []llama.Model{loaded("a")}
	h.router.slots["a"] = []llama.Slot{{ID: 0, NCtx: 10}}
	var snap *Snapshot
	for range slotWindow + 5 {
		snap = h.tick()
	}
	if len(snap.Series.Decode) != throughputWindow || len(snap.Series.Prefill) != throughputWindow ||
		len(snap.Series.Spark.TTFT) != sparkWindow || len(snap.Series.Spark.KV) != sparkWindow ||
		len(snap.SlotHistory[0].Samples) != slotWindow {
		t.Errorf("%+v", snap.Series)
	}
}

func TestRingOrder(t *testing.T) {
	r := newRing[int](3)
	for i := range 5 {
		r.push(i)
	}
	if got := r.values(); got[0] != 2 || got[2] != 4 {
		t.Errorf("%v", got)
	}
}

func TestSnapshotEncodesEvenWithBadMetrics(t *testing.T) {
	h := newHarness()
	if b, err := json.Marshal(h.s.Snapshot()); err != nil || !strings.Contains(string(b), `"models":[]`) {
		t.Errorf("before the first tick: %s %v", b, err)
	}
	h.router.models = []llama.Model{loaded("a")}
	h.router.metrics["a"] = map[string]float64{"tokens_predicted_total": math.NaN(), "prompt_tokens_total": math.Inf(1)}
	h.tick()
	snap := h.tick()
	if _, err := json.Marshal(snap); err != nil {
		t.Error(err)
	}
}

func TestHubDropsForSlowReaders(t *testing.T) {
	h := newHarness()
	ch, cancel := h.s.Subscribe()
	first := h.tick()
	_ = first
	second := h.tick()
	if got := <-ch; got != second {
		t.Errorf("want the newest snapshot")
	}
	select {
	case <-ch:
		t.Error("only the newest is kept")
	default:
	}
	cancel()
	h.tick()
	select {
	case <-ch:
		t.Error("delivered after cancel")
	default:
	}
}

func TestRollUpRecordsFinishedPeriods(t *testing.T) {
	h := newHarness()
	var got []Rollup
	h.s.opts.Record = func(_ context.Context, r Rollup) error {
		got = append(got, r)
		return nil
	}
	for range 3 {
		h.tick()
		h.now = h.now.Add(time.Second)
	}
	if len(got) != 0 {
		t.Fatalf("recorded a period still filling: %+v", got)
	}
	h.now = h.now.Add(RollupPeriod)
	h.tick()
	if len(got) != 1 || !got[0].At.Equal(h.now.Add(-RollupPeriod).Truncate(RollupPeriod)) {
		t.Errorf("recorded %+v at %v", got, h.now)
	}
}

func TestHardwareEverySecondTickAndVRAMEvent(t *testing.T) {
	h := newHarness()
	calls := 0
	h.s.opts.Hardware = func(context.Context) hardware.Sample {
		calls++
		used := uint64(95)
		if calls == 2 {
			used = 50
		}
		return hardware.Sample{GPUs: []hardware.GPU{gpu(used)}, Memory: hardware.Memory{Total: 8}}
	}
	for range 6 {
		h.tick()
	}
	if calls != 3 {
		t.Errorf("sampled %d times", calls)
	}
	if len(h.events) != 2 || h.events[0] != EventVRAMHigh {
		t.Errorf("events %v: raised once per crossing", h.events)
	}
	if h.s.Snapshot().RAM.Total != 8 {
		t.Errorf("ram: %+v", h.s.Snapshot().RAM)
	}
}

func TestPortToModel(t *testing.T) {
	h := newHarness()
	h.router.models = []llama.Model{loaded("a")}
	h.tick()
	if h.s.PortToModel(54000) != "a" || h.s.PortToModel(1) != "" {
		t.Error("port map")
	}
}

func TestRunTicksUntilCancelled(t *testing.T) {
	h := newHarness()
	h.s.opts.Interval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	ch, stop := h.s.Subscribe()
	defer stop()
	go func() { h.s.Run(ctx); close(done) }()
	<-ch
	<-ch
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestPercentile(t *testing.T) {
	for _, tc := range []struct {
		in   []float64
		p    float64
		want float64
	}{
		{nil, .5, 0},
		{[]float64{7}, .95, 7},
		{[]float64{1, 3}, .5, 2},
		{[]float64{1, 2, 3, 4, 5}, 1, 5},
	} {
		if got := Percentile(tc.in, tc.p); got != tc.want {
			t.Errorf("%v %v = %v", tc.in, tc.p, got)
		}
	}
}

func TestThroughputCountsRunningTasks(t *testing.T) {
	track := &modelTrack{slots: map[int]*slotTrack{}}
	start := time.Unix(1000, 0)
	read := func(sec int, predicted float64, decoded int, processing bool) float64 {
		r := reading{
			metrics: map[string]float64{"tokens_predicted_total": predicted},
			slots:   []llama.Slot{{ID: 0, IsProcessing: processing, NDecoded: decoded}},
		}
		decode, _ := track.applyMetrics(start.Add(time.Duration(sec)*time.Second), r, &Snapshot{})
		return decode
	}
	read(0, 32, 0, false)
	if got := read(1, 32, 100, true); got != 100 {
		t.Errorf("mid-task rate = %v, want 100 although the counter did not move", got)
	}
	if got := read(2, 182, 0, false); got != 50 {
		t.Errorf("rate as the task ends = %v, want 50, not a spike", got)
	}
	if got := read(3, 182, 0, false); got != 0 {
		t.Errorf("idle rate = %v, want 0", got)
	}
	if got := read(4, 10, 0, false); got != 10 {
		t.Errorf("rate after a counter reset = %v, want 10", got)
	}
	if got := read(5, 10, 50, true); got != 50 {
		t.Fatalf("next task rate = %v, want 50", got)
	}
	if got := read(6, 10, 0, false); got != 0 {
		t.Errorf("rate after a cancelled task missing from the counter = %v, want 0", got)
	}
}
