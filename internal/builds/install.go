package builds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/releases"
)

// JobState is the stage of an install.
type JobState string

// Job states, in order.
const (
	StateQueued      JobState = "queued"
	StateDownloading JobState = "downloading"
	StateVerifying   JobState = "verifying"
	StateExtracting  JobState = "extracting"
	StateProbing     JobState = "probing"
	StateDone        JobState = "done"
	StateFailed      JobState = "failed"
	StateCanceled    JobState = "canceled"
)

var (
	// ErrAlreadyInstalled means the build is on disk. The returned Job still has its ID.
	ErrAlreadyInstalled = errors.New("build already installed")
	// ErrJobActive means the build is being installed. The returned Job is that install.
	ErrJobActive = errors.New("build is already being installed")
	// ErrJobNotFound means there is no such job, or it has already finished.
	ErrJobNotFound = errors.New("no such install job")
	// ErrClosed means the Manager is shutting down.
	ErrClosed = errors.New("builds manager closed")
)

// Job is a snapshot of an install. Its ID is the id the build will have.
type Job struct {
	ID          string    `json:"id"`
	Version     string    `json:"version"`
	Backend     string    `json:"backend"`
	State       JobState  `json:"state"`
	Step        string    `json:"step"`
	Done        int64     `json:"done"`
	Total       int64     `json:"total"`
	BytesPerSec int64     `json:"bytes_per_sec"`
	Error       string    `json:"error,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at,omitzero"`
}

func (j Job) finished() bool {
	return j.State == StateDone || j.State == StateFailed || j.State == StateCanceled
}

type job struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	state    Job
	rateAt   time.Time
	rateDone int64
}

func (j *job) snapshot() Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state
}

func (j *job) setStep(state JobState, step string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.state.State = state
	j.state.Step = step
	j.state.BytesPerSec = 0
}

func (j *job) addDone(n int64, now time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.updateDone(j.state.Done+n, now)
}

func (j *job) setDone(done int64, now time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.updateDone(done, now)
	j.rateAt, j.rateDone = now, done
}

// updateDone refreshes the speed once a second, so that it does not jitter with every read.
func (j *job) updateDone(done int64, now time.Time) {
	j.state.Done = done
	if elapsed := now.Sub(j.rateAt); elapsed >= time.Second {
		j.state.BytesPerSec = int64(float64(done-j.rateDone) / elapsed.Seconds())
		j.rateAt, j.rateDone = now, done
	}
}

// Install starts installing a version ("stable", "nightly" or a tag) in the background. It
// resolves the release and its assets first, so a bad request fails here and not in the job.
// ctx only covers that part; the job lasts until it ends, Cancel or Close.
func (m *Manager) Install(ctx context.Context, version, backend string) (Job, error) {
	if m.External() {
		return Job{}, fmt.Errorf("%w: cannot install %s", ErrExternal, version)
	}
	if err := ValidateBackend(backend); err != nil {
		return Job{}, err
	}
	if version != string(releases.ChannelStable) && version != string(releases.ChannelNightly) {
		if err := releases.ValidateTag(version); err != nil {
			return Job{}, err
		}
	}
	rel, err := m.releases.ResolveVersion(ctx, version)
	if err != nil {
		return Job{}, err
	}
	id, err := BuildID(rel.BinaryTag, backend)
	if err != nil {
		return Job{}, err
	}
	if _, err := m.readManifest(id); err == nil {
		return Job{ID: id, Version: version, Backend: backend, State: StateDone}, fmt.Errorf("%w: %s", ErrAlreadyInstalled, id)
	}
	var gpu GPUInfo
	if backend == BackendCUDA {
		gpu.CUDAVersion = m.hardware(ctx).CUDAVersion
	}
	sel, err := SelectAssets(rel, backend, m.arch, gpu)
	if err != nil {
		return Job{}, err
	}

	total := sel.Main.Size
	if sel.CUDART != nil {
		total += sel.CUDART.Size
	}
	jobCtx, cancel := context.WithCancel(context.Background())
	j := &job{
		cancel: cancel,
		done:   make(chan struct{}),
		state: Job{
			ID: id, Version: version, Backend: backend, State: StateQueued, Step: "waiting for other installs",
			Total: total, StartedAt: m.now(),
		},
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		cancel()
		return Job{}, ErrClosed
	}
	if old, ok := m.jobs[id]; ok {
		if snap := old.snapshot(); !snap.finished() {
			cancel()
			return snap, fmt.Errorf("%w: %s", ErrJobActive, id)
		}
	}
	m.jobs[id] = j
	m.wg.Add(1)
	go m.runJob(jobCtx, j, rel, sel)
	return j.snapshot(), nil
}

// Jobs returns every job since startup, the latest per build id, oldest first.
func (m *Manager) Jobs() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		list = append(list, j.snapshot())
	}
	slices.SortFunc(list, func(a, b Job) int {
		if c := a.StartedAt.Compare(b.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return list
}

// Cancel stops a queued or running job. What was downloaded stays for a later resume.
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok || j.snapshot().finished() {
		return fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	j.cancel()
	return nil
}

// Wait blocks until a job finishes or ctx ends, and returns its latest snapshot.
func (m *Manager) Wait(ctx context.Context, id string) (Job, error) {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return Job{ID: id}, fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	select {
	case <-j.done:
		return j.snapshot(), nil
	case <-ctx.Done():
		return j.snapshot(), ctx.Err()
	}
}

// Close cancels all jobs and waits for them to clean up.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	for _, j := range m.jobs {
		j.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) runJob(ctx context.Context, j *job, rel releases.Release, sel Selection) {
	defer m.wg.Done()
	defer j.cancel()
	id := j.snapshot().ID

	err := m.install(ctx, j, rel, sel)

	j.mu.Lock()
	j.state.FinishedAt = m.now()
	j.state.BytesPerSec = 0
	switch {
	case err == nil:
		j.state.State = StateDone
		j.state.Step = ""
	case ctx.Err() != nil:
		j.state.State = StateCanceled
		j.state.Step = ""
	default:
		j.state.State = StateFailed
		j.state.Error = err.Error()
	}
	state := j.state.State
	j.mu.Unlock()
	close(j.done)

	if err != nil && state == StateFailed {
		m.log.Error("build install failed", "id", id, "err", err)
	} else {
		m.log.Info("build install finished", "id", id, "state", state)
	}
}

func (m *Manager) install(ctx context.Context, j *job, rel releases.Release, sel Selection) error {
	select {
	case m.slot <- struct{}{}:
		defer func() { <-m.slot }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := os.MkdirAll(m.tmpDir(), 0o755); err != nil { //nolint:gosec // holds no secrets
		return err
	}

	snap := j.snapshot()
	assets := []releases.Asset{sel.Main}
	if sel.CUDART != nil {
		assets = append(assets, *sel.CUDART)
	}

	paths := make([]string, len(assets))
	for i, a := range assets {
		j.setStep(StateDownloading, "downloading "+a.Name)
		p, err := m.download(ctx, j, a)
		if err != nil {
			return err
		}
		paths[i] = p
	}
	for i, a := range assets {
		j.setStep(StateVerifying, "verifying "+a.Name)
		if err := m.verifyDigest(ctx, paths[i], a); err != nil {
			return err
		}
	}

	staging := filepath.Join(m.tmpDir(), "staging-"+snap.ID)
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(staging); err != nil {
			m.log.Warn("removing staging directory", "path", staging, "err", err)
		}
	}()
	if err := os.Mkdir(staging, 0o755); err != nil { //nolint:gosec // binaries must be readable
		return err
	}
	tops := []string{"llama-" + rel.BinaryTag}
	for i, a := range assets {
		j.setStep(StateExtracting, "extracting "+a.Name)
		// The runtime tarball's top directory is named after the asset, not the tag.
		err := extract(ctx, paths[i], staging, append(slices.Clone(tops), strings.TrimSuffix(a.Name, archiveSuffix)), m.maxExtract)
		if err != nil {
			if ctx.Err() == nil {
				// Without a digest a corrupt file could otherwise fail the same way forever.
				_ = os.Remove(paths[i])
			}
			return err
		}
	}

	j.setStep(StateProbing, "running "+serverBinary)
	probe, err := m.probe(ctx, staging)
	if err != nil {
		return err
	}
	man := Manifest{
		ID: snap.ID, Tag: rel.BinaryTag, ReleaseTag: rel.Tag, Channel: rel.Channel, Backend: snap.Backend,
		Variant: sel.Variant, Arch: m.arch, InstalledAt: m.now().UTC(), Assets: assets, Probe: probe,
	}
	if err := writeManifest(staging, man); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	final := filepath.Join(m.dir, snap.ID)
	// No manifest (Install checked), so whatever is there was never a build.
	if err := os.RemoveAll(final); err != nil {
		return err
	}
	if err := os.Rename(staging, final); err != nil {
		return err
	}
	for _, p := range paths {
		if err := os.Remove(p); err != nil {
			m.log.Warn("removing download", "path", p, "err", err)
		}
	}
	return nil
}

func writeManifest(dir string, man Manifest) error {
	raw, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, manifestName+".tmp-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.Write(raw)
	if err == nil {
		err = f.Chmod(0o644)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, manifestName))
}
