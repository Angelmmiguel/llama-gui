package hardware

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const smiCSV = `0, NVIDIA GeForce RTX 4090, GPU-aaaa, 550.54.14, 24564, 1024, 37, 54
1, Tesla, Inc. K80, GPU-bbbb, 550.54.14, [N/A], [N/A], [Not Supported], [N/A]
`

const smiHeader = `Wed Nov  5 10:00:00 2026
+-----------------------------------------------------------------------------------------+
| NVIDIA-SMI 550.54.14              Driver Version: 550.54.14      CUDA Version: 12.4     |
|-----------------------------------------+------------------------+----------------------+
`

const cpuinfo = `processor	: 0
model name	: AMD Ryzen 9 7950X
physical id	: 0
core id		: 0
flags		: fpu sse4_2 avx avx2 avx512f avx512_vnni fma nx

processor	: 1
model name	: AMD Ryzen 9 7950X
physical id	: 0
core id		: 0
flags		: fpu sse4_2 avx avx2 avx512f avx512_vnni fma nx

processor	: 2
model name	: AMD Ryzen 9 7950X
physical id	: 0
core id		: 1
flags		: fpu sse4_2 avx avx2 avx512f avx512_vnni fma nx
`

const meminfo = `MemTotal:       65536 kB
MemFree:         1024 kB
MemAvailable:   32768 kB
Unparsable
`

func writeRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func smi(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return []byte(smiHeader), nil
	}
	return []byte(smiCSV), nil
}

func absent(context.Context, string, ...string) ([]byte, error) {
	return nil, &exec.Error{Name: "nvidia-smi", Err: exec.ErrNotFound}
}

func TestDetect(t *testing.T) {
	root := writeRoot(t, map[string]string{
		"proc/cpuinfo":                           cpuinfo,
		"proc/meminfo":                           meminfo,
		"dev/dri/renderD128":                     "",
		"dev/dri/renderD129":                     "",
		"dev/dri/renderD130":                     "",
		"dev/dri/card0":                          "",
		"sys/class/drm/renderD128/device/vendor": "0x10de\n",
		"sys/class/drm/renderD129/device/vendor": "0x8086\n",
	})
	d := &Detector{Run: smi, Root: root, DiskPaths: []string{root, filepath.Join(root, "missing")}}
	s := d.Detect(context.Background())

	if s.CUDAVersion != "12.4" {
		t.Errorf("CUDA version = %q", s.CUDAVersion)
	}
	if len(s.GPUs) != 2 {
		t.Fatalf("GPUs = %+v", s.GPUs)
	}
	g := s.GPUs[0]
	if g.Name != "NVIDIA GeForce RTX 4090" || g.UUID != "GPU-aaaa" || g.DriverVersion != "550.54.14" ||
		*g.MemoryTotal != 24564<<20 || *g.MemoryUsed != 1024<<20 || *g.Utilization != 37 || *g.Temperature != 54 {
		t.Errorf("GPU 0 = %+v", g)
	}
	g = s.GPUs[1]
	if g.Name != "Tesla, Inc. K80" || g.MemoryTotal != nil || g.MemoryUsed != nil || g.Utilization != nil || g.Temperature != nil {
		t.Errorf("GPU 1 = %+v", g)
	}

	want := []RenderNode{
		{"/dev/dri/renderD128", VendorNVIDIA, "0x10de"},
		{"/dev/dri/renderD129", VendorIntel, "0x8086"},
		{"/dev/dri/renderD130", VendorUnknown, ""},
	}
	if !reflect.DeepEqual(s.RenderNodes, want) {
		t.Errorf("render nodes = %+v", s.RenderNodes)
	}

	wantCPU := CPU{"AMD Ryzen 9 7950X", 2, 3, []string{"avx", "avx2", "avx512_vnni", "avx512f", "fma", "sse4_2"}}
	if !reflect.DeepEqual(s.CPU, wantCPU) {
		t.Errorf("CPU = %+v", s.CPU)
	}
	if s.Memory != (Memory{Total: 65536 << 10, Available: 32768 << 10}) {
		t.Errorf("memory = %+v", s.Memory)
	}
	if s.Disks[0].Total == 0 || s.Disks[0].Error != "" || s.Disks[1].Error == "" {
		t.Errorf("disks = %+v", s.Disks)
	}
	if RecommendBackend(s) != BackendCUDA {
		t.Errorf("recommend = %s", RecommendBackend(s))
	}
}

func TestDetectEmptyHost(t *testing.T) {
	s := (&Detector{Run: absent, Root: t.TempDir()}).Detect(context.Background())
	if len(s.GPUs) != 0 || len(s.Warnings) != 0 || len(s.RenderNodes) != 0 || s.CPU.Threads != 0 {
		t.Errorf("snapshot = %+v", s)
	}
	if RecommendBackend(s) != BackendCPU {
		t.Errorf("recommend = %s", RecommendBackend(s))
	}
}

func TestBrokenNvidiaSmiIsAWarning(t *testing.T) {
	run := func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("exit status 9") }
	s := (&Detector{Run: run, Root: t.TempDir()}).Detect(context.Background())
	if len(s.GPUs) != 0 || len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "exit status 9") {
		t.Errorf("snapshot = %+v", s)
	}
}

func TestNoDevicesLine(t *testing.T) {
	if g := parseGPUs([]byte("No devices were found\n")); len(g) != 0 {
		t.Errorf("GPUs = %+v", g)
	}
}

func TestSampleSkipsHeaderCommand(t *testing.T) {
	calls := 0
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		return smi(ctx, name, args...)
	}
	root := writeRoot(t, map[string]string{"proc/meminfo": meminfo})
	s := (&Detector{Run: run, Root: root}).Sample(context.Background())
	if calls != 1 || len(s.GPUs) != 2 || s.Memory.Total == 0 {
		t.Errorf("calls = %d, sample = %+v", calls, s)
	}
}

func TestRecommendVulkan(t *testing.T) {
	if b := RecommendBackend(Snapshot{RenderNodes: []RenderNode{{Vendor: VendorAMD}}}); b != BackendVulkan {
		t.Errorf("recommend = %s", b)
	}
}

func TestArmCPU(t *testing.T) {
	c := parseCPUInfo([]byte("processor\t: 0\nFeatures\t: fp asimd sve i8mm\n\nprocessor\t: 1\nFeatures\t: fp asimd sve i8mm\n"))
	if c.Threads != 2 || c.Cores != 2 || !reflect.DeepEqual(c.Features, []string{"asimd", "i8mm", "sve"}) {
		t.Errorf("CPU = %+v", c)
	}
}

func TestCanExecute(t *testing.T) {
	if err := CanExecute(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := CanExecute(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing directory passed")
	}
}

// A noexec mount cannot be created unprivileged, so this relies on /dev/shm when it is one.
func TestCanExecuteNoexec(t *testing.T) {
	err := CanExecute("/dev/shm")
	if err == nil || !errors.Is(err, os.ErrPermission) {
		t.Skipf("no usable noexec directory: %v", err)
	}
	if !errors.Is(err, ErrNoExec) {
		t.Errorf("err = %v", err)
	}
}
