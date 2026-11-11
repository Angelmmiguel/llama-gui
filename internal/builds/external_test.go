package builds

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Angelmmiguel/llama-gui/internal/hardware"
)

func newExternal(t *testing.T, script string) (*Manager, string, string) {
	t.Helper()
	ext := t.TempDir()
	if script != "" {
		writeServer(t, ext, script)
	}
	dir := filepath.Join(t.TempDir(), "builds")
	m, err := New(Options{
		Dir:      dir,
		External: ext,
		Releases: fakeReleases{},
		Hardware: func(context.Context) hardware.Snapshot { return hardware.Snapshot{} },
		Store:    &memStore{},
		Arch:     ArchX64,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m, ext, dir
}

func writeServer(t *testing.T, dir, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, serverBinary), []byte(script), 0o755); err != nil { //nolint:gosec // an executable
		t.Fatal(err)
	}
}

func TestExternalBuild(t *testing.T) {
	ctx := context.Background()
	m, ext, dir := newExternal(t, routerScript)

	if id, err := m.Active(ctx); err != nil || id != "" {
		t.Fatalf("before the probe: Active = %q, %v", id, err)
	}
	if list, _ := m.List(); len(list) != 0 {
		t.Fatalf("before the probe: List = %v", list)
	}
	if err := m.EnsureFirstInstall(ctx, "stable", AutoBackend); err != nil {
		t.Fatal(err)
	}

	if id, err := m.Active(ctx); err != nil || id != ExternalID {
		t.Fatalf("Active = %q, %v", id, err)
	}
	man, err := m.Get(ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if man.Tag != "b12132" || man.Backend != BackendVulkan || man.Path != ext || !man.Probe.Router {
		t.Errorf("manifest = %+v", man)
	}
	if list, _ := m.List(); len(list) != 1 || list[0].ID != ExternalID {
		t.Errorf("List = %v", list)
	}
	if bin, err := m.ServerBinary(ExternalID); err != nil || bin != filepath.Join(ext, serverBinary) {
		t.Errorf("ServerBinary = %q, %v", bin, err)
	}
	if _, err := m.Get("b12132-cpu"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get of an installed id: %v", err)
	}

	if err := m.Activate(ctx, ExternalID); err != nil {
		t.Errorf("Activate(external): %v", err)
	}
	for name, err := range map[string]error{
		"Activate": m.Activate(ctx, "b12132-cpu"),
		"Remove":   m.Remove(ctx, ExternalID),
		"Install":  func() error { _, err := m.Install(ctx, "stable", BackendCPU); return err }(),
	} {
		if !errors.Is(err, ErrExternal) {
			t.Errorf("%s: %v, want ErrExternal", name, err)
		}
	}
	if removed, err := m.ApplyRetention(ctx, 0); err != nil || len(removed) != 0 {
		t.Errorf("ApplyRetention = %v, %v", removed, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the builds directory was touched: %v", err)
	}
}

func TestExternalBuildProbeRetries(t *testing.T) {
	ctx := context.Background()
	m, ext, _ := newExternal(t, "")

	if err := m.EnsureFirstInstall(ctx, "stable", AutoBackend); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing binary: %v", err)
	}
	writeServer(t, ext, plainScript)
	if err := m.EnsureFirstInstall(ctx, "stable", AutoBackend); !errors.Is(err, ErrUnsupportedBuild) {
		t.Fatalf("no router mode: %v", err)
	}
	if id, _ := m.Active(ctx); id != "" {
		t.Fatalf("an unusable build is active: %q", id)
	}
	writeServer(t, ext, routerScript)
	if err := m.EnsureFirstInstall(ctx, "stable", AutoBackend); err != nil {
		t.Fatal(err)
	}
	if id, _ := m.Active(ctx); id != ExternalID {
		t.Errorf("Active = %q", id)
	}
}

func TestDeviceBackend(t *testing.T) {
	for _, tc := range []struct {
		devices []string
		want    string
	}{
		{nil, BackendCPU},
		{[]string{"CUDA0: NVIDIA GeForce RTX 4090 (24076 MiB, 23500 MiB free)"}, BackendCUDA},
		{[]string{"Vulkan0: AMD Radeon (RADV)", "Vulkan1: Intel"}, BackendVulkan},
		{[]string{"ROCm0: AMD Radeon RX 7900 XTX"}, BackendROCm},
		{[]string{"BLAS: BLAS (0 MiB, 0 MiB free)"}, BackendCPU},
		{[]string{"BLAS: BLAS", "CUDA0: NVIDIA"}, BackendCUDA},
		{[]string{"no colon here"}, BackendCPU},
	} {
		if got := deviceBackend(tc.devices); got != tc.want {
			t.Errorf("deviceBackend(%q) = %q, want %q", tc.devices, got, tc.want)
		}
	}
}
