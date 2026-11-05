package builds

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/app/reaper"
)

const (
	serverBinary = "llama-server"
	probeTimeout = 30 * time.Second
	routerFlag   = "--models-preset"
)

// Runner runs a command and returns its combined stdout and stderr.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Probe is what a build says about itself.
type Probe struct {
	Version string `json:"version"`
	Build   string `json:"build"`
	Commit  string `json:"commit"`
	// VersionOutput is kept because the format can change between builds.
	VersionOutput string   `json:"version_output"`
	Flags         []string `json:"flags"`
	Router        bool     `json:"router"`
	Devices       []string `json:"devices"`
}

func (m *Manager) probe(ctx context.Context, dir string) (Probe, error) {
	bin := filepath.Join(dir, serverBinary)
	run := func(arg string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		return m.run(ctx, bin, arg)
	}

	out, err := run("--version")
	if err != nil {
		if ctx.Err() != nil {
			return Probe{}, ctx.Err()
		}
		return Probe{}, fmt.Errorf("%s --version: %w: %s", serverBinary, err, tail(out))
	}
	p := parseVersion(out)

	// The two below have no stable schema, so failing is not fatal. A build without flags
	// simply does not count as supporting the router.
	if out, err := run("--help"); err == nil {
		p.Flags = parseFlags(out)
	} else {
		m.log.Warn("probe --help failed", "dir", dir, "err", err)
	}
	p.Router = slices.Contains(p.Flags, routerFlag)
	if out, err := run("--list-devices"); err == nil {
		p.Devices = parseDevices(out)
	} else {
		m.log.Warn("probe --list-devices failed", "dir", dir, "err", err)
	}
	if err := ctx.Err(); err != nil {
		return Probe{}, err
	}
	return p, nil
}

func tail(out []byte) string {
	const limit = 500
	s := strings.TrimSpace(string(out))
	if len(s) > limit {
		s = s[len(s)-limit:]
	}
	return s
}

var (
	versionRE    = regexp.MustCompile(`version:\s*(\S+)\s*\(build\s+(\w+),\s*commit\s+(\w+)\)`)
	oldVersionRE = regexp.MustCompile(`version:\s*(\d+)\s*\((\w+)\)`)
	anyVersionRE = regexp.MustCompile(`version:\s*(\S+)`)
	flagRE       = regexp.MustCompile(`^--?[A-Za-z0-9][A-Za-z0-9-]*$`)
)

func parseVersion(out []byte) Probe {
	p := Probe{VersionOutput: tail(out)}
	if m := versionRE.FindSubmatch(out); m != nil {
		p.Version, p.Build, p.Commit = string(m[1]), string(m[2]), string(m[3])
	} else if m := oldVersionRE.FindSubmatch(out); m != nil {
		p.Version, p.Build, p.Commit = string(m[1]), string(m[1]), string(m[2])
	} else if m := anyVersionRE.FindSubmatch(out); m != nil {
		p.Version = string(m[1])
	}
	return p
}

// parseFlags reads the left column of --help, as in "-kvo, --kv-offload, -nkvo, --no-kv-offload"
// or "-t, --threads N   text". Continuation lines are deeply indented and can mention other flags.
func parseFlags(out []byte) []string {
	seen := map[string]bool{}
	for line := range strings.Lines(string(out)) {
		if len(line)-len(strings.TrimLeft(line, " ")) > 4 {
			continue
		}
		for _, field := range strings.Fields(line) {
			name, comma := strings.CutSuffix(field, ",")
			if flagRE.MatchString(name) {
				seen[name] = true
			} else if !comma {
				break
			}
		}
	}
	flags := make([]string, 0, len(seen))
	for f := range seen {
		flags = append(flags, f)
	}
	slices.Sort(flags)
	return flags
}

func parseDevices(out []byte) []string {
	_, list, ok := strings.Cut(string(out), "Available devices:")
	if !ok {
		return nil
	}
	var devices []string
	for line := range strings.Lines(list) {
		if line = strings.TrimSpace(line); line != "" && line != "(none)" {
			devices = append(devices, line)
		}
	}
	return devices
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.WaitDelay = time.Second
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		err := reaper.Start(cmd)
		if err == nil {
			err = reaper.Wait(cmd)
		}
		out := buf.Bytes()
		// A concurrent fork can briefly hold a just-written binary open for writing.
		if errors.Is(err, syscall.ETXTBSY) && attempt < 5 {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		return out, err
	}
}
