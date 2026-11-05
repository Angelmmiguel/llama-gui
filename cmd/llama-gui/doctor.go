package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/app"
	"github.com/Angelmmiguel/llama-gui/internal/app/reaper"
	"github.com/Angelmmiguel/llama-gui/internal/builds"
	"github.com/Angelmmiguel/llama-gui/internal/config"
	"github.com/Angelmmiguel/llama-gui/internal/hardware"
	"github.com/Angelmmiguel/llama-gui/internal/store"
)

const (
	doctorNetTimeout  = 5 * time.Second
	doctorExecTimeout = 15 * time.Second
	diskWarnBytes     = 10 << 30
	diskFailBytes     = 1 << 30
)

// doctorSites are checked for reachability. Any HTTP answer counts.
var doctorSites = []struct{ name, url string }{
	{"GitHub", "https://api.github.com"},
	{"Hugging Face", "https://huggingface.co"},
}

// report prints check lines and remembers whether any failed.
type report struct {
	w      io.Writer
	failed bool
}

func (r *report) pass(check, format string, args ...any) { r.line("PASS", check, format, args...) }
func (r *report) warn(check, format string, args ...any) { r.line("WARN", check, format, args...) }
func (r *report) fail(check, format string, args ...any) {
	r.failed = true
	r.line("FAIL", check, format, args...)
}

func (r *report) line(status, check, format string, args ...any) {
	_, _ = fmt.Fprintf(r.w, "%s  %-12s %s\n", status, check, fmt.Sprintf(format, args...))
}

// doctor needs no running service: it looks at this machine the way serve would.
func doctor(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	cfg, err := config.Load(args, getenv)
	if errors.Is(err, config.ErrHelp) {
		usage(stdout)
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "llama-gui: %v\n", err)
		return 2
	}
	r := &report{w: stdout}
	ctx := context.Background()
	checkBuildsDir(r, cfg.Builds)
	checkDataDir(r, cfg.Data)
	checkModelsDir(r, cfg.Models)

	snap := hardware.NewDetector(cfg.Data, cfg.Builds, cfg.Models).Detect(ctx)
	checkDisks(r, snap.Disks)
	checkGPUs(r, snap)
	checkNetwork(ctx, r)
	checkActiveBuild(ctx, r, cfg)

	if r.failed {
		return 1
	}
	return 0
}

func checkBuildsDir(r *report, dir string) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		r.fail("builds", "%s cannot be created: %v", dir, err)
		return
	}
	if err := hardware.CanExecute(dir); err != nil {
		r.fail("builds", "%v", err)
		return
	}
	r.pass("builds", "%s is writable and allows execution", dir)
}

func checkDataDir(r *report, dir string) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		r.fail("data", "%s cannot be created: %v", dir, err)
		return
	}
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		r.fail("data", "%s is not writable: %v", dir, err)
		return
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	r.pass("data", "%s is writable", dir)
}

func checkModelsDir(r *report, dir string) {
	if _, err := os.ReadDir(dir); err != nil {
		r.warn("models", "%s cannot be read: %v", dir, err)
		return
	}
	r.pass("models", "%s is readable", dir)
}

func checkDisks(r *report, disks []hardware.Disk) {
	for _, d := range disks {
		switch {
		case d.Error != "":
			r.warn("disk", "%s: %s", d.Path, d.Error)
		case d.Free < diskFailBytes:
			r.fail("disk", "%s has only %.1f GiB free", d.Path, gib(d.Free))
		case d.Free < diskWarnBytes:
			r.warn("disk", "%s has only %.1f GiB free", d.Path, gib(d.Free))
		default:
			r.pass("disk", "%s has %.1f GiB free", d.Path, gib(d.Free))
		}
	}
}

func gib(n uint64) float64 { return float64(n) / (1 << 30) }

func checkGPUs(r *report, snap hardware.Snapshot) {
	switch {
	case len(snap.GPUs) > 0:
		names := make([]string, len(snap.GPUs))
		for i, g := range snap.GPUs {
			names[i] = g.Name
		}
		r.pass("gpu", "%s (recommended backend: %s)", strings.Join(names, ", "), hardware.RecommendBackend(snap))
	case len(snap.RenderNodes) > 0:
		r.pass("gpu", "%d render node(s) (recommended backend: %s)", len(snap.RenderNodes), hardware.RecommendBackend(snap))
	default:
		r.warn("gpu", "no GPU visible, llama.cpp will run on the CPU")
	}
	for _, w := range snap.Warnings {
		r.warn("gpu", "%s", w)
	}
}

func checkNetwork(ctx context.Context, r *report) {
	client := http.Client{Timeout: doctorNetTimeout}
	for _, site := range doctorSites {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, site.url, nil)
		if err != nil {
			r.warn("network", "%s: %v", site.name, err)
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			r.warn("network", "%s is not reachable: %v", site.name, err)
			continue
		}
		_ = resp.Body.Close()
		r.pass("network", "%s is reachable", site.name)
	}
}

func checkActiveBuild(ctx context.Context, r *report, cfg config.Config) {
	db := filepath.Join(cfg.Data, app.DBFile)
	if _, err := os.Stat(db); err != nil {
		r.warn("active build", "none yet: the service has not run with this data directory")
		return
	}
	st, err := store.Open(db)
	if err != nil {
		r.fail("active build", "reading %s: %v", db, err)
		return
	}
	defer func() { _ = st.Close() }()
	id, err := builds.StoreFromKV(st).ActiveBuild(ctx)
	if err != nil {
		r.fail("active build", "reading %s: %v", db, err)
		return
	}
	if id == "" {
		r.warn("active build", "none is active")
		return
	}
	if err := builds.ValidateID(id); err != nil {
		r.fail("active build", "%v", err)
		return
	}
	out, err := runVersion(ctx, filepath.Join(cfg.Builds, id, "llama-server"))
	if err != nil {
		r.fail("active build", "%s does not run: %v: %s", id, err, out)
		return
	}
	r.pass("active build", "%s runs: %s", id, out)
}

// runVersion runs --version, keeping the output for what it says about missing
// libraries, which is the usual way a prebuilt fails.
func runVersion(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, doctorExecTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = time.Second
	err := reaper.Start(cmd)
	if err == nil {
		err = reaper.Wait(cmd)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if err != nil {
		return strings.Join(lines[max(0, len(lines)-3):], " "), err
	}
	for _, l := range lines {
		if strings.Contains(l, "version:") {
			return strings.TrimSpace(l), nil
		}
	}
	return lines[0], nil
}
