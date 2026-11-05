package builds

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/releases"
	"github.com/Angelmmiguel/llama-gui/internal/store"
)

// Update policies.
const (
	UpdatesOff    = "off"
	UpdatesNotify = "notify"
	UpdatesAuto   = "auto"
)

const (
	defaultUpdateInterval = 6 * time.Hour
	defaultIdlePoll       = 30 * time.Second
	resolveTimeout        = time.Minute
)

// UpdateOptions configures an UpdateChecker. Policy, Releases and Active are
// required; Auto needs the rest too.
type UpdateOptions struct {
	Policy   string
	Releases Releases
	// Active returns the active build, or false when there is none.
	Active func(ctx context.Context) (Manifest, bool)
	// Install, Wait and Activate carry out an automatic update.
	Install  func(ctx context.Context, version, backend string) (Job, error)
	Wait     func(ctx context.Context, id string) (Job, error)
	Activate func(ctx context.Context, id string) error
	// InFlight says how many requests an automatic switch would interrupt.
	InFlight func() int
	Interval time.Duration
	IdlePoll time.Duration
	Now      func() time.Time
	Logger   *slog.Logger
}

// UpdateInfo is what the last check found.
type UpdateInfo struct {
	// Available says the active build's channel has something newer.
	Available bool `json:"available"`
	// Latest is that newer version's tag.
	Latest    string    `json:"latest,omitempty"`
	Stable    string    `json:"stable,omitempty"`
	Nightly   string    `json:"nightly,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
	Error     string    `json:"error,omitempty"`
}

// UpdateChecker looks for new releases on a schedule and acts on its policy.
type UpdateChecker struct {
	o   UpdateOptions
	log *slog.Logger

	mu              sync.Mutex
	stable, nightly releases.Release
	checkedAt       time.Time
	lastErr         string
}

// NewUpdateChecker returns a checker. Run starts it.
func NewUpdateChecker(o UpdateOptions) *UpdateChecker {
	if o.Interval <= 0 {
		o.Interval = defaultUpdateInterval
	}
	if o.IdlePoll <= 0 {
		o.IdlePoll = defaultIdlePoll
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &UpdateChecker{o: o, log: log}
}

// Run checks at once and then every interval until ctx ends. Policy off does nothing.
func (c *UpdateChecker) Run(ctx context.Context) {
	if c.o.Policy != UpdatesNotify && c.o.Policy != UpdatesAuto {
		return
	}
	ticker := time.NewTicker(c.o.Interval)
	defer ticker.Stop()
	for {
		c.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Check refreshes what is known and, under policy auto, updates a stable build.
func (c *UpdateChecker) Check(ctx context.Context) {
	c.refresh(ctx)
	if c.o.Policy == UpdatesAuto {
		c.autoUpdate(ctx)
	}
}

// Refresh resolves both channels now and returns the result, without acting on it.
func (c *UpdateChecker) Refresh(ctx context.Context) UpdateInfo {
	c.refresh(ctx)
	return c.Info(ctx)
}

// refresh resolves both channels. A failure is remembered and otherwise ignored: what was
// found before stays, and the next check tries again.
func (c *UpdateChecker) refresh(ctx context.Context) {
	stable, stableErr := c.resolve(ctx, string(releases.ChannelStable))
	nightly, nightlyErr := c.resolve(ctx, string(releases.ChannelNightly))
	err := errors.Join(stableErr, nightlyErr)

	c.mu.Lock()
	if stableErr == nil {
		c.stable = stable
	}
	if nightlyErr == nil {
		c.nightly = nightly
	}
	c.checkedAt = c.o.Now()
	c.lastErr = ""
	if err != nil {
		c.lastErr = err.Error()
	}
	c.mu.Unlock()

	switch {
	case err == nil:
	case errors.Is(err, releases.ErrRateLimited):
		c.log.Info("update check skipped, GitHub's rate limit was hit", "error", err)
	case ctx.Err() == nil:
		c.log.Warn("update check failed", "error", err)
	}
}

func (c *UpdateChecker) resolve(ctx context.Context, version string) (releases.Release, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	return c.o.Releases.ResolveVersion(ctx, version)
}

// Info compares what the last check found with the build active right now.
func (c *UpdateChecker) Info(ctx context.Context) UpdateInfo {
	c.mu.Lock()
	info := UpdateInfo{Stable: c.stable.Tag, Nightly: c.nightly.Tag, CheckedAt: c.checkedAt, Error: c.lastErr}
	stable, nightly := c.stable, c.nightly
	c.mu.Unlock()
	if c.o.Policy == UpdatesOff || c.o.Policy == "" {
		return info
	}
	active, ok := c.o.Active(ctx)
	if !ok {
		return info
	}
	latest := nightly
	if active.Channel == releases.ChannelStable {
		latest = stable
	}
	if newerBuild(latest, active.Tag) {
		info.Available, info.Latest = true, latest.Tag
	}
	return info
}

func (c *UpdateChecker) autoUpdate(ctx context.Context) {
	c.mu.Lock()
	stable := c.stable
	c.mu.Unlock()
	active, ok := c.o.Active(ctx)
	if !ok || active.Channel != releases.ChannelStable || !newerBuild(stable, active.Tag) {
		return
	}
	job, err := c.o.Install(ctx, stable.Tag, active.Backend)
	if err != nil && !errors.Is(err, ErrAlreadyInstalled) && !errors.Is(err, ErrJobActive) {
		c.log.Warn("automatic update failed", "version", stable.Tag, "error", err)
		return
	}
	if !errors.Is(err, ErrAlreadyInstalled) {
		if job, err = c.o.Wait(ctx, job.ID); err != nil {
			return
		}
		if job.State != StateDone {
			c.log.Warn("automatic update failed", "version", stable.Tag, "state", job.State, "error", job.Error)
			return
		}
	}
	ticker := time.NewTicker(c.o.IdlePoll)
	defer ticker.Stop()
	for c.o.InFlight() > 0 {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
	if err := c.o.Activate(ctx, job.ID); err != nil {
		c.log.Warn("automatic update could not switch", "build", job.ID, "error", err)
		return
	}
	c.log.Info("updated automatically", "build", job.ID)
}

// newerBuild compares b* numbers: they grow with every merge, and stable releases
// point at one through BinaryTag.
func newerBuild(rel releases.Release, tag string) bool {
	latest, ok := buildNumber(rel.BinaryTag)
	if !ok {
		return false
	}
	current, ok := buildNumber(tag)
	return ok && latest > current
}

func buildNumber(tag string) (int, bool) {
	s, ok := strings.CutPrefix(tag, "b")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// ActiveManifest returns the active build, or false when there is none.
func (m *Manager) ActiveManifest(ctx context.Context) (Manifest, bool) {
	id, err := m.Active(ctx)
	if err != nil || id == "" {
		return Manifest{}, false
	}
	man, err := m.Get(id)
	return man, err == nil
}

// ActiveBuildKey is where the id of the active build lives in the kv table.
const ActiveBuildKey = "active_build"

// KV is the part of internal/store that StoreFromKV needs.
type KV interface {
	GetKV(ctx context.Context, key string) (string, error)
	SetKV(ctx context.Context, key, value string) error
}

// StoreFromKV keeps the active build's id in a key-value store.
func StoreFromKV(kv KV) Store { return kvStore{kv} }

type kvStore struct{ kv KV }

func (s kvStore) ActiveBuild(ctx context.Context) (string, error) {
	id, err := s.kv.GetKV(ctx, ActiveBuildKey)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	return id, err
}

func (s kvStore) SetActiveBuild(ctx context.Context, id string) error {
	return s.kv.SetKV(ctx, ActiveBuildKey, id)
}
