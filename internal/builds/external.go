package builds

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Angelmmiguel/llama-gui/internal/releases"
)

var deviceRE = regexp.MustCompile(`^([A-Za-z]+)[0-9]*:`)

// ExternalID is the id of the build in Options.External.
const ExternalID = "external"

// ErrExternal means the build comes from Options.External, so builds are not installed,
// switched or removed here.
var ErrExternal = errors.New("the llama.cpp build is provided externally")

// External reports whether the build comes from Options.External.
func (m *Manager) External() bool { return m.external != "" }

// probeExternal probes the build in Options.External once it has succeeded, so a build that
// failed, perhaps because a GPU driver was not loaded yet, is tried again on the next call.
func (m *Manager) probeExternal(ctx context.Context) error {
	m.extMu.Lock()
	defer m.extMu.Unlock()
	if m.extMan != nil {
		return nil
	}
	info, err := os.Stat(filepath.Join(m.external, serverBinary))
	if err != nil {
		return fmt.Errorf("external build: %w", err)
	}
	p, err := m.probe(ctx, m.external)
	if err != nil {
		return fmt.Errorf("external build in %s: %w", m.external, err)
	}
	if !p.Router {
		return fmt.Errorf("%w: %s", ErrUnsupportedBuild, m.external)
	}
	tag := p.Version
	if _, err := strconv.Atoi(p.Build); err == nil {
		tag = "b" + p.Build
	}
	m.extMan = &Manifest{
		ID:          ExternalID,
		Tag:         tag,
		ReleaseTag:  tag,
		Channel:     releases.ChannelNone,
		Backend:     deviceBackend(p.Devices),
		Arch:        m.arch,
		InstalledAt: info.ModTime(),
		Probe:       p,
		Path:        m.external,
		SizeBytes:   info.Size(),
	}
	m.log.Info("using the external build", "dir", m.external, "tag", tag, "backend", m.extMan.Backend)
	return nil
}

// externalManifest returns the probed external build, or ErrNotFound before a probe succeeds.
func (m *Manager) externalManifest() (Manifest, error) {
	m.extMu.Lock()
	defer m.extMu.Unlock()
	if m.extMan == nil {
		return Manifest{}, fmt.Errorf("%w: %s", ErrNotFound, ExternalID)
	}
	return *m.extMan, nil
}

// deviceBackend names the backend of the first accelerator --list-devices printed, as in
// "CUDA0: NVIDIA GeForce RTX 4090 (24076 MiB, 23500 MiB free)". BLAS runs on the CPU, so a
// build with nothing else, or with no device at all, is a CPU build.
func deviceBackend(devices []string) string {
	for _, d := range devices {
		m := deviceRE.FindStringSubmatch(d)
		if m == nil {
			continue
		}
		if name := strings.ToLower(m[1]); name != "cpu" && name != "blas" {
			return name
		}
	}
	return BackendCPU
}
