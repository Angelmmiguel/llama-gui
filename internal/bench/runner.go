package bench

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/app/reaper"
	"github.com/Angelmmiguel/llama-gui/internal/store"
)

// Run states.
const (
	StateRunning  = "running"
	StateDone     = "done"
	StateFailed   = "failed"
	StateCanceled = "canceled"
)

var (
	// ErrBusy means a run is already going; only one runs at a time.
	ErrBusy = errors.New("bench: a run is already in progress")
	// ErrNotRunning is returned by Cancel when there is nothing to cancel.
	ErrNotRunning = errors.New("bench: no run in progress")
	// ErrModel means the model file is missing or outside the models directory.
	ErrModel = errors.New("bench: model file not found in the models directory")
)

// Run is one benchmark, running or finished.
type Run struct {
	ID         int64     `json:"id"`
	State      string    `json:"state"`
	Setup      Setup     `json:"setup"`
	Build      string    `json:"build"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	Done       int       `json:"done"`
	Total      int       `json:"total"`
	// Current describes the last finished test, or the phase before any has.
	Current string   `json:"current"`
	Results []Result `json:"results"`
	Error   string   `json:"error,omitempty"`
}

// Store is the persistence a Runner needs; *store.Store satisfies it.
type Store interface {
	InsertBenchRun(ctx context.Context, r store.BenchRun) (int64, error)
	UpdateBenchRun(ctx context.Context, r store.BenchRun) error
	GetBenchRun(ctx context.Context, id int64) (store.BenchRun, error)
	ListBenchRuns(ctx context.Context, model string, limit int) ([]store.BenchRun, error)
}

// Options configures a Runner.
type Options struct {
	// Binary returns the active build's llama-bench and the build id to record.
	Binary func() (path, buildID string, err error)
	Models string // models directory
	// Pause frees the GPU, for example by unloading models and making the proxy
	// answer 503. Its resume is called once the run ends, whatever happens.
	Pause  func(ctx context.Context) (resume func(), err error)
	Store  Store
	Clock  func() time.Time
	Logger *slog.Logger
}

// Runner runs one llama-bench at a time.
type Runner struct {
	opt Options

	mu     sync.Mutex
	cur    *Run
	cancel context.CancelFunc
	done   chan struct{} // closed when cur finishes
}

// NewRunner returns a Runner with defaults filled in.
func NewRunner(opt Options) *Runner {
	if opt.Clock == nil {
		opt.Clock = time.Now
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	return &Runner{opt: opt}
}

// Start validates the setup and begins a run in the background. ctx only
// covers this preparation; the run outlives it and ends through Cancel.
func (r *Runner) Start(ctx context.Context, setup Setup) (Run, error) {
	setup = setup.Normalize()
	if err := setup.Validate(); err != nil {
		return Run{}, err
	}
	model, err := resolveModel(r.opt.Models, setup.ModelFile)
	if err != nil {
		return Run{}, err
	}

	r.mu.Lock()
	if r.cur != nil && r.cur.State == StateRunning {
		r.mu.Unlock()
		return Run{}, ErrBusy
	}
	// Reserve the slot while preparing, so a concurrent Start sees ErrBusy.
	r.cur, r.cancel = &Run{State: StateRunning, Setup: setup}, nil
	r.mu.Unlock()

	run, args, bin, err := r.prepare(ctx, setup, model)
	if err != nil {
		r.mu.Lock()
		r.cur = nil
		r.mu.Unlock()
		return Run{}, err
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r.mu.Lock()
	r.cur, r.cancel, r.done = &run, cancel, make(chan struct{})
	snapshot := r.snapshotLocked()
	r.mu.Unlock()

	go r.execute(runCtx, bin, args)
	return snapshot, nil
}

func (r *Runner) prepare(ctx context.Context, setup Setup, model string) (run Run, args []string, bin string, err error) {
	bin, build, err := r.opt.Binary()
	if err != nil {
		return Run{}, nil, "", fmt.Errorf("bench: no llama-bench available: %w", err)
	}
	help, err := helpText(ctx, bin)
	if err != nil {
		return Run{}, nil, "", err
	}
	args, err = setup.Args(model, ParseHelp(help))
	if err != nil {
		return Run{}, nil, "", err
	}
	run = Run{
		State: StateRunning, Setup: setup, Build: build, StartedAt: r.opt.Clock(),
		Total: setup.Estimate().Tests, Current: "Freeing the GPU", Results: []Result{},
	}
	run.ID, err = r.opt.Store.InsertBenchRun(ctx, toStore(run))
	if err != nil {
		return Run{}, nil, "", err
	}
	return run, args, bin, nil
}

// helpText runs llama-bench --help, which prints to stderr on some builds.
func helpText(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "--help")
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := reaper.Start(cmd); err != nil {
		return "", fmt.Errorf("bench: %w", err)
	}
	// --help exits nonzero on some builds, so only the text matters.
	_ = reaper.Wait(cmd)
	if out.Len() == 0 {
		return "", errors.New("bench: llama-bench --help printed nothing")
	}
	return out.String(), nil
}

// execute owns the run's lifecycle. resume is deferred so it runs after a
// panic too, and the final state is recorded last.
func (r *Runner) execute(ctx context.Context, bin string, args []string) {
	var err error
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("bench: panic: %v", p)
			r.opt.Logger.Error("bench run panicked", "panic", p)
		}
		r.finish(ctx, err)
	}()

	resume, err := r.opt.Pause(ctx)
	if err != nil {
		err = fmt.Errorf("bench: pausing the server: %w", err)
		return
	}
	defer resume()

	r.update(func(run *Run) { run.Current = "Starting llama-bench" })
	err = r.exec(ctx, bin, args)
}

func (r *Runner) exec(ctx context.Context, bin string, args []string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 3 * time.Second
	stderr := &tail{max: 2048}
	cmd.Stderr = stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := reaper.Start(cmd); err != nil {
		return err
	}

	sc := bufio.NewScanner(out)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		res, ok, perr := ParseLine(sc.Text())
		if perr != nil {
			r.opt.Logger.Warn("bench: skipping unparseable line", "err", perr)
			continue
		}
		if ok {
			r.update(func(run *Run) {
				run.Results = append(run.Results, res)
				run.Done = len(run.Results)
				run.Current = describe(res)
			})
		}
	}
	// A scan error (an enormous line) must not leave the child blocked on a full pipe.
	_, _ = io.Copy(io.Discard, out)
	werr := reaper.Wait(cmd)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if werr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("llama-bench: %w: %s", werr, msg)
		}
		return fmt.Errorf("llama-bench: %w", werr)
	}
	return nil
}

func describe(res Result) string {
	return fmt.Sprintf("%s · fa %s · %s / %s", res.Test, res.FlashAttn, res.CacheK, res.CacheV)
}

// finish records the outcome in memory and in the store.
func (r *Runner) finish(ctx context.Context, err error) {
	r.mu.Lock()
	run := r.cur
	switch {
	case errors.Is(err, context.Canceled) && ctx.Err() != nil:
		run.State = StateCanceled
	case err != nil:
		run.State, run.Error = StateFailed, err.Error()
	case len(run.Results) == 0:
		run.State, run.Error = StateFailed, "llama-bench produced no results"
	default:
		run.State = StateDone
	}
	run.FinishedAt = r.opt.Clock()
	run.Current = ""
	if run.State == StateDone {
		run.Done, run.Total = len(run.Results), len(run.Results)
	}
	row := toStore(*run)
	done := r.done
	r.mu.Unlock()

	if uerr := r.opt.Store.UpdateBenchRun(context.WithoutCancel(ctx), row); uerr != nil {
		r.opt.Logger.Error("bench: saving run", "id", row.ID, "err", uerr)
	}
	r.cancel()
	close(done)
}

func (r *Runner) update(f func(*Run)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(r.cur)
}

func (r *Runner) snapshotLocked() Run {
	run := *r.cur
	run.Results = append([]Result{}, run.Results...)
	return run
}

// Current returns the running run, or the last finished one; false before any.
func (r *Runner) Current() (Run, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil || r.cur.ID == 0 {
		return Run{}, false
	}
	return r.snapshotLocked(), true
}

// Cancel kills the running benchmark, or returns ErrNotRunning.
func (r *Runner) Cancel() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil || r.cur.State != StateRunning || r.cancel == nil {
		return ErrNotRunning
	}
	r.cancel()
	return nil
}

// Wait blocks until the current run has finished, or ctx ends.
func (r *Runner) Wait(ctx context.Context) error {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// List returns stored runs, newest first, for one model or all when empty.
func (r *Runner) List(ctx context.Context, model string, limit int) ([]Run, error) {
	rows, err := r.opt.Store.ListBenchRuns(ctx, model, limit)
	if err != nil {
		return nil, err
	}
	runs := make([]Run, len(rows))
	for i, row := range rows {
		runs[i] = fromStore(row)
	}
	return runs, nil
}

// Get returns a stored run with its results, or store.ErrNotFound.
func (r *Runner) Get(ctx context.Context, id int64) (Run, error) {
	row, err := r.opt.Store.GetBenchRun(ctx, id)
	if err != nil {
		return Run{}, err
	}
	return fromStore(row), nil
}

func toStore(run Run) store.BenchRun {
	setup, _ := json.Marshal(run.Setup)
	results, _ := json.Marshal(run.Results)
	return store.BenchRun{
		ID: run.ID, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, State: run.State,
		BuildID: run.Build, Model: run.Setup.ModelFile, Setup: setup, Results: results, Error: run.Error,
	}
}

// fromStore tolerates documents from other versions: bad JSON leaves fields empty.
func fromStore(row store.BenchRun) Run {
	run := Run{
		ID: row.ID, State: row.State, Build: row.BuildID, StartedAt: row.StartedAt,
		FinishedAt: row.FinishedAt, Error: row.Error,
	}
	_ = json.Unmarshal(row.Setup, &run.Setup)
	_ = json.Unmarshal(row.Results, &run.Results)
	run.Done = len(run.Results)
	run.Total = run.Setup.Estimate().Tests
	if run.State == StateDone {
		run.Done, run.Total = len(run.Results), len(run.Results)
	}
	return run
}

// resolveModel returns the absolute path of file inside dir, following links
// and refusing any that lead out.
func resolveModel(dir, file string) (string, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", ErrModel
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, file))
	if err != nil {
		return "", ErrModel
	}
	if rel, err := filepath.Rel(root, path); err != nil || !filepath.IsLocal(rel) {
		return "", ErrModel
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return "", ErrModel
	}
	return path, nil
}

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
