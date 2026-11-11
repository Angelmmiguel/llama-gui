package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/api"
	"github.com/Angelmmiguel/llama-gui/internal/builds"
	"github.com/Angelmmiguel/llama-gui/internal/hardware"
	"github.com/Angelmmiguel/llama-gui/internal/logs"
	"github.com/Angelmmiguel/llama-gui/internal/profiles"
	"github.com/Angelmmiguel/llama-gui/internal/releases"
	"github.com/Angelmmiguel/llama-gui/internal/supervisor"
)

// Lifecycle events, next to the ones logs.Detector derives from the output.
const (
	eventRouterStopped      = "router.stopped"
	eventRouterCrashed      = "router.crashed"
	eventBuildSwitched      = "build.switched"
	eventBuildInstalled     = "build.installed"
	eventBuildInstallFailed = "build.install_failed"
)

const (
	reloadTimeout    = 10 * time.Second
	retentionTimeout = time.Minute
	watchInterval    = 500 * time.Millisecond
)

// control is what crosses components: every operation that touches the
// supervisor, the builds and the profiles together lives here, behind the
// interfaces the API asks for.
type control struct {
	sup    *supervisor.Supervisor
	mgr    *builds.Manager
	svc    *profiles.Service
	pipe   *logs.Pipeline
	router func() (reloader, bool)
	hw     func(context.Context) hardware.Snapshot
	keep   int
	log    *slog.Logger
	// benching says a benchmark is running; nil until the runner exists.
	benching func() bool
	// installIssue says why the first install is waiting to try again.
	installIssue atomic.Pointer[api.InstallIssue]

	// ctx bounds the background work, whose request has long been answered.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex // guards closed, so nothing is added to wg after close
	closed bool
}

type reloader interface {
	Reload(ctx context.Context) error
}

// background runs f unless shutdown has begun.
func (c *control) background(f func(ctx context.Context)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.wg.Go(func() { f(c.ctx) })
}

// close stops the background work and waits for it, so a late Start cannot
// follow the final Stop.
func (c *control) close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.cancel()
	c.wg.Wait()
}

// binary is the supervisor's Binary: re-asked on every start, so a switch only
// has to restart.
func (c *control) binary() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, err := c.mgr.Active(ctx)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.New("no build is active")
	}
	return c.mgr.ServerBinary(id)
}

// requireBuild reports having no build at once, as starting would only find out
// in the background, where the caller can't see it.
func (c *control) requireBuild(ctx context.Context) error {
	id, err := c.mgr.Active(ctx)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("%w: install one first", supervisor.ErrNoBuild)
	}
	return nil
}

// Status returns the router's status.
func (c *control) Status() supervisor.Status { return c.sup.State() }

// Start begins starting the router and returns.
func (c *control) Start(ctx context.Context) error {
	if err := c.requireNoBench(); err != nil {
		return err
	}
	if err := c.requireBuild(ctx); err != nil {
		return err
	}
	c.background(c.startRouter)
	return nil
}

func (c *control) startRouter(ctx context.Context) {
	if err := c.sup.Start(ctx); err != nil && !errors.Is(err, supervisor.ErrStopped) && ctx.Err() == nil {
		c.log.Warn("starting llama-server", "error", err)
	}
}

// Stop stops the router, returning when it is gone.
func (c *control) Stop(ctx context.Context) error { return c.sup.Stop(ctx) }

// Restart begins replacing the router and returns.
func (c *control) Restart(ctx context.Context) error {
	if err := c.requireNoBench(); err != nil {
		return err
	}
	if err := c.requireBuild(ctx); err != nil {
		return err
	}
	c.background(c.restartRouter)
	return nil
}

func (c *control) restartRouter(ctx context.Context) {
	if err := c.sup.Restart(ctx); err != nil && !errors.Is(err, supervisor.ErrStopped) && ctx.Err() == nil {
		c.log.Warn("restarting llama-server", "error", err)
	}
}

// liveBuilds adds to the manager what must happen around its operations.
type liveBuilds struct {
	*builds.Manager
	c *control
}

// Install starts an install, resolving the backend "auto", and follows it.
func (b liveBuilds) Install(ctx context.Context, version, backend string) (builds.Job, error) {
	job, err := b.Manager.Install(ctx, version, resolveBackend(ctx, backend, b.c.hw))
	if err == nil {
		b.c.background(func(ctx context.Context) { b.c.followJob(ctx, job.ID) })
	}
	return job, err
}

// resolveBackend maps the configured names to the backends of release assets.
func resolveBackend(ctx context.Context, backend string, hw func(context.Context) hardware.Snapshot) string {
	switch backend {
	case builds.AutoBackend:
		return hardware.RecommendBackend(hw(ctx))
	case "sycl":
		return builds.BackendSYCLFP32
	}
	return backend
}

// followJob reports how an install ended and applies retention after a success.
func (c *control) followJob(ctx context.Context, id string) {
	job, err := c.mgr.Wait(ctx, id)
	if err != nil {
		return
	}
	switch job.State {
	case builds.StateDone:
		c.pipe.Event(eventBuildInstalled, logs.SeverityInfo, "Installed "+id, map[string]string{"build": id})
		c.applyRetention(ctx)
	case builds.StateFailed:
		c.pipe.Event(eventBuildInstallFailed, logs.SeverityError, "Installing "+id+" failed", map[string]string{"build": id, "error": job.Error})
	}
}

func (c *control) applyRetention(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, retentionTimeout)
	defer cancel()
	removed, err := c.mgr.ApplyRetention(ctx, c.keep)
	switch {
	case err != nil:
		c.log.Warn("applying build retention", "error", err)
	case len(removed) > 0:
		c.log.Info("removed old builds", "builds", strings.Join(removed, ","))
	}
}

// Activate switches the active build and begins restarting the router on it.
func (b liveBuilds) Activate(ctx context.Context, id string) error {
	if err := b.c.requireNoBench(); err != nil {
		return err
	}
	if err := b.Manager.Activate(ctx, id); err != nil {
		return err
	}
	b.c.applyActive(ctx)
	b.c.pipe.Event(eventBuildSwitched, logs.SeverityInfo, "Switched to "+id, map[string]string{"build": id})
	b.c.background(b.c.restartRouter)
	return nil
}

// applyActive renders the preset for what the active build supports.
func (c *control) applyActive(ctx context.Context) {
	var supported func(string) bool
	if man, ok := c.mgr.ActiveManifest(ctx); ok && len(man.Probe.Flags) > 0 {
		flags := make(map[string]bool, len(man.Probe.Flags))
		for _, f := range man.Probe.Flags {
			flags[f] = true
		}
		supported = func(flag string) bool { return flags["--"+flag] }
	}
	c.svc.SetSupported(supported)
	skipped, err := c.svc.Sync(ctx)
	if err != nil {
		c.log.Warn("rendering the preset", "error", err)
		return
	}
	for _, s := range skipped {
		c.log.Warn("left out of the preset", "profile", s.Alias, "flag", s.Flag, "reason", s.Reason)
	}
}

// firstStart installs a build if there is none, or probes the external one, and then
// starts the router. It blocks for as long as the download takes, so it runs in the
// background. A failed install is retried, because the usual causes pass: GitHub's rate
// limit is shared by everyone behind an address, and a server often boots before its
// network.
func (c *control) firstStart(ctx context.Context, version, backend string) {
	before, _ := c.mgr.Active(ctx)
	if backend != builds.AutoBackend {
		backend = resolveBackend(ctx, backend, c.hw)
	}
	defer c.installIssue.Store(nil)
	for attempt := 0; ; attempt++ {
		err := c.mgr.EnsureFirstInstall(ctx, version, backend)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return
		}
		delay := installRetryDelay(err, attempt, time.Now())
		c.log.Warn("first install failed", "error", err, "retry_in", delay)
		c.installIssue.Store(&api.InstallIssue{
			Error: err.Error(), RateLimited: errors.Is(err, releases.ErrRateLimited), RetryAt: time.Now().Add(delay),
		})
		if attempt == 0 {
			what := "Installing " + version
			if c.mgr.External() {
				what = "Probing the external build"
			}
			c.pipe.Event(eventBuildInstallFailed, logs.SeverityError, what+" failed", map[string]string{"version": version, "error": err.Error()})
			// With no build this leaves the router stopped, with the reason in its status.
			c.startIfBuilt(ctx)
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
		// Someone installed a build by hand in the meantime; that path started the router.
		if now, _ := c.mgr.Active(ctx); now != before && now != "" {
			return
		}
	}
	if after, _ := c.mgr.Active(ctx); after != before && after != "" {
		if !c.mgr.External() {
			c.pipe.Event(eventBuildInstalled, logs.SeverityInfo, "Installed "+after, map[string]string{"build": after})
			c.applyRetention(ctx)
		}
		c.applyActive(ctx)
	}
	c.startIfBuilt(ctx)
}

// startIfBuilt starts the router; having no build yet is expected here.
func (c *control) startIfBuilt(ctx context.Context) {
	if err := c.sup.Start(ctx); err != nil && !errors.Is(err, supervisor.ErrNoBuild) {
		c.log.Warn("starting llama-server", "error", err)
	}
}

// installRetryDelay waits out a rate limit, and otherwise doubles from a minute up to
// half an hour.
func installRetryDelay(err error, attempt int, now time.Time) time.Duration {
	var limited *releases.RateLimitError
	if errors.As(err, &limited) && limited.Reset.After(now) {
		return limited.Reset.Sub(now) + 5*time.Second
	}
	return min(time.Minute<<min(attempt, 5), 30*time.Minute)
}

// liveProfiles tells a running router when the preset changes.
type liveProfiles struct {
	*profiles.Service
	c *control
}

// Create stores a profile and reloads the preset.
func (p liveProfiles) Create(ctx context.Context, prof profiles.Profile) (profiles.Profile, error) {
	out, err := p.Service.Create(ctx, prof)
	if err == nil {
		p.c.reloadPreset(ctx)
	}
	return out, err
}

// Update replaces a profile and reloads the preset.
func (p liveProfiles) Update(ctx context.Context, id string, prof profiles.Profile) (profiles.Profile, error) {
	out, err := p.Service.Update(ctx, id, prof)
	if err == nil {
		p.c.reloadPreset(ctx)
	}
	return out, err
}

// Delete removes a profile and reloads the preset.
func (p liveProfiles) Delete(ctx context.Context, id string) error {
	err := p.Service.Delete(ctx, id)
	if err == nil {
		p.c.reloadPreset(ctx)
	}
	return err
}

// reloadPreset is best effort: the change is saved, and a router that misses it
// picks it up on its next start.
func (c *control) reloadPreset(ctx context.Context) {
	router, up := c.router()
	if !up {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reloadTimeout)
	defer cancel()
	if err := router.Reload(ctx); err != nil {
		c.log.Warn("reloading the preset", "error", err)
	}
}

// watchSupervisor turns the supervisor's state changes into events until ctx
// ends. It polls because the supervisor has no callback, and a crash stays
// visible for at least the backoff, which is longer than the interval.
func watchSupervisor(ctx context.Context, state func() supervisor.Status, pipe *logs.Pipeline, interval time.Duration) {
	prev := state().State
	check := func() {
		st := state()
		if st.State == prev {
			return
		}
		switch st.State {
		case supervisor.Running:
			pipe.Event(logs.EventRouterStarted, logs.SeverityInfo, "Router started", map[string]string{"build": st.Build})
		case supervisor.Stopped:
			pipe.Event(eventRouterStopped, logs.SeverityInfo, "Router stopped", nil)
		case supervisor.Crashed:
			pipe.Event(eventRouterCrashed, logs.SeverityError, "Router crashed", map[string]string{"error": st.LastError})
		}
		prev = st.State
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			check()
			return
		case <-ticker.C:
			check()
		}
	}
}
