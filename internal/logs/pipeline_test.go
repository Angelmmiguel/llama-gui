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
