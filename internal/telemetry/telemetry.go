// Package telemetry samples the router once a second, whether or not anyone is
// watching, and keeps the short histories the dashboard charts need.
package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/hardware"
	"github.com/Angelmmiguel/llama-gui/internal/llama"
	"github.com/Angelmmiguel/llama-gui/internal/proxy"
)

const (
	throughputWindow = 60
	sparkWindow      = 30
	slotWindow       = 90

	rpmWindow       = time.Minute
	ttftWindow      = 5 * time.Minute
	maxObservations = 4096

	defaultInterval = time.Second
	hardwareEvery   = 2
	// tickTimeout keeps a stuck upstream from stretching ticks, since /slots can
	// wait behind a long decode.
	tickTimeout = 900 * time.Millisecond

	vramHigh  = 0.90
	vramClear = 0.85

	statusLoaded = "loaded"
)

// Slot states.
const (
	StateIdle    = "idle"
	StatePrefill = "prefill"
	StateDecode  = "decode"
)

// EventVRAMHigh is the kind of the event raised when a GPU's memory fills up.
const EventVRAMHigh = "gpu.vram_high"

// Router is the part of the router's API the sampler reads.
type Router interface {
	Models(ctx context.Context) ([]llama.Model, error)
	Slots(ctx context.Context, model string) ([]llama.Slot, error)
	Metrics(ctx context.Context, model string) (map[string]float64, error)
}

// Requests is the part of the proxy the sampler reads and informs.
type Requests interface {
	AssignTask(model string, slot int, task int64) bool
	List() []proxy.Inflight
}

// Options configures a Sampler. Router and State are required.
type Options struct {
	// Router returns the router's client, or false while it is not running.
	Router   func() (Router, bool)
	State    func() string
	Hardware func(context.Context) hardware.Sample
	Requests Requests
	// OnEvent receives conditions only the sampler can see.
	OnEvent func(kind, severity, title string, meta map[string]string)
	// Record stores each finished Rollup. Nil keeps no history.
	Record   func(ctx context.Context, r Rollup) error
	Clock    func() time.Time
	Interval time.Duration
	Logger   *slog.Logger
}

// Snapshot is everything the dashboard shows live.
type Snapshot struct {
	Time        time.Time        `json:"time"`
	Server      Server           `json:"server"`
	Models      []Model          `json:"models"`
	Throughput  Throughput       `json:"throughput"`
	Requests    RequestStats     `json:"requests"`
	KV          KV               `json:"kv"`
	GPUs        []hardware.GPU   `json:"gpus"`
	RAM         hardware.Memory  `json:"ram"`
	InFlight    []proxy.Inflight `json:"in_flight"`
	Series      Series           `json:"series"`
	SlotHistory []SlotHistory    `json:"slot_history"`
}

// Server is the router's state.
type Server struct {
	State string `json:"state"`
	// Error says why the last model list could not be read.
	Error string `json:"error,omitempty"`
}

// Model is one of the router's models. Slots are only known while it is loaded.
type Model struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Memory Memory `json:"memory"`
	Slots  []Slot `json:"slots"`
	// Error says why this tick could not read the model; Slots then hold the
	// last good reading.
	Error string `json:"error,omitempty"`
}

// Memory is the measured use of a loaded model, in bytes.
type Memory struct {
	Total   int64          `json:"total"`
	Devices []llama.Device `json:"devices"`
}

// Slot is one slot of a loaded model.
type Slot struct {
	ID        int    `json:"id"`
	State     string `json:"state"`
	Cached    int    `json:"cached"`
	Prompt    int    `json:"prompt"`
	Generated int    `json:"generated"`
	Total     int    `json:"total"`
	NCtx      int    `json:"n_ctx"`
	// Task stays set after a task finishes, as upstream's id_task does.
	Task   *int64         `json:"task"`
	Speed  float64        `json:"speed"`
	Params map[string]any `json:"params,omitempty"`
}

// Throughput is tokens per second across all models.
type Throughput struct {
	Decode  float64 `json:"decode"`
	Prefill float64 `json:"prefill"`
}

// RequestStats are the rolling request aggregates. TTFT covers the last five
// minutes, so it says something on a quiet server too.
type RequestStats struct {
	InFlight   int     `json:"in_flight"`
	Processing int     `json:"processing"`
	Deferred   int     `json:"deferred"`
	PerMinute  float64 `json:"per_minute"`
	TTFTP50Ms  float64 `json:"ttft_p50_ms"`
	TTFTP95Ms  float64 `json:"ttft_p95_ms"`
}

// KV is the sum of slot n_prompt_tokens over the sum of n_ctx.
type KV struct {
	Used  int `json:"used"`
	Total int `json:"total"`
}

// Series are the chart histories, oldest first.
type Series struct {
	Decode  []float64 `json:"decode"`
	Prefill []float64 `json:"prefill"`
	Spark   Spark     `json:"spark"`
}

// Spark are the 30 s tile sparklines.
type Spark struct {
	Prefill []float64 `json:"prefill"`
	TTFT    []float64 `json:"ttft"`
	RPM     []float64 `json:"rpm"`
	KV      []float64 `json:"kv"`
}

// SlotHistory is one slot's last 90 s, oldest first, for the heatmap.
type SlotHistory struct {
	Model   string       `json:"model"`
	Slot    int          `json:"slot"`
	Samples []SlotSample `json:"samples"`
}

// SlotSample is one second of a slot.
type SlotSample struct {
	State string  `json:"state"`
	Speed float64 `json:"speed"`
}

// Sampler is the 1 Hz sampling loop and the hub publishing what it finds.
type Sampler struct {
	opts Options
	log  *slog.Logger

	// Everything below is only touched by Tick, except where mu says otherwise.
	ticks  int
	tracks map[string]*modelTrack
	models []llama.Model
	sample hardware.Sample
	warned map[int]bool

	decode, prefill                            *ring[float64]
	sparkPrefill, sparkTTFT, sparkRPM, sparkKV *ring[float64]

	rollup        Rollup
	rollupSamples int

	mu       sync.Mutex
	latest   *Snapshot
	ports    map[int]string
	observed []observation
	nextSub  int
	subs     map[int]chan *Snapshot
}

type modelTrack struct {
	counters              time.Time // when predicted and prompt were read
	predicted             float64
	prompt                float64
	runDecoded, runPrompt float64 // tokens of tasks still running at that reading
	slots                 map[int]*slotTrack
	last                  []Slot
}

type slotTrack struct {
	at      time.Time
	task    int64
	hasTask bool
	decoded int
	history *ring[SlotSample]
}

type observation struct {
	at     time.Time
	ttftMs float64
}

// New returns a Sampler. Run starts it.
func New(opts Options) *Sampler {
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Interval <= 0 {
		opts.Interval = defaultInterval
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Sampler{
		opts: opts, log: log,
		tracks: map[string]*modelTrack{}, warned: map[int]bool{},
		decode: newRing[float64](throughputWindow), prefill: newRing[float64](throughputWindow),
		sparkPrefill: newRing[float64](sparkWindow), sparkTTFT: newRing[float64](sparkWindow),
		sparkRPM: newRing[float64](sparkWindow), sparkKV: newRing[float64](sparkWindow),
		ports: map[int]string{}, subs: map[int]chan *Snapshot{},
	}
}

// Run samples every interval until ctx ends.
func (s *Sampler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.opts.Interval)
	defer ticker.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// reading is what one tick got from one loaded model.
type reading struct {
	slots      []llama.Slot
	slotsErr   error
	metrics    map[string]float64
	metricsErr error
}

// Tick takes one sample and publishes it. Calls must not overlap.
func (s *Sampler) Tick(ctx context.Context) {
	now := s.opts.Clock()
	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()

	var modelsErr error
	router, up := s.opts.Router()
	if !up {
		s.models = nil
	} else if models, err := router.Models(ctx); err != nil {
		// Keeping the last list means one slow answer does not blank the page.
		modelsErr = err
	} else {
		s.models = models
	}

	readings := make([]reading, len(s.models))
	var wg sync.WaitGroup
	for i, m := range s.models {
		if !up || m.Status != statusLoaded {
			continue
		}
		wg.Go(func() {
			r := &readings[i]
			r.slots, r.slotsErr = router.Slots(ctx, m.ID)
			r.metrics, r.metricsErr = router.Metrics(ctx, m.ID)
		})
	}
	if s.opts.Hardware != nil && s.ticks%hardwareEvery == 0 {
		wg.Go(func() { s.sample = s.opts.Hardware(ctx) })
	}
	wg.Wait()
	s.ticks++

	snap := s.compute(now, modelsErr, readings)
	s.rollUp(ctx, now, snap)
	s.checkVRAM()
	s.publish(snap)
}

// RollupPeriod is how often the history gets a new point.
const RollupPeriod = 10 * time.Second

// Rollup is the numbers of one RollupPeriod, for the history the chart ranges read.
type Rollup struct {
	At         time.Time
	Decode     float64
	DecodePeak float64
	Prefill    float64
	TTFT       float64
	RPM        float64
	KV         float64
}

// rollUp adds a sample to the current period, and records the period once time has moved past it.
func (s *Sampler) rollUp(ctx context.Context, now time.Time, snap *Snapshot) {
	if s.opts.Record == nil {
		return
	}
	period := now.Truncate(RollupPeriod)
	if !period.Equal(s.rollup.At) {
		if s.rollupSamples > 0 {
			done := s.rollup
			done.Decode /= float64(s.rollupSamples)
			done.Prefill /= float64(s.rollupSamples)
			if err := s.opts.Record(ctx, done); err != nil {
				s.log.Warn("recording telemetry history", "error", err)
			}
		}
		s.rollup = Rollup{At: period}
		s.rollupSamples = 0
	}
	s.rollupSamples++
	s.rollup.Decode += snap.Throughput.Decode
	s.rollup.DecodePeak = max(s.rollup.DecodePeak, snap.Throughput.Decode)
	s.rollup.Prefill += snap.Throughput.Prefill
	s.rollup.TTFT = snap.Requests.TTFTP50Ms
	s.rollup.RPM = snap.Requests.PerMinute
	if snap.KV.Total > 0 {
		s.rollup.KV = float64(snap.KV.Used) / float64(snap.KV.Total)
	}
}

func (s *Sampler) compute(now time.Time, modelsErr error, readings []reading) *Snapshot {
	snap := &Snapshot{
		Time:   now,
		Server: Server{State: s.opts.State()},
		Models: make([]Model, 0, len(s.models)),
		GPUs:   s.sample.GPUs,
		RAM:    s.sample.Memory,
	}
	if snap.GPUs == nil {
		snap.GPUs = []hardware.GPU{}
	}
	if modelsErr != nil {
		snap.Server.Error = modelsErr.Error()
	}

	var decode, prefill float64
	ports := map[int]string{}
	seen := map[string]bool{}
	for i, m := range s.models {
		if m.Port > 0 {
			ports[m.Port] = m.ID
		}
		out := Model{ID: m.ID, Status: m.Status, Memory: memoryOf(m), Slots: []Slot{}}
		if m.Status == statusLoaded {
			seen[m.ID] = true
			t := s.tracks[m.ID]
			if t == nil {
				t = &modelTrack{slots: map[int]*slotTrack{}}
				s.tracks[m.ID] = t
			}
			r := readings[i]
			d, p := t.applyMetrics(now, r, snap)
			decode, prefill = decode+d, prefill+p
			s.applySlots(now, m.ID, t, r, &out)
			if err := firstErr(r.slotsErr, r.metricsErr); err != nil {
				out.Error = err.Error()
			}
			for _, sl := range out.Slots {
				snap.KV.Used += sl.Total
				snap.KV.Total += sl.NCtx
			}
		}
		snap.Models = append(snap.Models, out)
	}
	// A model that is gone starts from scratch if it comes back, so its counter
	// restart is never mistaken for a delta.
	maps.DeleteFunc(s.tracks, func(id string, _ *modelTrack) bool { return !seen[id] })

	snap.Throughput = Throughput{Decode: decode, Prefill: prefill}
	if s.opts.Requests != nil {
		snap.InFlight = s.opts.Requests.List()
	}
	if snap.InFlight == nil {
		snap.InFlight = []proxy.Inflight{}
	}
	snap.Requests.InFlight = len(snap.InFlight)

	s.mu.Lock()
	s.ports = ports
	rpm, p50, p95 := s.aggregates(now)
	s.mu.Unlock()
	snap.Requests.PerMinute, snap.Requests.TTFTP50Ms, snap.Requests.TTFTP95Ms = rpm, p50, p95

	s.decode.push(decode)
	s.prefill.push(prefill)
	s.sparkPrefill.push(prefill)
	s.sparkTTFT.push(p50)
	s.sparkRPM.push(rpm)
	var kv float64
	if snap.KV.Total > 0 {
		kv = float64(snap.KV.Used) / float64(snap.KV.Total)
	}
	s.sparkKV.push(kv)
	snap.Series = Series{
		Decode: s.decode.values(), Prefill: s.prefill.values(),
		Spark: Spark{Prefill: s.sparkPrefill.values(), TTFT: s.sparkTTFT.values(), RPM: s.sparkRPM.values(), KV: s.sparkKV.values()},
	}
	snap.SlotHistory = s.slotHistory()
	return snap
}

// applyMetrics returns the decode and prefill rates since the last good reading.
// Upstream only adds a task to its counters when the task ends, so the tokens of
// tasks still running are added from the slots: as a task ends its tokens move from
// one term to the other, and the sum keeps rising smoothly.
func (t *modelTrack) applyMetrics(now time.Time, r reading, snap *Snapshot) (decode, prefill float64) {
	if r.metricsErr != nil || r.metrics == nil {
		return 0, 0
	}
	predicted, prompt := metric(r.metrics, "tokens_predicted_total"), metric(r.metrics, "prompt_tokens_total")
	snap.Requests.Processing += int(metric(r.metrics, "requests_processing"))
	snap.Requests.Deferred += int(metric(r.metrics, "requests_deferred"))
	runDecoded, runPrompt := t.runDecoded, t.runPrompt
	if r.slotsErr == nil {
		runDecoded, runPrompt = 0, 0
		for _, sl := range r.slots {
			if sl.IsProcessing {
				runDecoded += float64(sl.NDecoded)
				runPrompt += float64(sl.NProcessed)
			}
		}
	}
	if !t.counters.IsZero() {
		dt := now.Sub(t.counters).Seconds()
		decode = tokenRate(t.predicted, predicted, t.runDecoded, runDecoded, dt)
		prefill = tokenRate(t.prompt, prompt, t.runPrompt, runPrompt, dt)
	}
	t.counters, t.predicted, t.prompt, t.runDecoded, t.runPrompt = now, predicted, prompt, runDecoded, runPrompt
	return decode, prefill
}

// metric reads a value, which ParseFloat lets be NaN or infinite; those would
// break the JSON encoding of the whole snapshot.
func metric(m map[string]float64, name string) float64 {
	v := m[name]
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// tokenRate is the per-second increase of a counter plus the tokens of running
// tasks. A counter that went down was reset, so everything it now holds is new. A
// running total can only fall short when a cancelled task never reaches the counter.
func tokenRate(prevCounter, counter, prevRunning, running, seconds float64) float64 {
	if seconds <= 0 {
		return 0
	}
	delta := counter - prevCounter
	if delta < 0 {
		delta = counter
	}
	return max(delta+running-prevRunning, 0) / seconds
}

func (s *Sampler) applySlots(now time.Time, model string, t *modelTrack, r reading, out *Model) {
	if r.slotsErr != nil {
		out.Slots = t.last
		if out.Slots == nil {
			out.Slots = []Slot{}
		}
		return
	}
	alive := map[int]bool{}
	for _, sl := range r.slots {
		alive[sl.ID] = true
		st := t.slots[sl.ID]
		if st == nil {
			st = &slotTrack{history: newRing[SlotSample](slotWindow)}
			t.slots[sl.ID] = st
		}
		state := StateIdle
		var speed float64
		if sl.IsProcessing {
			state = StatePrefill
			if sl.NDecoded > 0 {
				state = StateDecode
			}
			newTask := sl.HasTask && (!st.hasTask || st.task != int64(sl.TaskID))
			if newTask && s.opts.Requests != nil {
				s.opts.Requests.AssignTask(model, sl.ID, int64(sl.TaskID))
			}
			if !st.at.IsZero() {
				prev := st.decoded
				if newTask {
					prev = 0
				}
				if dt := now.Sub(st.at).Seconds(); dt > 0 && sl.NDecoded > prev {
					speed = float64(sl.NDecoded-prev) / dt
				}
			}
		}
		// A task is only recorded once seen processing: id_task lingers on idle
		// slots, and a request must not be matched to a task that has finished.
		if sl.IsProcessing && sl.HasTask {
			st.task, st.hasTask = int64(sl.TaskID), true
		}
		st.at, st.decoded = now, sl.NDecoded
		st.history.push(SlotSample{State: state, Speed: speed})

		tokens := sl.Tokens()
		slot := Slot{
			ID: sl.ID, State: state, Cached: tokens.Cached, Prompt: tokens.Prompt, Generated: tokens.Generated,
			Total: tokens.Total, NCtx: sl.NCtx, Speed: speed, Params: paramSubset(sl.Params),
		}
		if sl.HasTask {
			task := int64(sl.TaskID)
			slot.Task = &task
		}
		out.Slots = append(out.Slots, slot)
	}
	maps.DeleteFunc(t.slots, func(id int, _ *slotTrack) bool { return !alive[id] })
	t.last = out.Slots
}

// paramKeys is an allow list: params can grow new fields, and a future one
// could carry prompt text.
var paramKeys = []string{"n_predict", "max_tokens", "temperature", "top_k", "top_p", "min_p", "repeat_penalty", "seed", "stream"}

func paramSubset(params map[string]any) map[string]any {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]any, len(paramKeys))
	for _, k := range paramKeys {
		if v, ok := params[k]; ok {
			out[k] = v
		}
	}
	return out
}

func memoryOf(m llama.Model) Memory {
	mem := Memory{Devices: m.Memory.Effective.Devices}
	if mem.Devices == nil {
		mem.Devices = []llama.Device{}
	}
	for _, d := range mem.Devices {
		mem.Total += d.Model + d.Context + d.Compute
	}
	return mem
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Sampler) slotHistory() []SlotHistory {
	out := []SlotHistory{}
	for id, t := range s.tracks {
		for slot, st := range t.slots {
			out = append(out, SlotHistory{Model: id, Slot: slot, Samples: st.history.values()})
		}
	}
	slices.SortFunc(out, func(a, b SlotHistory) int {
		if a.Model != b.Model {
			if a.Model < b.Model {
				return -1
			}
			return 1
		}
		return a.Slot - b.Slot
	})
	return out
}

func (s *Sampler) checkVRAM() {
	for _, g := range s.sample.GPUs {
		if g.MemoryTotal == nil || g.MemoryUsed == nil || *g.MemoryTotal == 0 {
			continue
		}
		use := float64(*g.MemoryUsed) / float64(*g.MemoryTotal)
		switch {
		case use >= vramHigh && !s.warned[g.Index]:
			s.warned[g.Index] = true
			if s.opts.OnEvent != nil {
				s.opts.OnEvent(EventVRAMHigh, "warn", fmt.Sprintf("GPU %d memory above 90%%", g.Index), map[string]string{"gpu": g.Name})
			}
		case use < vramClear:
			delete(s.warned, g.Index)
		}
	}
}

// ObserveRequest feeds a finished request into the rolling aggregates. It runs
// on the request path and does not block on anything slow.
func (s *Sampler) ObserveRequest(at time.Time, ttftMs float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.observed) >= maxObservations {
		s.observed = s.observed[1:]
	}
	s.observed = append(s.observed, observation{at, ttftMs})
}

// aggregates drops what left the window and summarises the rest. mu is held.
func (s *Sampler) aggregates(now time.Time) (rpm, p50, p95 float64) {
	s.observed = slices.DeleteFunc(s.observed, func(o observation) bool { return now.Sub(o.at) > ttftWindow })
	var ttfts []float64
	for _, o := range s.observed {
		if now.Sub(o.at) <= rpmWindow {
			rpm++
		}
		if o.ttftMs > 0 {
			ttfts = append(ttfts, o.ttftMs)
		}
	}
	slices.Sort(ttfts)
	return rpm, Percentile(ttfts, 0.50), Percentile(ttfts, 0.95)
}

// Percentile interpolates the p (0 to 1) percentile of sorted values, and is 0
// when there are none.
func Percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := p * float64(len(sorted)-1)
	lo := int(pos)
	if lo >= len(sorted)-1 {
		return sorted[len(sorted)-1]
	}
	return sorted[lo] + (sorted[lo+1]-sorted[lo])*(pos-float64(lo))
}

func (s *Sampler) publish(snap *Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latest = snap
	for _, ch := range s.subs {
		// The channel holds one snapshot: a slow reader skips to the newest
		// rather than holding anyone up. Tick is the only sender.
		select {
		case <-ch:
		default:
		}
		ch <- snap
	}
}

// Snapshot returns the latest sample, or an empty one before the first tick.
// Treat it as read-only: subscribers share it.
func (s *Sampler) Snapshot() *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest != nil {
		return s.latest
	}
	return &Snapshot{
		Time: s.opts.Clock(), Server: Server{State: s.opts.State()},
		Models: []Model{}, GPUs: []hardware.GPU{}, InFlight: []proxy.Inflight{}, SlotHistory: []SlotHistory{},
		Series: Series{Decode: []float64{}, Prefill: []float64{}, Spark: Spark{Prefill: []float64{}, TTFT: []float64{}, RPM: []float64{}, KV: []float64{}}},
	}
}

// Subscribe returns a channel receiving each new snapshot, skipping ahead when
// the reader is slow, and a function ending the subscription.
func (s *Sampler) Subscribe() (<-chan *Snapshot, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextSub
	s.nextSub++
	ch := make(chan *Snapshot, 1)
	s.subs[id] = ch
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.subs, id)
	}
}

// PortToModel maps a child's port to its model, from the latest model list.
func (s *Sampler) PortToModel(port int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ports[port]
}

// ring keeps the last few values of a series.
type ring[T any] struct {
	buf  []T
	next int
	full bool
}

func newRing[T any](size int) *ring[T] { return &ring[T]{buf: make([]T, size)} }

func (r *ring[T]) push(v T) {
	r.buf[r.next] = v
	r.next++
	if r.next == len(r.buf) {
		r.next, r.full = 0, true
	}
}

// values copies the contents out, oldest first.
func (r *ring[T]) values() []T {
	if !r.full {
		return slices.Clone(r.buf[:r.next])
	}
	return append(slices.Clone(r.buf[r.next:]), r.buf[:r.next]...)
}
