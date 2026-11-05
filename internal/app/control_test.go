package app

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/builds"
	"github.com/Angelmmiguel/llama-gui/internal/hardware"
	"github.com/Angelmmiguel/llama-gui/internal/logs"
	"github.com/Angelmmiguel/llama-gui/internal/store"
	"github.com/Angelmmiguel/llama-gui/internal/supervisor"
)

func TestResolveBackend(t *testing.T) {
	ctx := context.Background()
	gpu := func(context.Context) hardware.Snapshot {
		return hardware.Snapshot{GPUs: []hardware.GPU{{Name: "x"}}}
	}
	for in, want := range map[string]string{
		"auto":   hardware.BackendCUDA,
		"sycl":   builds.BackendSYCLFP32,
		"vulkan": "vulkan",
		"nope":   "nope", // the manager rejects it
	} {
		if got := resolveBackend(ctx, in, gpu); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestWatchSupervisorEmitsTransitions(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	pipe := logs.NewPipeline(logs.PipelineOptions{Store: st, Logger: quiet()})

	var mu sync.Mutex
	status := supervisor.Status{State: supervisor.Stopped}
	set := func(s supervisor.Status) {
		mu.Lock()
		defer mu.Unlock()
		status = s
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		watchSupervisor(ctx, func() supervisor.Status {
			mu.Lock()
			defer mu.Unlock()
			return status
		}, pipe, time.Millisecond)
		close(done)
	}()

	events := func() string {
		pipeDrain(pipe)
		list, err := st.ListEvents(context.Background(), time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		kinds := make([]string, len(list))
		for i, e := range list {
			kinds[len(list)-1-i] = e.Kind
		}
		return strings.Join(kinds, " ")
	}
	for _, step := range []struct {
		state supervisor.State
		want  string
	}{
		{supervisor.Starting, ""},
		{supervisor.Running, logs.EventRouterStarted},
		{supervisor.Crashed, logs.EventRouterStarted + " " + eventRouterCrashed},
		{supervisor.Starting, logs.EventRouterStarted + " " + eventRouterCrashed},
	} {
		set(supervisor.Status{State: step.state, LastError: "exit status 1"})
		waitFor(t, "events "+step.want, func() bool { return events() == step.want })
	}

	// A change right before the end is still reported.
	set(supervisor.Status{State: supervisor.Stopped})
	cancel()
	<-done
	want := logs.EventRouterStarted + " " + eventRouterCrashed + " " + eventRouterStopped
	if got := events(); got != want {
		t.Errorf("events %q, want %q", got, want)
	}
}

// pipeDrain runs the pipeline just long enough to write what is queued.
func pipeDrain(p *logs.Pipeline) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.Run(ctx)
}
