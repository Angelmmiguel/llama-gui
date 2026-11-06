// Package app is the composition root: it builds every component, wires them,
// runs them and stops them.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/api"
	"github.com/Angelmmiguel/llama-gui/internal/app/reaper"
	"github.com/Angelmmiguel/llama-gui/internal/bench"
	"github.com/Angelmmiguel/llama-gui/internal/builds"
	"github.com/Angelmmiguel/llama-gui/internal/config"
	"github.com/Angelmmiguel/llama-gui/internal/hardware"
	"github.com/Angelmmiguel/llama-gui/internal/library"
	"github.com/Angelmmiguel/llama-gui/internal/llama"
	"github.com/Angelmmiguel/llama-gui/internal/logs"
	"github.com/Angelmmiguel/llama-gui/internal/profiles"
	"github.com/Angelmmiguel/llama-gui/internal/proxy"
	"github.com/Angelmmiguel/llama-gui/internal/releases"
	"github.com/Angelmmiguel/llama-gui/internal/store"
	"github.com/Angelmmiguel/llama-gui/internal/supervisor"
	"github.com/Angelmmiguel/llama-gui/internal/telemetry"
	"github.com/Angelmmiguel/llama-gui/web"
)

const (
	// DBFile is the database, inside the data directory.
	DBFile     = "llama-gui.db"
	presetFile = "preset.ini"
	// shutdownTimeout and stopTimeout together fit inside Docker's default 10 s
	// stop timeout.
	shutdownTimeout = 5 * time.Second
	stopTimeout     = 4 * time.Second
	flushTimeout    = time.Second
	pruneInterval   = time.Hour
)

// Run serves until ctx ends or the listener fails.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	if os.Getpid() == 1 {
		go reaper.Reap(ctx, log)
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	return run(ctx, cfg, log, ln)
}

// run is Run over an existing listener, which it takes ownership of.
func run(ctx context.Context, cfg config.Config, log *slog.Logger, ln net.Listener) error {
	defer func() { _ = ln.Close() }()

	if err := os.MkdirAll(cfg.Data, 0o750); err != nil {
		return fmt.Errorf("data directory: %w", err)
	}
	st, err := store.Open(filepath.Join(cfg.Data, DBFile))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	detector := hardware.NewDetector(cfg.Data, cfg.Builds, cfg.Models)
	rel := releases.NewClient(nil, cfg.GitHubAPI, cfg.GitHubToken)
	mgr, err := builds.New(builds.Options{
		Dir: cfg.Builds, Releases: rel, Hardware: detector.Detect, Store: builds.StoreFromKV(st), Logger: log,
	})
	if err != nil {
		return err
	}
	presetPath := filepath.Join(cfg.Data, presetFile)
	svc := profiles.NewService(st, cfg.Models, presetPath)

	// These need each other, so they meet through closures: the pipeline asks the
	// sampler for ports, the sampler reads the supervisor and the proxy, and the
	// supervisor asks the proxy what is in flight.
	var (
		sampler *telemetry.Sampler
		px      *proxy.Proxy
		ctrl    *control
	)
	pipe := logs.NewPipeline(logs.PipelineOptions{
		Store:       st,
		PortToModel: func(port int) string { return sampler.PortToModel(port) },
		Logger:      log,
	})
	sup := supervisor.New(supervisor.Options{
		Binary:       func() (string, error) { return ctrl.binary() },
		PresetPath:   presetPath,
		DataDir:      cfg.Data,
		ModelsMax:    cfg.ModelsMax,
		MemoryMargin: cfg.MemoryMargin,
		Verbosity:    cfg.LogVerbosity,
		ExtraArgs:    strings.Fields(cfg.LlamaArgs),
		Logger:       log,
		OnLine:       pipe.OnLine,
		IdlePolicy:   svc.IdlePolicy,
		InFlight:     func(model string) int { return px.InFlight(model) },
	})
	clients := newRouterClients(sup.Upstream)

	ctrlCtx, cancelCtrl := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelCtrl()
	ctrl = &control{
		sup: sup, mgr: mgr, svc: svc, pipe: pipe, hw: detector.Detect, keep: cfg.KeepBuilds, log: log,
		ctx: ctrlCtx, cancel: cancelCtrl,
		router: func() (reloader, bool) {
			c, ok := clients.get()
			if !ok {
				return nil, false
			}
			return presetReloader{c}, true
		},
	}
	liveB := liveBuilds{Manager: mgr, c: ctrl}

	sink := proxy.NewStoreSink(st, log, 0)
	sampler = telemetry.New(telemetry.Options{
		Router: func() (telemetry.Router, bool) {
			c, ok := clients.get()
			if !ok {
				return nil, false
			}
			return c, true
		},
		State:    func() string { return string(sup.State().State) },
		Hardware: detector.Sample,
		Requests: requestsFunc{func() *proxy.Proxy { return px }},
		OnEvent:  pipe.Event,
		Record: func(ctx context.Context, r telemetry.Rollup) error {
			return st.InsertTelemetrySample(ctx, store.TelemetrySample(r))
		},
		Logger: log,
	})
	px = proxy.New(proxy.Options{
		Upstream:       sup.Upstream,
		Sink:           teeSink{sink, sampler},
		Policy:         svc,
		Touch:          sup.Touch,
		Capture:        cfg.Capture == config.CaptureBodies,
		AllowedOrigins: cfg.AllowedOrigins,
		Logger:         log,
	})

	failInterruptedRuns(ctx, st, time.Now(), log)
	pause := &benchPause{
		router: func() (benchRouter, bool) {
			c, ok := clients.get()
			if !ok {
				return nil, false
			}
			return c, true
		},
		setPaused: px.SetPaused,
		ctx:       ctrlCtx,
		log:       log,
		timeout:   benchUnloadTimeout,
		poll:      benchUnloadPoll,
		retry:     benchUnloadRetry,
	}
	runner := bench.NewRunner(bench.Options{
		Binary: ctrl.benchBinary,
		Models: cfg.Models,
		Pause:  pause.pause,
		Store:  st,
		Logger: log,
	})
	ctrl.benching = func() bool {
		run, ok := runner.Current()
		return ok && run.State == bench.StateRunning
	}

	updates := builds.NewUpdateChecker(builds.UpdateOptions{
		Policy:   cfg.LlamaUpdates,
		Releases: rel,
		Active:   mgr.ActiveManifest,
		Install:  liveB.Install,
		Wait:     mgr.Wait,
		Activate: liveB.Activate,
		InFlight: func() int {
			n := len(px.List())
			if ctrl.benching() {
				n++ // a switch restarts the router under the run
			}
			return n
		},
		Logger: log,
	})

	ctrl.applyActive(ctx)

	hub := library.NewClient(nil, "", cfg.HFToken)
	if err := os.MkdirAll(cfg.Models, 0o750); err != nil {
		log.Warn("models directory", "error", err)
	}
	downloads := library.NewQueue(cfg.Models, hub, nil, func() { ctrl.reloadPreset(ctrlCtx) })

	handler := routes(cfg.AllowedHosts, api.Handler(api.Deps{
		Version:      cfg.Version,
		Capture:      cfg.Capture,
		LogVerbosity: cfg.LogVerbosity,
		MemoryMargin: cfg.MemoryMargin,
		ModelsDir:    cfg.Models,
		Started:      time.Now(),
		Now:          time.Now,
		Logger:       log,
		Server:       ctrl,
		Builds:       liveB,
		Releases:     rel,
		Profiles:     liveProfiles{Service: svc, c: ctrl},
		Store:        st,
		Telemetry:    sampler,
		Tail:         pipe,
		Hub:          libraryHub{hub},
		Downloads:    downloads,
		Bench:        runner,
		ReloadModels: ctrl.reloadPreset,
		Router: func() (api.Router, bool) {
			c, ok := clients.get()
			if !ok {
				return nil, false
			}
			return guardedRouter{c, ctrl}, true
		},
		CancelSlot:   px.Cancel,
		Hardware:     detector.Detect,
		Updates:      updates.Info,
		CheckUpdates: updates.Refresh,
		Done:         ctx.Done(),
	}), web.Handler(), px)

	// Each of these outlives what feeds it: the log pipeline runs until the router
	// is gone, so its last lines are kept.
	pipeCtx, stopPipe := context.WithCancel(context.WithoutCancel(ctx))
	defer stopPipe()
	sampleCtx, stopSampling := context.WithCancel(context.WithoutCancel(ctx))
	defer stopSampling()
	var pipeDone, sampling sync.WaitGroup
	pipeDone.Go(func() { pipe.Run(pipeCtx) })
	sampling.Go(func() { sampler.Run(sampleCtx) })
	sampling.Go(func() { pruneLoop(sampleCtx, st, cfg.Retention, pruneInterval, log) })
	sampling.Go(func() { watchSupervisor(sampleCtx, sup.State, pipe, watchInterval) })

	ctrl.background(func(ctx context.Context) { ctrl.firstStart(ctx, cfg.LlamaVersion, cfg.LlamaBackend) })
	ctrl.background(updates.Run)

	if !isLoopback(ln.Addr()) {
		log.Warn("listening beyond loopback with no authentication; anyone who can reach this port can use the API, "+
			"so keep it on a trusted network or behind an authenticating reverse proxy", "addr", ln.Addr().String())
	}
	log.Info("listening", "addr", ln.Addr().String(), "data", cfg.Data, "version", cfg.Version)
	serveErr := serve(ctx, ln, handler, shutdownTimeout, log)

	// The HTTP server has drained, so nothing can start new work from here on.
	downloads.Close()
	ctrl.close()
	// After close, so a cancelled run finds no reason to reload models on the way out.
	if err := runner.Cancel(); err == nil {
		benchCtx, cancelBench := context.WithTimeout(context.Background(), benchStopTimeout)
		defer cancelBench()
		if err := runner.Wait(benchCtx); err != nil {
			log.Warn("stopping llama-bench", "error", err)
		}
	}
	mgr.Close()
	stopCtx, cancelStop := context.WithTimeout(context.Background(), stopTimeout)
	defer cancelStop()
	if err := sup.Stop(stopCtx); err != nil {
		log.Warn("stopping llama-server", "error", err)
	}
	stopSampling()
	sampling.Wait()
	flushCtx, cancelFlush := context.WithTimeout(context.Background(), flushTimeout)
	defer cancelFlush()
	if err := sink.Close(flushCtx); err != nil {
		log.Warn("flushing request measurements", "error", err)
	}
	stopPipe()
	pipeDone.Wait()
	return serveErr
}

// routerClients keeps one client per router address, which changes on each start.
type routerClients struct {
	upstream func() (*url.URL, bool)
	http     *http.Client

	mu     sync.Mutex
	base   string
	client *llama.Client
}

func newRouterClients(upstream func() (*url.URL, bool)) *routerClients {
	// A bare transport: a proxy from the environment must not get between us and loopback.
	return &routerClients{upstream: upstream, http: &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 16}}}
}

func (r *routerClients) get() (*llama.Client, bool) {
	u, ok := r.upstream()
	if !ok {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if base := u.String(); base != r.base {
		r.base, r.client = base, llama.New(base, r.http)
	}
	return r.client, true
}

// libraryHub adds the resolved starter catalog to the hub client.
type libraryHub struct{ *library.Client }

// Candidates recovers the reason when no entry resolved, as ResolveCatalog only
// leaves out what failed, and the Library should say Hugging Face is out of reach
// rather than show an empty list.
func (h libraryHub) Candidates(ctx context.Context) ([]library.Candidate, error) {
	entries := library.Catalog()
	cands := library.ResolveCatalog(ctx, h.Client, entries)
	if len(cands) == 0 && len(entries) > 0 {
		if _, err := h.Repo(ctx, entries[0].Repo); err != nil {
			return nil, err
		}
	}
	return cands, nil
}

type presetReloader struct{ c *llama.Client }

func (p presetReloader) Reload(ctx context.Context) error {
	_, err := p.c.Reload(ctx)
	return err
}

// requestsFunc defers to the proxy, which is built after the sampler it feeds.
type requestsFunc struct{ px func() *proxy.Proxy }

func (r requestsFunc) AssignTask(model string, slot int, task int64) bool {
	return r.px().AssignTask(model, slot, task)
}

func (r requestsFunc) List() []proxy.Inflight { return r.px().List() }

// teeSink stores measurements and feeds the rolling aggregates.
type teeSink struct {
	*proxy.StoreSink
	sampler *telemetry.Sampler
}

func (t teeSink) Record(r proxy.RequestRecord) {
	t.StoreSink.Record(r)
	t.sampler.ObserveRequest(r.StartedAt.Add(time.Duration(r.TotalMs*float64(time.Millisecond))), r.TTFTMs)
}

// serve runs the HTTP server until ctx ends, then drains it for at most drain.
func serve(ctx context.Context, ln net.Listener, handler http.Handler, drain time.Duration, log *slog.Logger) error {
	srv := &http.Server{
		Handler: handler,
		// There is deliberately no WriteTimeout: inference responses and the
		// SSE streams stay open for as long as they need.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	log.Info("shutting down")
	drainCtx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		// Long streams are normal, so running out of patience is no failure.
		log.Warn("closing connections that did not drain", "error", err)
		if err := srv.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("close: %w", err)
		}
	}
	return nil
}

// pruneLoop applies retention at startup and then every interval.
func pruneLoop(ctx context.Context, st *store.Store, retention, interval time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		n, err := st.Prune(ctx, time.Now().Add(-retention))
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("pruning history", "error", err)
		case n > 0:
			log.Info("pruned history", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func isLoopback(addr net.Addr) bool {
	tcp, ok := addr.(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}
