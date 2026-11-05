// Package supervisor runs the llama-server router process: start, stop, restart
// with backoff, restoring loaded models, and unloading idle ones.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/app/reaper"
	"github.com/Angelmmiguel/llama-gui/internal/llama"
)

// ErrNoBuild means Options.Binary could not name a binary. It leaves the
// supervisor stopped rather than crash looping until a build is installed.
var ErrNoBuild = errors.New("supervisor: no llama-server build")

// ErrStopped is what Start returns when a Stop overtakes it.
var ErrStopped = errors.New("supervisor: stopped")

// State is the router process's lifecycle state.
type State string

// States.
const (
	Stopped  State = "stopped"
	Starting State = "starting"
	Running  State = "running"
	Stopping State = "stopping"
	Crashed  State = "crashed"
)

// Status is a snapshot of the supervisor.
type Status struct {
	State        State     `json:"state"`
	PID          int       `json:"pid"`
	Since        time.Time `json:"since"`
	RestartCount int       `json:"restart_count"` // crash restarts since this supervisor was created
	LastError    string    `json:"last_error"`
	BaseURL      string    `json:"base_url"`
	Build        string    `json:"build"` // path of the binary in use
}

// Options configures a Supervisor. Binary and DataDir are required; the rest have defaults.
type Options struct {
	Binary       func() (string, error) // re-asked on every start, so a build switch needs only Restart
	PresetPath   string
	DataDir      string
	ModelsMax    int // 0 leaves upstream's default
	MemoryMargin int // MiB; 0 leaves upstream's default
	Verbosity    int // -lv; 0 means 3
	ExtraArgs    []string
	Env          []string // added to this process's environment
	Logger       *slog.Logger
	OnLine       func(raw string, at time.Time) // stdout and stderr, one call per line, from one goroutine
	Clock        func() time.Time

	// IdlePolicy returns how long a model may stay idle, if it has a limit.
	IdlePolicy func(model string) (idle time.Duration, ok bool)
	// InFlight returns the requests in progress for a model.
	InFlight func(model string) int

	HealthTimeout time.Duration // default 60 s
	StopTimeout   time.Duration // SIGTERM to SIGKILL; default 10 s
	BackoffMin    time.Duration // default 1 s
	BackoffMax    time.Duration // default 60 s
	StableAfter   time.Duration // a run this long resets the backoff; default 60 s
	PollInterval  time.Duration // /models polling for restore and idle unload; default 5 s
}

// Supervisor owns one router process at a time.
type Supervisor struct {
	opts Options
	log  *slog.Logger
	now  func() time.Time

	mu         sync.Mutex
	status     Status
	run        *run
	upstream   *url.URL
	remembered []string // what to reload after the next start

	idleMu sync.Mutex
	idle   idleTracker
}

// run is one Start to Stop span, possibly over several processes.
type run struct {
	stop, kill, done, resolved chan struct{}
	stopOnce, killOnce         sync.Once
	resolveOnce                sync.Once
	firstErr                   error
}

func newRun() *run {
	return &run{stop: make(chan struct{}), kill: make(chan struct{}), done: make(chan struct{}), resolved: make(chan struct{})}
}

func (r *run) requestStop() { r.stopOnce.Do(func() { close(r.stop) }) }
func (r *run) requestKill() { r.killOnce.Do(func() { close(r.kill) }) }

// resolve reports the first attempt's outcome to Start.
func (r *run) resolve(err error) {
	r.resolveOnce.Do(func() {
		r.firstErr = err
		close(r.resolved)
	})
}

// New returns a stopped supervisor.
func New(opts Options) *Supervisor {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&opts.HealthTimeout, 60*time.Second)
	def(&opts.StopTimeout, 10*time.Second)
	def(&opts.BackoffMin, time.Second)
	def(&opts.BackoffMax, 60*time.Second)
	def(&opts.StableAfter, 60*time.Second)
	def(&opts.PollInterval, 5*time.Second)
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Supervisor{opts: opts, log: opts.Logger, now: opts.Clock}
	s.status = Status{State: Stopped, Since: s.now()}
	return s
}

// State returns the current status.
func (s *Supervisor) State() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Upstream returns the router's address while it is healthy. It is the proxy's upstream.
func (s *Supervisor) Upstream() (*url.URL, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upstream, s.upstream != nil
}

// Touch restarts a model's idle timer. The proxy calls it as requests start and end.
func (s *Supervisor) Touch(model string) {
	now := s.now()
	s.idleMu.Lock()
	defer s.idleMu.Unlock()
	s.idle.touch(model, now)
}

func (s *Supervisor) update(f func(*Status)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.status.State
	f(&s.status)
	if s.status.State != prev {
		s.status.Since = s.now()
	}
}

// Start launches the router and waits for the first attempt to become healthy or
// fail, or for ctx. It returns nil at once if already started. A failed first
// attempt is returned but keeps retrying with backoff, unless the cause is
// ErrNoBuild. Cancelling ctx stops the waiting, not the process.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.run != nil {
		s.mu.Unlock()
		return nil
	}
	r := newRun()
	s.run = r
	s.status.LastError = ""
	s.mu.Unlock()
	go s.loop(r)
	select {
	case <-r.resolved:
		return r.firstErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop terminates the router for good: it is not restarted until Start. It
// escalates to SIGKILL after StopTimeout or when ctx ends, and in both cases
// returns only when the process is gone.
func (s *Supervisor) Stop(ctx context.Context) error {
	err := s.halt(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remembered = nil
	return err
}

// Restart replaces the router, asking Binary again, and reloads what was loaded.
func (s *Supervisor) Restart(ctx context.Context) error {
	if st := s.State(); st.State == Running {
		// More current than the last poll, which may be seconds old. Failure just keeps that one.
		qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		models, err := llama.New(st.BaseURL, nil).Models(qctx)
		cancel()
		if err == nil {
			s.remember(models)
		}
	}
	if err := s.halt(ctx); err != nil {
		return err
	}
	return s.Start(ctx)
}

func (s *Supervisor) halt(ctx context.Context) error {
	s.mu.Lock()
	r := s.run
	s.mu.Unlock()
	if r == nil {
		return nil
	}
	r.requestStop()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		r.requestKill()
		<-r.done
		return ctx.Err()
	}
}

func (s *Supervisor) loop(r *run) {
	defer close(r.done)
	finish := func(lastError string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.status.State != Stopped {
			s.status.Since = s.now()
		}
		s.status.State, s.status.PID, s.status.BaseURL, s.status.LastError = Stopped, 0, "", lastError
		s.upstream, s.run = nil, nil
	}
	attempt := 0
	for {
		bin, err := s.opts.Binary()
		if err != nil {
			err = fmt.Errorf("%w: %w", ErrNoBuild, err)
			finish(err.Error())
			r.resolve(err)
			return
		}
		begin := s.now()
		stopped, err := s.runOnce(r, bin)
		if stopped {
			finish("")
			r.resolve(ErrStopped)
			return
		}
		s.log.Warn("llama-server failed", "error", err)
		s.update(func(st *Status) {
			st.State, st.PID, st.BaseURL, st.LastError = Crashed, 0, "", err.Error()
		})
		r.resolve(err)
		if s.now().Sub(begin) >= s.opts.StableAfter {
			attempt = 0
		}
		timer := time.NewTimer(backoff(attempt, s.opts.BackoffMin, s.opts.BackoffMax))
		attempt++
		select {
		case <-timer.C:
		case <-r.stop:
			timer.Stop()
			finish("")
			return
		}
		s.update(func(st *Status) { st.RestartCount++ })
	}
}

// backoff doubles from lo per attempt and caps at hi.
func backoff(attempt int, lo, hi time.Duration) time.Duration {
	d := lo
	for range attempt {
		d *= 2
		if d >= hi || d <= 0 {
			return hi
		}
	}
	return min(d, hi)
}

// runOnce owns one process from spawn to gone. stopped says it ended because of a
// Stop; otherwise err says why it failed.
func (s *Supervisor) runOnce(r *run, bin string) (stopped bool, err error) {
	if err := os.MkdirAll(filepath.Join(s.opts.DataDir, "slots"), 0o750); err != nil {
		return false, fmt.Errorf("supervisor: %w", err)
	}
	port, err := freePort()
	if err != nil {
		return false, fmt.Errorf("supervisor: pick port: %w", err)
	}
	base := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}

	cmd := exec.Command(bin, s.args(port)...)
	cmd.Env = append(os.Environ(), s.opts.Env...)
	// Its own group, so that signals reach the model children too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// One pipe for both streams keeps lines in order. Passing files rather than
	// writers means Wait does not hang on a child that outlives the router.
	pr, pw, err := os.Pipe()
	if err != nil {
		return false, fmt.Errorf("supervisor: %w", err)
	}
	cmd.Stdout, cmd.Stderr = pw, pw

	s.update(func(st *Status) {
		st.State, st.BaseURL, st.Build, st.PID = Starting, base.String(), bin, 0
	})
	err = reaper.Start(cmd)
	_ = pw.Close()
	if err != nil {
		_ = pr.Close()
		return false, fmt.Errorf("supervisor: start: %w", err)
	}
	pid := cmd.Process.Pid
	s.update(func(st *Status) { st.PID = pid })
	s.log.Info("llama-server started", "pid", pid, "port", port, "binary", bin)

	exited := make(chan error, 1)
	go func() { exited <- reaper.Wait(cmd) }()
	lines := make(chan struct{})
	go func() {
		defer close(lines)
		scanLines(pr, maxLine, s.emit)
	}()
	// Whatever the way out, clear the group: a router that crashed leaves model
	// children holding the GPU and its port.
	defer func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		select {
		case <-lines:
		case <-time.After(2 * time.Second):
		}
		_ = pr.Close()
		<-lines
	}()

	client := llama.New(base.String(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	health := make(chan error, 1)
	go func() { health <- s.waitHealth(ctx, client) }()
	select {
	case err := <-health:
		if err != nil {
			s.terminate(r, pid, exited)
			return false, err
		}
	case err := <-exited:
		return false, exitError(err)
	case <-r.stop:
		s.terminate(r, pid, exited)
		return true, nil
	}

	s.mu.Lock()
	s.status.State, s.status.Since = Running, s.now()
	s.upstream = base
	wanted := s.remembered
	s.mu.Unlock()
	r.resolve(nil)

	monitor := make(chan struct{})
	go func() {
		defer close(monitor)
		s.monitor(ctx, client, wanted)
	}()
	defer func() {
		cancel()
		<-monitor
	}()

	select {
	case err := <-exited:
		s.clearUpstream()
		return false, exitError(err)
	case <-r.stop:
		s.clearUpstream()
		s.terminate(r, pid, exited)
		return true, nil
	}
}

func (s *Supervisor) clearUpstream() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upstream = nil
}

func exitError(err error) error {
	if err == nil {
		return errors.New("llama-server exited with status 0")
	}
	return fmt.Errorf("llama-server exited: %w", err)
}

// terminate sends SIGTERM to the group, then SIGKILL after StopTimeout or on request.
func (s *Supervisor) terminate(r *run, pid int, exited <-chan error) {
	s.update(func(st *Status) { st.State = Stopping })
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	timer := time.NewTimer(s.opts.StopTimeout)
	defer timer.Stop()
	select {
	case err := <-exited:
		s.log.Info("llama-server stopped", "pid", pid, "result", err)
		return
	case <-timer.C:
	case <-r.kill:
	}
	s.log.Warn("llama-server ignored SIGTERM, killing", "pid", pid)
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	s.log.Info("llama-server killed", "pid", pid, "result", <-exited)
}

func (s *Supervisor) args(port int) []string {
	args := []string{"--host", "127.0.0.1", "--port", strconv.Itoa(port)}
	if s.opts.PresetPath != "" {
		args = append(args, "--models-preset", s.opts.PresetPath)
	}
	if s.opts.ModelsMax > 0 {
		args = append(args, "--models-max", strconv.Itoa(s.opts.ModelsMax))
	}
	if s.opts.MemoryMargin > 0 {
		args = append(args, "--models-memory-margin", strconv.Itoa(s.opts.MemoryMargin))
	}
	verbosity := s.opts.Verbosity
	if verbosity == 0 {
		verbosity = 3
	}
	args = append(args, "--no-models-autoload", "--metrics",
		"--slot-save-path", filepath.Join(s.opts.DataDir, "slots"),
		"--log-jsonl", "-lv", strconv.Itoa(verbosity))
	return append(args, s.opts.ExtraArgs...)
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = ln.Close() }()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected address %v", ln.Addr())
	}
	return addr.Port, nil
}

func (s *Supervisor) waitHealth(ctx context.Context, client *llama.Client) error {
	ctx, cancel := context.WithTimeout(ctx, s.opts.HealthTimeout)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		rctx, rcancel := context.WithTimeout(ctx, 2*time.Second)
		err := client.Health(rctx)
		rcancel()
		if err == nil {
			return nil
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return fmt.Errorf("llama-server not healthy within %s: %w", s.opts.HealthTimeout, err)
		}
	}
}

func (s *Supervisor) emit(line string) {
	if s.opts.OnLine != nil {
		s.opts.OnLine(line, s.now())
	}
}
