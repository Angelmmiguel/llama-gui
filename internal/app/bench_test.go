package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/bench"
	"github.com/Angelmmiguel/llama-gui/internal/llama"
	"github.com/Angelmmiguel/llama-gui/internal/store"
)

// pauseRouter unloads a model on the next listing, as a router takes a moment.
type pauseRouter struct {
	mu        sync.Mutex
	status    map[string]string
	unloading map[string]bool
	stuck     bool
	listErr   error
	loadErr   error
	loads     []string
	unloads   []string
}

func (r *pauseRouter) Models(context.Context) ([]llama.Model, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	var out []llama.Model
	for id := range r.unloading {
		if !r.stuck {
			r.status[id] = "unloaded"
		}
	}
	for id, s := range r.status {
		out = append(out, llama.Model{ID: id, Status: s})
	}
	return out, nil
}

func (r *pauseRouter) Load(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loads = append(r.loads, id)
	return r.loadErr
}

func (r *pauseRouter) Unload(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unloads = append(r.unloads, id)
	if r.unloading == nil {
		r.unloading = map[string]bool{}
	}
	r.unloading[id] = true
	return nil
}

type pauseFixture struct {
	router *pauseRouter
	up     bool
	reason []string
	pause  *benchPause
}

func newPauseFixture(status map[string]string) *pauseFixture {
	f := &pauseFixture{router: &pauseRouter{status: status}, up: true}
	f.pause = &benchPause{
		router: func() (benchRouter, bool) { return f.router, f.up },
		setPaused: func(reason string) {
			f.reason = append(f.reason, reason)
		},
		ctx: context.Background(), log: quiet(),
		timeout: 5 * time.Second, poll: time.Millisecond, retry: time.Minute,
	}
	return f
}

func TestPauseUnloadsAndResumeReloadsWhatWasLoaded(t *testing.T) {
	f := newPauseFixture(map[string]string{"a": "loaded", "b": "unloaded", "c": "loading"})
	resume, err := f.pause.pause(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(f.router.unloads)
	if !slices.Equal(f.router.unloads, []string{"a", "c"}) {
		t.Errorf("unloads %v", f.router.unloads)
	}
	if !slices.Equal(f.reason, []string{benchPauseReason}) {
		t.Errorf("paused %q", f.reason)
	}
	if len(f.router.loads) != 0 {
		t.Errorf("reloaded early: %v", f.router.loads)
	}

	resume()
	slices.Sort(f.router.loads)
	if !slices.Equal(f.router.loads, []string{"a", "c"}) {
		t.Errorf("loads %v", f.router.loads)
	}
	if !slices.Equal(f.reason, []string{benchPauseReason, ""}) {
		t.Errorf("not unpaused: %q", f.reason)
	}
}

func TestResumeUnpausesWhenReloadFails(t *testing.T) {
	f := newPauseFixture(map[string]string{"a": "loaded"})
	resume, err := f.pause.pause(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.router.loadErr = errors.New("nope")
	resume()
	if f.reason[len(f.reason)-1] != "" {
		t.Errorf("still paused: %q", f.reason)
	}
}

func TestResumeUsesItsOwnContextAfterCancel(t *testing.T) {
	f := newPauseFixture(map[string]string{"a": "loaded"})
	ctx, cancel := context.WithCancel(context.Background())
	resume, err := f.pause.pause(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	f.router = &pauseRouter{status: map[string]string{"a": "unloaded"}, loadErr: nil}
	checking := &ctxRouter{pauseRouter: f.router}
	f.pause.router = func() (benchRouter, bool) { return checking, true }
	resume()
	if checking.loadCtxErr != nil || !slices.Equal(f.router.loads, []string{"a"}) {
		t.Errorf("ctx %v, loads %v", checking.loadCtxErr, f.router.loads)
	}
}

type ctxRouter struct {
	*pauseRouter
	loadCtxErr error
}

func (r *ctxRouter) Load(ctx context.Context, id string) error {
	r.loadCtxErr = ctx.Err()
	return r.pauseRouter.Load(ctx, id)
}

func TestPauseWithoutRouterStillPauses(t *testing.T) {
	f := newPauseFixture(nil)
	f.up = false
	resume, err := f.pause.pause(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resume()
	if !slices.Equal(f.reason, []string{benchPauseReason, ""}) {
		t.Errorf("%q", f.reason)
	}
}

func TestPauseFailureUndoesItself(t *testing.T) {
	f := newPauseFixture(map[string]string{"a": "loaded"})
	f.router.stuck = true
	f.pause.timeout = 20 * time.Millisecond
	resume, err := f.pause.pause(context.Background())
	if resume != nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "a") {
		t.Fatalf("resume %v, err %v", resume != nil, err)
	}
	if !slices.Equal(f.router.loads, []string{"a"}) {
		t.Errorf("loads %v", f.router.loads)
	}
	if f.reason[len(f.reason)-1] != "" {
		t.Errorf("still paused: %q", f.reason)
	}

	f = newPauseFixture(map[string]string{"a": "loaded"})
	f.router.listErr = errors.New("down")
	if _, err := f.pause.pause(context.Background()); err == nil {
		t.Fatal("no error")
	}
	if f.reason[len(f.reason)-1] != "" {
		t.Errorf("still paused: %q", f.reason)
	}
}

func TestPauseCancelledWhileWaiting(t *testing.T) {
	f := newPauseFixture(map[string]string{"a": "loaded"})
	f.router.stuck = true
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)
	if _, err := f.pause.pause(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if !slices.Equal(f.router.loads, []string{"a"}) || f.reason[len(f.reason)-1] != "" {
		t.Errorf("loads %v, paused %q", f.router.loads, f.reason)
	}
}

func TestPauseRetriesARefusedUnload(t *testing.T) {
	f := newPauseFixture(map[string]string{"a": "loading"})
	f.router.stuck = true
	f.pause.retry = 2 * time.Millisecond
	f.pause.timeout = 30 * time.Millisecond
	_, _ = f.pause.pause(context.Background())
	if len(f.router.unloads) < 2 {
		t.Errorf("unloads %v", f.router.unloads)
	}
}

func TestHoldsMemory(t *testing.T) {
	for status, want := range map[string]bool{"loaded": true, "loading": true, "sleeping": true, "future": true, "unloaded": false, "": false} {
		if got := holdsMemory(llama.Model{Status: status}); got != want {
			t.Errorf("%q: %v", status, got)
		}
	}
}

func TestFailInterruptedRuns(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	running, err := st.InsertBenchRun(ctx, store.BenchRun{StartedAt: now, State: bench.StateRunning, Model: "a.gguf", BuildID: "b"})
	if err != nil {
		t.Fatal(err)
	}
	done, err := st.InsertBenchRun(ctx, store.BenchRun{StartedAt: now, State: bench.StateDone, Model: "a.gguf", BuildID: "b"})
	if err != nil {
		t.Fatal(err)
	}
	failInterruptedRuns(ctx, st, now.Add(time.Hour), quiet())
	r, err := st.GetBenchRun(ctx, running)
	if err != nil || r.State != bench.StateFailed || !strings.Contains(r.Error, "interrupted") || !r.FinishedAt.Equal(now.Add(time.Hour)) {
		t.Errorf("%+v %v", r, err)
	}
	if r, err := st.GetBenchRun(ctx, done); err != nil || r.State != bench.StateDone {
		t.Errorf("%+v %v", r, err)
	}
}

func TestControlRefusesRouterStartsDuringBench(t *testing.T) {
	c := &control{benching: func() bool { return true }}
	if err := c.requireNoBench(); !errors.Is(err, bench.ErrBusy) {
		t.Errorf("err %v", err)
	}
	g := guardedRouter{c: c}
	if err := g.Load(context.Background(), "a"); !errors.Is(err, bench.ErrBusy) {
		t.Errorf("load: %v", err)
	}
	c.benching = func() bool { return false }
	if err := c.requireNoBench(); err != nil {
		t.Errorf("err %v", err)
	}
}
