// Package builds manages the llama.cpp builds installed on disk: installing them from GitHub
// releases, probing what they support, and switching between them.
package builds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/hardware"
	"github.com/Angelmmiguel/llama-gui/internal/releases"
)

const (
	manifestName     = "manifest.json"
	tmpDirName       = ".tmp"
	maxExtractBytes  = 32 << 30
	stallTimeout     = time.Minute
	partialRetention = 7 * 24 * time.Hour
	// NoInstall as the version of EnsureFirstInstall skips the install.
	NoInstall = "none"
	// AutoBackend as the backend of EnsureFirstInstall uses hardware.RecommendBackend.
	AutoBackend = "auto"
)

var (
	// ErrInvalidID means a build id is not "<tag>-<backend>".
	ErrInvalidID = errors.New("invalid build id")
	// ErrNotFound means the build is not installed.
	ErrNotFound = errors.New("build not installed")
	// ErrActiveBuild means the operation would remove the active build.
	ErrActiveBuild = errors.New("build is active")
	// ErrUnsupportedBuild means the build cannot run as a router.
	ErrUnsupportedBuild = errors.New("build does not support router mode")
)

// Releases is the part of the release client that installing needs.
type Releases interface {
	ResolveVersion(ctx context.Context, version string) (releases.Release, error)
}

// Store persists the id of the active build. An empty id means none.
type Store interface {
	ActiveBuild(ctx context.Context) (string, error)
	SetActiveBuild(ctx context.Context, id string) error
}

// Options configures a Manager. Dir, Releases, Hardware and Store are required.
type Options struct {
	// Dir holds one directory per build.
	Dir      string
	Releases Releases
	Hardware func(context.Context) hardware.Snapshot
	Store    Store
	Now      func() time.Time
	Logger   *slog.Logger
	// Run executes the installed binaries when probing, by default os/exec.
	Run Runner
	// HTTP downloads assets. It must not set an overall timeout.
	HTTP *http.Client
	// Arch overrides HostArch.
	Arch string
	// MaxExtractBytes bounds the unpacked size of each archive.
	MaxExtractBytes int64
}

// Manager owns the build directory.
type Manager struct {
	dir          string
	releases     Releases
	hardware     func(context.Context) hardware.Snapshot
	store        Store
	now          func() time.Time
	log          *slog.Logger
	run          Runner
	http         *http.Client
	arch         string
	maxExtract   int64
	stallTimeout time.Duration

	// opMu serializes what reads the active id and then acts on it.
	opMu sync.Mutex

	mu     sync.Mutex
	jobs   map[string]*job
	closed bool
	wg     sync.WaitGroup
	// slot lets one job work at a time; the others wait as queued.
	slot chan struct{}
}

// New returns a Manager. It does not touch the disk, so a bad volume cannot stop startup.
func New(o Options) (*Manager, error) {
	if o.Dir == "" || o.Releases == nil || o.Hardware == nil || o.Store == nil {
		return nil, errors.New("builds: Dir, Releases, Hardware and Store are required")
	}
	dir, err := filepath.Abs(o.Dir)
	if err != nil {
		return nil, fmt.Errorf("builds: %w", err)
	}
	m := &Manager{
		dir:          dir,
		releases:     o.Releases,
		hardware:     o.Hardware,
		store:        o.Store,
		now:          o.Now,
		log:          o.Logger,
		run:          o.Run,
		http:         o.HTTP,
		arch:         o.Arch,
		maxExtract:   o.MaxExtractBytes,
		stallTimeout: stallTimeout,
		jobs:         map[string]*job{},
		slot:         make(chan struct{}, 1),
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.log == nil {
		m.log = slog.New(slog.DiscardHandler)
	}
	if m.run == nil {
		m.run = runCommand
	}
	if m.http == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.ResponseHeaderTimeout = stallTimeout
		m.http = &http.Client{Transport: t}
	}
	if m.arch == "" {
		m.arch = HostArch()
	}
	if m.maxExtract <= 0 {
		m.maxExtract = maxExtractBytes
	}
	return m, nil
}

// Manifest describes an installed build. It is written last, so its presence means complete.
type Manifest struct {
	ID string `json:"id"`
	// Tag is the b* tag the binaries came from.
	Tag string `json:"tag"`
	// ReleaseTag is the tag asked for: the v* one for a stable release, otherwise Tag.
	ReleaseTag  string           `json:"release_tag"`
	Channel     releases.Channel `json:"channel"`
	Backend     string           `json:"backend"`
	Variant     string           `json:"variant,omitempty"`
	Arch        string           `json:"arch"`
	InstalledAt time.Time        `json:"installed_at"`
	Assets      []releases.Asset `json:"assets"`
	Probe       Probe            `json:"probe"`
}

// BuildID returns the id and directory name of a build.
func BuildID(tag, backend string) (string, error) {
	if err := releases.ValidateTag(tag); err != nil {
		return "", err
	}
	if err := ValidateBackend(backend); err != nil {
		return "", err
	}
	return tag + "-" + backend, nil
}

// ValidateID accepts only "<tag>-<backend>", which is a single safe path element.
func ValidateID(id string) error {
	for _, b := range backends {
		if tag, ok := strings.CutSuffix(id, "-"+b); ok && releases.ValidateTag(tag) == nil {
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrInvalidID, id)
}

func (m *Manager) tmpDir() string { return filepath.Join(m.dir, tmpDirName) }

func (m *Manager) readManifest(id string) (Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(m.dir, id, manifestName))
	if errors.Is(err, fs.ErrNotExist) {
		return Manifest{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Manifest{}, err
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return Manifest{}, fmt.Errorf("build %s: %w", id, err)
	}
	if man.ID != id {
		return Manifest{}, fmt.Errorf("build %s: manifest says %q", id, man.ID)
	}
	return man, nil
}

// List returns the installed builds, newest first. Directories without a readable manifest are
// not builds.
func (m *Manager) List() ([]Manifest, error) {
	entries, err := os.ReadDir(m.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []Manifest{}, nil
	}
	if err != nil {
		return nil, err
	}
	list := []Manifest{}
	for _, e := range entries {
		if !e.IsDir() || ValidateID(e.Name()) != nil {
			continue
		}
		man, err := m.readManifest(e.Name())
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				m.log.Warn("skipping build", "id", e.Name(), "err", err)
			}
			continue
		}
		list = append(list, man)
	}
	slices.SortStableFunc(list, func(a, b Manifest) int { return b.InstalledAt.Compare(a.InstalledAt) })
	return list, nil
}

// Get returns the manifest of a build.
func (m *Manager) Get(id string) (Manifest, error) {
	if err := ValidateID(id); err != nil {
		return Manifest{}, err
	}
	return m.readManifest(id)
}

// ServerBinary returns the absolute path of the build's llama-server.
func (m *Manager) ServerBinary(id string) (string, error) {
	if _, err := m.Get(id); err != nil {
		return "", err
	}
	return filepath.Join(m.dir, id, serverBinary), nil
}

// Active returns the id of the active build, or "" if there is none or it is gone from disk,
// which only means downloading it again.
func (m *Manager) Active(ctx context.Context) (string, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.active(ctx)
}

func (m *Manager) active(ctx context.Context) (string, error) {
	id, err := m.store.ActiveBuild(ctx)
	if err != nil {
		return "", fmt.Errorf("read active build: %w", err)
	}
	if id == "" {
		return "", nil
	}
	if _, err := m.Get(id); err != nil {
		m.log.Warn("active build is unusable", "id", id, "err", err)
		return "", nil
	}
	return id, nil
}

// Activate makes the build the active one, if it supports running as a router. Restarting the
// router on it is up to the caller.
func (m *Manager) Activate(ctx context.Context, id string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	man, err := m.Get(id)
	if err != nil {
		return err
	}
	if !man.Probe.Router {
		return fmt.Errorf("%w: %s", ErrUnsupportedBuild, id)
	}
	if err := m.store.SetActiveBuild(ctx, id); err != nil {
		return fmt.Errorf("activate %s: %w", id, err)
	}
	m.log.Info("build activated", "id", id)
	return nil
}

// Remove deletes a build other than the active one.
func (m *Manager) Remove(ctx context.Context, id string) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.remove(ctx, id)
}

func (m *Manager) remove(ctx context.Context, id string) error {
	active, err := m.active(ctx)
	if err != nil {
		return err
	}
	if id == active {
		return fmt.Errorf("%w: %s", ErrActiveBuild, id)
	}
	if _, err := m.readManifest(id); err != nil {
		return err
	}
	// Moving it out first means a crash halfway cannot leave a damaged build looking installed.
	if err := os.MkdirAll(m.tmpDir(), 0o755); err != nil { //nolint:gosec // holds no secrets
		return err
	}
	trash, err := os.MkdirTemp(m.tmpDir(), "trash-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(trash); err != nil {
			m.log.Warn("removing build leftovers", "path", trash, "err", err)
		}
	}()
	if err := os.Rename(filepath.Join(m.dir, id), filepath.Join(trash, id)); err != nil {
		return err
	}
	m.log.Info("build removed", "id", id)
	return nil
}

// ApplyRetention removes all builds but the active one and the newest keep, and old abandoned
// downloads. It returns the ids it removed.
func (m *Manager) ApplyRetention(ctx context.Context, keep int) ([]string, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	active, err := m.active(ctx)
	if err != nil {
		return nil, err
	}
	list, err := m.List()
	if err != nil {
		return nil, err
	}
	removed := []string{}
	var errs []error
	for i, man := range list {
		if i < keep || man.ID == active {
			continue
		}
		if err := m.remove(ctx, man.ID); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, man.ID)
	}
	m.pruneTmp()
	return removed, errors.Join(errs...)
}

// pruneTmp deletes partial downloads that nobody came back for, and the leftovers of a crash.
// Those are always safe to remove, as a job in progress keeps touching its own.
func (m *Manager) pruneTmp() {
	entries, err := os.ReadDir(m.tmpDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || m.now().Sub(info.ModTime()) < partialRetention {
			continue
		}
		if err := os.RemoveAll(filepath.Join(m.tmpDir(), e.Name())); err != nil {
			m.log.Warn("pruning temporary files", "name", e.Name(), "err", err)
		}
	}
}

// Available lists the backends a release offers for an architecture, with a reason on those the
// detected hardware cannot use. The release must come from ResolveVersion to have its binaries.
func (m *Manager) Available(ctx context.Context, rel releases.Release, arch string) ([]Option, error) {
	return availableOptions(rel, arch, m.hardware(ctx))
}

// EnsureFirstInstall installs and activates a build when there is no active one, blocking until
// it is done, so run it in the background. Version NoInstall does nothing, and backend
// AutoBackend follows the hardware.
func (m *Manager) EnsureFirstInstall(ctx context.Context, version, backend string) error {
	if version == NoInstall {
		return nil
	}
	active, err := m.Active(ctx)
	if err != nil {
		return err
	}
	if active != "" {
		return nil
	}
	if backend == AutoBackend {
		backend = hardware.RecommendBackend(m.hardware(ctx))
	}
	j, err := m.Install(ctx, version, backend)
	if err != nil && !errors.Is(err, ErrAlreadyInstalled) && !errors.Is(err, ErrJobActive) {
		return err
	}
	if !errors.Is(err, ErrAlreadyInstalled) {
		j, err = m.Wait(ctx, j.ID)
		if err != nil {
			_ = m.Cancel(j.ID)
			return err
		}
		if j.State != StateDone {
			return fmt.Errorf("install %s: %s: %s", j.ID, j.State, j.Error)
		}
	}
	return m.Activate(ctx, j.ID)
}
