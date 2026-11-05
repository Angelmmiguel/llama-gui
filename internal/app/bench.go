package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/bench"
	"github.com/Angelmmiguel/llama-gui/internal/llama"
	"github.com/Angelmmiguel/llama-gui/internal/store"
	"github.com/Angelmmiguel/llama-gui/internal/supervisor"
)

const (
	benchBinary        = "llama-bench"
	benchPauseReason   = "llama-server is paused for a benchmark"
	benchUnloadTimeout = 2 * time.Minute
	benchUnloadPoll    = 250 * time.Millisecond
	benchUnloadRetry   = 3 * time.Second
	benchReloadTimeout = 30 * time.Second
	benchStopTimeout   = time.Second
	// interruptedRuns is how far back startup looks for runs the last process left behind.
	interruptedRuns = 50
)

// benchRouter is what a pause needs from the router.
type benchRouter interface {
	Models(ctx context.Context) ([]llama.Model, error)
	Load(ctx context.Context, id string) error
	Unload(ctx context.Context, id string) error
}

// benchPause frees the GPU for a run and gives it back afterwards.
type benchPause struct {
	router    func() (benchRouter, bool)
	setPaused func(reason string)
	// ctx bounds the reload. A run's own context is no use: cancelling it is what got us there.
	ctx     context.Context
	log     *slog.Logger
	timeout time.Duration
	poll    time.Duration
	retry   time.Duration
}

// pause answers new requests with a 503, then unloads every model and waits for
// that to happen. When it fails, it has already undone its work: the runner only
// calls the resume of a successful pause.
func (p *benchPause) pause(ctx context.Context) (func(), error) {
	p.setPaused(benchPauseReason)
	loaded, err := p.unloadAll(ctx)
	resume := func() { p.resume(loaded) }
	if err != nil {
		resume()
		return nil, err
	}
	return resume, nil
}

func (p *benchPause) resume(models []string) {
	defer p.setPaused("")
	router, up := p.router()
	if !up {
		return
	}
	ctx, cancel := context.WithTimeout(p.ctx, benchReloadTimeout)
	defer cancel()
	for _, id := range models {
		if err := router.Load(ctx, id); err != nil {
			p.log.Warn("reloading a model after a benchmark", "model", id, "error", err)
		}
	}
}

// unloadAll returns what was loaded, even when not all of it could be unloaded.
func (p *benchPause) unloadAll(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	var loaded []string
	asked := map[string]time.Time{}
	ticker := time.NewTicker(p.poll)
	defer ticker.Stop()
	for first := true; ; first = false {
		router, up := p.router()
		if !up {
			return loaded, nil // no router, no models
		}
		models, err := router.Models(ctx)
		if err != nil {
			return loaded, fmt.Errorf("listing models: %w", err)
		}
		var holding []string
		for _, m := range models {
			if !holdsMemory(m) {
				continue
			}
			holding = append(holding, m.ID)
			if first {
				loaded = append(loaded, m.ID)
			}
			// A model still loading can refuse, so asking is repeated now and then.
			if time.Since(asked[m.ID]) >= p.retry {
				asked[m.ID] = time.Now()
				if err := router.Unload(ctx, m.ID); err != nil {
					p.log.Warn("unloading a model for a benchmark", "model", m.ID, "error", err)
				}
			}
		}
		if len(holding) == 0 {
			return loaded, nil
		}
		select {
		case <-ctx.Done():
			return loaded, fmt.Errorf("still loaded: %s: %w", strings.Join(holding, ", "), ctx.Err())
		case <-ticker.C:
		}
	}
}

// holdsMemory treats a state it does not know as holding memory, since upstream can be any version.
func holdsMemory(m llama.Model) bool {
	return m.Status != "" && m.Status != "unloaded"
}

// benchBinary is the runner's Binary: llama-bench next to the active build's llama-server.
func (c *control) benchBinary() (path, build string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	build, err = c.mgr.Active(ctx)
	if err != nil {
		return "", "", err
	}
	if build == "" {
		return "", "", fmt.Errorf("%w: install one first", supervisor.ErrNoBuild)
	}
	server, err := c.mgr.ServerBinary(build)
	if err != nil {
		return "", "", err
	}
	path = filepath.Join(filepath.Dir(server), benchBinary)
	if _, err := os.Stat(path); err != nil {
		return "", "", fmt.Errorf("%w: build %s has no %s", bench.ErrUnsupported, build, benchBinary)
	}
	return path, build, nil
}

// requireNoBench refuses what would bring models back in the middle of a run.
func (c *control) requireNoBench() error {
	if c.benching != nil && c.benching() {
		return bench.ErrBusy
	}
	return nil
}

// guardedRouter is the router as the API sees it: loading a model waits for the
// benchmark, which has just unloaded them all and is timing the GPU.
type guardedRouter struct {
	*llama.Client
	c *control
}

// Load asks the router to load a model, unless a benchmark is running.
func (g guardedRouter) Load(ctx context.Context, id string) error {
	if err := g.c.requireNoBench(); err != nil {
		return err
	}
	return g.Client.Load(ctx, id)
}

// failInterruptedRuns closes the runs a killed process left marked as running.
func failInterruptedRuns(ctx context.Context, st bench.Store, now time.Time, log *slog.Logger) {
	runs, err := st.ListBenchRuns(ctx, "", interruptedRuns)
	if err != nil {
		log.Warn("looking for interrupted benchmarks", "error", err)
		return
	}
	for _, r := range runs {
		if r.State != bench.StateRunning {
			continue
		}
		r.State, r.Error, r.FinishedAt = bench.StateFailed, "interrupted by a restart of llama-gui", now
		if err := st.UpdateBenchRun(ctx, r); err != nil && !errors.Is(err, store.ErrNotFound) {
			log.Warn("closing an interrupted benchmark", "id", r.ID, "error", err)
		}
	}
}
