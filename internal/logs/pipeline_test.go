package logs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/store"
)

func newPipeline(t *testing.T, tweak func(*PipelineOptions)) (*Pipeline, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	o := PipelineOptions{Store: st, FlushEvery: time.Hour}
	if tweak != nil {
		tweak(&o)
	}
	return NewPipeline(o), st
}

var at = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

const (
	spawnLine = `{"type":"log","time":1,"level":"info","msg":"srv  load: spawning server instance with name=smol on port 54000\n"}`
	childLine = `{"type":"log","time":2,"level":"none","msg":"[54000] {\"type\":\"log\",\"time\":1,\"level\":\"info\",\"msg\":\"srv  server_run_i: model loaded\\n\"}\n"}`
	startLine = `{"type":"log","time":0,"level":"info","msg":"srv  server_run_r: starting server in router mode\n"}`
)

func TestLinesAndEventsReachTheStore(t *testing.T) {
	p, st := newPipeline(t, nil)
	ctx := context.Background()
	p.OnLine(startLine, at)
	p.OnLine(spawnLine, at)
	p.OnLine(childLine, at.Add(time.Second))
	p.OnLine("not json at all", at.Add(2*time.Second))
	p.Event("build.switched", "info", "Switched", map[string]string{"build": "b1-cpu"})
	p.drain()

	lines, _, err := st.ListLogLines(ctx, store.LogFilter{})
	if err != nil || len(lines) != 4 {
		t.Fatalf("%+v %v", lines, err)
	}
	if lines[0].Raw != "not json at all" || lines[1].Model != "smol" || lines[1].ID == 0 {
		t.Errorf("%+v", lines[:2])
	}
	events, err := st.ListEvents(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, e := range events {
		kinds[e.Kind] = true
	}
	if !kinds[EventModelLoading] || !kinds[EventModelLoaded] || !kinds["build.switched"] {
		t.Errorf("kinds %v", kinds)
	}
	if kinds[EventRouterStarted] {
		t.Error("the supervisor reports router starts, not the log")
	}
}

func TestPortFallsBackToTheModelList(t *testing.T) {
	p, st := newPipeline(t, func(o *PipelineOptions) {
		o.PortToModel = func(port int) string { return fmt.Sprint("m", port) }
	})
	p.OnLine(`{"type":"log","time":2,"level":"none","msg":"[7] {\"type\":\"log\",\"time\":1,\"level\":\"info\",\"msg\":\"srv  x: y\\n\"}\n"}`, at)
	p.drain()
	lines, _, _ := st.ListLogLines(context.Background(), store.LogFilter{})
	if len(lines) != 1 || lines[0].Model != "m7" {
		t.Errorf("%+v", lines)
	}
}

func TestRunFlushesOnSizeAndTimeAndDrainsAtTheEnd(t *testing.T) {
	p, st := newPipeline(t, func(o *PipelineOptions) { o.FlushLines = 3 })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	count := func() int {
		lines, _, _ := st.ListLogLines(context.Background(), store.LogFilter{})
		return len(lines)
	}
	for i := range 3 {
		p.OnLine(fmt.Sprint("line", i), at)
	}
	deadline := time.Now().Add(5 * time.Second)
	for count() != 3 {
		if time.Now().After(deadline) {
			t.Fatalf("batch not flushed by size: %d", count())
		}
		time.Sleep(time.Millisecond)
	}
	p.OnLine("last", at)
	cancel()
	<-done
	if count() != 4 {
		t.Errorf("final drain: %d", count())
	}

	q, st2 := newPipeline(t, func(o *PipelineOptions) { o.FlushEvery = time.Millisecond })
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	go q.Run(ctx)
	q.OnLine("timed", at)
	deadline = time.Now().Add(5 * time.Second)
	for {
		lines, _, _ := st2.ListLogLines(context.Background(), store.LogFilter{})
		if len(lines) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not flushed by time")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFullQueueDropsWithAWarningWithoutTheContent(t *testing.T) {
	var buf syncBuffer
	p, _ := newPipeline(t, func(o *PipelineOptions) {
		o.Queue = 1
		o.FlushLines = 100
		o.Logger = slog.New(slog.NewTextHandler(&buf, nil))
	})
	p.OnLine("kept", at)
	p.OnLine("the secret prompt", at) // nothing reads the queue, and this must not block
	if !strings.Contains(buf.String(), "dropping router log lines") {
		t.Errorf("log: %q", buf.String())
	}
	if strings.Contains(buf.String(), "secret") {
		t.Errorf("line content in the log: %q", buf.String())
	}
}

type failingStore struct{ PipelineStore }

func (failingStore) InsertLogLines(context.Context, []store.LogLine) error { return errors.New("disk") }

func TestStoreFailureStillPublishesAndDoesNotLogContent(t *testing.T) {
	var buf syncBuffer
	p, st := newPipeline(t, func(o *PipelineOptions) { o.Logger = slog.New(slog.NewTextHandler(&buf, nil)) })
	p.opts.Store = failingStore{st}
	_, ch, stop, err := p.Tail(context.Background(), store.LogFilter{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	p.OnLine("the secret prompt", at)
	p.drain()
	if it := <-ch; it.Line == nil || it.Line.Raw != "the secret prompt" {
		t.Errorf("%+v", it)
	}
	if !strings.Contains(buf.String(), "disk") || strings.Contains(buf.String(), "secret") {
		t.Errorf("log: %q", buf.String())
	}
}

func TestTailHasBacklogThenLiveWithoutDuplicates(t *testing.T) {
	p, _ := newPipeline(t, nil)
	ctx := context.Background()
	p.OnLine("one", at)
	p.OnLine("two", at)
	p.OnLine("three", at)
	p.drain()

	backlog, ch, stop, err := p.Tail(ctx, store.LogFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if len(backlog) != 2 || backlog[0].Raw != "two" || backlog[1].Raw != "three" {
		t.Errorf("backlog %+v", backlog)
	}
	p.OnLine("four", at)
	p.Event("x", "info", "X", nil)
	p.drain()
	if it := <-ch; it.Line == nil || it.Line.Raw != "four" {
		t.Errorf("%+v", it)
	}
	if it := <-ch; it.Event == nil || it.Event.Kind != "x" || it.Event.ID == 0 {
		t.Errorf("%+v", it)
	}
}

// pausedStore stops a backlog query, either before or after it reads the store,
// so a test can write in between.
type pausedStore struct {
	PipelineStore
	readFirst bool
	entered   chan struct{}
	resume    chan struct{}
}

func (s pausedStore) ListLogLines(ctx context.Context, f store.LogFilter) ([]store.LogLine, string, error) {
	if s.readFirst {
		lines, cursor, err := s.PipelineStore.ListLogLines(ctx, f)
		close(s.entered)
		<-s.resume
		return lines, cursor, err
	}
	close(s.entered)
	<-s.resume
	return s.PipelineStore.ListLogLines(ctx, f)
}

type tailResult struct {
	backlog []store.LogLine
	ch      <-chan Item
	stop    func()
	err     error
}

// tailPausedInQuery starts a Tail and returns once its backlog query is under way.
func tailPausedInQuery(p *Pipeline, st *store.Store, readFirst bool) (chan<- struct{}, <-chan tailResult) {
	paused := pausedStore{PipelineStore: st, readFirst: readFirst, entered: make(chan struct{}), resume: make(chan struct{})}
	p.opts.Store = paused
	done := make(chan tailResult, 1)
	go func() {
		backlog, ch, stop, err := p.Tail(context.Background(), store.LogFilter{Limit: 1000})
		done <- tailResult{backlog, ch, stop, err}
	}()
	<-paused.entered
	return paused.resume, done
}

func writeLines(p *Pipeline, n int) {
	for range n {
		p.OnLine("x", at)
	}
	p.drain()
}

// liveIDs reads the lines a channel holds right now.
func liveIDs(ch <-chan Item) []int64 {
	var ids []int64
	for {
		select {
		case it, ok := <-ch:
			if !ok {
				return ids
			}
			if it.Line != nil {
				ids = append(ids, it.Line.ID)
			}
		default:
			return ids
		}
	}
}

func TestBacklogQueryDoesNotBlockWrites(t *testing.T) {
	p, st := newPipeline(t, nil)
	resume, done := tailPausedInQuery(p, st, false)
	wrote := make(chan struct{})
	go func() {
		writeLines(p, 1)
		close(wrote)
	}()
	select {
	case <-wrote:
	case <-time.After(10 * time.Second):
		t.Error("write waited for the backlog query")
	}
	close(resume)
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	r.stop()
}

func TestTailDuringWritesHasNoDuplicatesOrLoss(t *testing.T) {
	for _, tc := range []struct {
		name      string
		readFirst bool
		backlog   int // of the 2 old and 3 written during the query
		live      int
	}{
		{"the query sees the writes", false, 5, 2},
		{"the query misses the writes", true, 2, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, st := newPipeline(t, nil)
			writeLines(p, 2)
			resume, done := tailPausedInQuery(p, st, tc.readFirst)
			writeLines(p, 3)
			p.Event("x", "info", "X", nil)
			p.drain()
			close(resume)
			r := <-done
			if r.err != nil {
				t.Fatal(r.err)
			}
			defer r.stop()
			writeLines(p, 2)

			if len(r.backlog) != tc.backlog {
				t.Errorf("backlog %d, want %d", len(r.backlog), tc.backlog)
			}
			var ids []int64
			for _, l := range r.backlog {
				ids = append(ids, l.ID)
			}
			ids = append(ids, liveIDs(r.ch)...)
			if len(ids) != 7 {
				t.Fatalf("ids %v", ids)
			}
			for i, id := range ids {
				if id != ids[0]+int64(i) {
					t.Fatalf("not consecutive: %v", ids)
				}
			}
		})
	}
}

func TestEventsDuringTheBacklogQueryAreKept(t *testing.T) {
	p, st := newPipeline(t, nil)
	writeLines(p, 3) // the events table has its own IDs, lower than the lines' here
	resume, done := tailPausedInQuery(p, st, false)
	p.Event("x", "info", "X", nil)
	p.drain()
	close(resume)
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	defer r.stop()
	select {
	case it := <-r.ch:
		if it.Event == nil || it.Event.Kind != "x" {
			t.Errorf("%+v", it)
		}
	case <-time.After(10 * time.Second):
		t.Error("the event was dropped")
	}
}

func TestOverflowDuringTheBacklogQueryCloses(t *testing.T) {
	p, st := newPipeline(t, func(o *PipelineOptions) { o.Queue = 10000 })
	resume, done := tailPausedInQuery(p, st, false)
	writeLines(p, subscriberBuffer+5)
	close(resume)
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	defer r.stop()
	if it, ok := <-r.ch; ok {
		t.Errorf("not closed and empty, so a line could repeat: %+v", it)
	}
}

func TestTailQueryFailureEndsTheSubscription(t *testing.T) {
	p, _ := newPipeline(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := p.Tail(ctx, store.LogFilter{}); err == nil {
		t.Fatal("no error")
	}
	if len(p.subs) != 0 {
		t.Errorf("%d subscriptions left", len(p.subs))
	}
}

// Under the race detector: whatever the interleaving, every line appears exactly once.
func TestTailRacingAWriter(t *testing.T) {
	p, _ := newPipeline(t, nil)
	stopWriting := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stopWriting:
				return
			default:
				writeLines(p, 3)
			}
		}
	})
	for range 50 {
		backlog, ch, stop, err := p.Tail(context.Background(), store.LogFilter{Limit: 1000})
		if err != nil {
			t.Fatal(err)
		}
		seen := make(map[int64]bool, len(backlog))
		var last int64
		for _, l := range backlog {
			seen[l.ID] = true
			last = l.ID
		}
		for _, it := range drainFor(ch, 2*time.Millisecond) {
			if it.Line == nil {
				continue
			}
			if seen[it.Line.ID] {
				t.Fatalf("line %d came twice", it.Line.ID)
			}
			if last != 0 && it.Line.ID != last+1 {
				t.Fatalf("after line %d came %d", last, it.Line.ID)
			}
			seen[it.Line.ID] = true
			last = it.Line.ID
		}
		stop()
	}
	close(stopWriting)
	wg.Wait()
}

func drainFor(ch <-chan Item, d time.Duration) []Item {
	var out []Item
	timeout := time.After(d)
	for {
		select {
		case it, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, it)
		case <-timeout:
			return out
		}
	}
}

func TestSlowTailIsClosedNotBlocking(t *testing.T) {
	p, _ := newPipeline(t, func(o *PipelineOptions) { o.FlushLines = 10000; o.Queue = 10000 })
	_, ch, stop, err := p.Tail(context.Background(), store.LogFilter{})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for range subscriberBuffer + 5 {
		p.OnLine("x", at)
	}
	p.drain()
	n := 0
	for range ch {
		n++
	}
	if n != subscriberBuffer {
		t.Errorf("read %d", n)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
