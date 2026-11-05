// Package hardware detects the GPUs, CPU, memory and disks of the host.
package hardware

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/app/reaper"
)

const commandTimeout = 5 * time.Second

// Backend names returned by RecommendBackend.
const (
	BackendCUDA   = "cuda"
	BackendVulkan = "vulkan"
	BackendCPU    = "cpu"
)

// Vendor names of render nodes.
const (
	VendorAMD     = "AMD"
	VendorIntel   = "Intel"
	VendorNVIDIA  = "NVIDIA"
	VendorUnknown = "unknown"
)

// Runner runs a command and returns its stdout.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Snapshot is everything known about the host at one moment. Detection never fails as a
// whole: what could not be read stays empty and, when surprising, is noted in Warnings.
type Snapshot struct {
	GPUs        []GPU        `json:"gpus"`
	CUDAVersion string       `json:"cuda_version"`
	RenderNodes []RenderNode `json:"render_nodes"`
	CPU         CPU          `json:"cpu"`
	Memory      Memory       `json:"memory"`
	Disks       []Disk       `json:"disks"`
	Warnings    []string     `json:"warnings,omitempty"`
}

// Sample is the subset of a Snapshot that changes from second to second.
type Sample struct {
	GPUs   []GPU  `json:"gpus"`
	Memory Memory `json:"memory"`
}

// GPU is one NVIDIA GPU. A nil number means nvidia-smi reported it as not available.
type GPU struct {
	Index         int     `json:"index"`
	Name          string  `json:"name"`
	UUID          string  `json:"uuid"`
	DriverVersion string  `json:"driver_version"`
	MemoryTotal   *uint64 `json:"memory_total"`
	MemoryUsed    *uint64 `json:"memory_used"`
	Utilization   *int    `json:"utilization"`
	Temperature   *int    `json:"temperature"`
}

// RenderNode is a /dev/dri render node.
type RenderNode struct {
	Path     string `json:"path"`
	Vendor   string `json:"vendor"`
	VendorID string `json:"vendor_id"`
}

// CPU describes the processor. Cores counts physical cores when /proc/cpuinfo exposes the
// topology, and otherwise equals Threads.
type CPU struct {
	Model    string   `json:"model"`
	Cores    int      `json:"cores"`
	Threads  int      `json:"threads"`
	Features []string `json:"features"`
}

// Memory is RAM in bytes.
type Memory struct {
	Total     uint64 `json:"total"`
	Available uint64 `json:"available"`
}

// Disk is the space in bytes of the filesystem holding Path.
type Disk struct {
	Path  string `json:"path"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
	Error string `json:"error,omitempty"`
}

// Detector reads the host. Run and Root are replaceable for tests.
type Detector struct {
	// Run executes external commands, by default os/exec.
	Run Runner
	// Root prefixes /proc, /sys and /dev.
	Root string
	// DiskPaths are the paths reported in Snapshot.Disks.
	DiskPaths []string
}

// NewDetector returns a Detector for the real host reporting the given disk paths.
func NewDetector(diskPaths ...string) *Detector {
	return &Detector{Run: runCommand, Root: "/", DiskPaths: diskPaths}
}

// Detect takes a complete snapshot.
func (d *Detector) Detect(ctx context.Context) Snapshot {
	var s Snapshot
	var warn string
	s.GPUs, warn = d.gpus(ctx)
	if warn != "" {
		s.Warnings = append(s.Warnings, warn)
	}
	if len(s.GPUs) > 0 {
		s.CUDAVersion = d.cudaVersion(ctx)
	}
	s.RenderNodes = d.renderNodes()
	s.CPU = d.cpu()
	s.Memory = d.memory()
	s.Disks = disks(d.DiskPaths)
	return s
}

// Sample cheaply reads GPU and memory use, for calling once a second.
func (d *Detector) Sample(ctx context.Context) Sample {
	gpus, _ := d.gpus(ctx)
	return Sample{GPUs: gpus, Memory: d.memory()}
}

// RecommendBackend picks cuda on NVIDIA, vulkan with any render node, and cpu otherwise.
func RecommendBackend(s Snapshot) string {
	switch {
	case len(s.GPUs) > 0:
		return BackendCUDA
	case len(s.RenderNodes) > 0:
		return BackendVulkan
	}
	return BackendCPU
}

const gpuQuery = "--query-gpu=index,name,uuid,driver_version,memory.total,memory.used,utilization.gpu,temperature.gpu"

func (d *Detector) gpus(ctx context.Context) ([]GPU, string) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	out, err := d.Run(ctx, "nvidia-smi", gpuQuery, "--format=csv,noheader,nounits")
	if errors.Is(err, exec.ErrNotFound) {
		return nil, ""
	}
	if err != nil {
		return nil, fmt.Sprintf("nvidia-smi: %v", err)
	}
	return parseGPUs(out), ""
}

func parseGPUs(out []byte) []GPU {
	var gpus []GPU
	for line := range strings.Lines(string(out)) {
		f := strings.Split(line, ",")
		if len(f) < 8 {
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		index, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		// Only the name can hold a comma, so the other columns are counted from the ends.
		tail := f[len(f)-6:]
		gpus = append(gpus, GPU{
			Index:         index,
			Name:          strings.Join(f[1:len(f)-6], ", "),
			UUID:          available(tail[0]),
			DriverVersion: available(tail[1]),
			MemoryTotal:   mebibytes(tail[2]),
			MemoryUsed:    mebibytes(tail[3]),
			Utilization:   number(tail[4]),
			Temperature:   number(tail[5]),
		})
	}
	return gpus
}

// available blanks nvidia-smi's "[N/A]" and "[Not Supported]" style placeholders.
func available(v string) string {
	if v == "N/A" || strings.HasPrefix(v, "[") {
		return ""
	}
	return v
}

func number(v string) *int {
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &n
}

func mebibytes(v string) *uint64 {
	n, err := strconv.ParseUint(v, 10, 44)
	if err != nil {
		return nil
	}
	n <<= 20
	return &n
}

var cudaVersionRE = regexp.MustCompile(`CUDA Version:\s*(\d+(?:\.\d+)*)`)

func (d *Detector) cudaVersion(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	out, err := d.Run(ctx, "nvidia-smi")
	if err != nil {
		return ""
	}
	if m := cudaVersionRE.FindSubmatch(out); m != nil {
		return string(m[1])
	}
	return ""
}

func (d *Detector) renderNodes() []RenderNode {
	paths, err := filepath.Glob(filepath.Join(d.Root, "dev/dri/renderD*"))
	if err != nil {
		return nil
	}
	var nodes []RenderNode
	for _, p := range paths {
		name := filepath.Base(p)
		id := ""
		if raw, err := os.ReadFile(filepath.Join(d.Root, "sys/class/drm", name, "device/vendor")); err == nil {
			id = strings.ToLower(strings.TrimSpace(string(raw)))
		}
		nodes = append(nodes, RenderNode{Path: "/dev/dri/" + name, Vendor: vendorName(id), VendorID: id})
	}
	return nodes
}

func vendorName(id string) string {
	switch id {
	case "0x1002":
		return VendorAMD
	case "0x8086":
		return VendorIntel
	case "0x10de":
		return VendorNVIDIA
	}
	return VendorUnknown
}

func (d *Detector) cpu() CPU {
	raw, err := os.ReadFile(filepath.Join(d.Root, "proc/cpuinfo"))
	if err != nil {
		return CPU{}
	}
	return parseCPUInfo(raw)
}

// featureRE keeps what decides the speed of llama.cpp, out of hundreds of cpuinfo flags.
var featureRE = regexp.MustCompile(`^(avx.*|amx.*|sse4_[12]|ssse3|fma|f16c|bmi2|asimd.*|sve.*|i8mm)$`)

func parseCPUInfo(raw []byte) CPU {
	var c CPU
	cores := map[string]bool{}
	var physical, core string
	scan := bufio.NewScanner(bytes.NewReader(raw))
	scan.Buffer(nil, 1<<20)
	flush := func() {
		if core != "" {
			cores[physical+"/"+core] = true
		}
		physical, core = "", ""
	}
	for scan.Scan() {
		key, value, ok := strings.Cut(scan.Text(), ":")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok {
			flush()
			continue
		}
		switch key {
		case "processor":
			c.Threads++
		case "model name":
			c.Model = value
		case "physical id":
			physical = value
		case "core id":
			core = value
		case "flags", "Features":
			if c.Features == nil {
				for _, f := range strings.Fields(value) {
					if featureRE.MatchString(f) {
						c.Features = append(c.Features, f)
					}
				}
				slices.Sort(c.Features)
			}
		}
	}
	flush()
	c.Cores = len(cores)
	if c.Cores == 0 {
		c.Cores = c.Threads
	}
	return c
}

func (d *Detector) memory() Memory {
	raw, err := os.ReadFile(filepath.Join(d.Root, "proc/meminfo"))
	if err != nil {
		return Memory{}
	}
	return parseMemInfo(raw)
}

func parseMemInfo(raw []byte) Memory {
	var m Memory
	var free uint64
	for line := range strings.Lines(string(raw)) {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		kb, err := strconv.ParseUint(f[1], 10, 54)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			m.Total = kb << 10
		case "MemAvailable:":
			m.Available = kb << 10
		case "MemFree:":
			free = kb << 10
		}
	}
	if m.Available == 0 {
		m.Available = free
	}
	return m
}

func disks(paths []string) []Disk {
	out := make([]Disk, 0, len(paths))
	for _, p := range paths {
		d := Disk{Path: p}
		var st syscall.Statfs_t
		if err := syscall.Statfs(p, &st); err != nil {
			d.Error = err.Error()
		} else {
			bs := uint64(st.Bsize) //nolint:gosec // never negative
			d.Total = st.Blocks * bs
			d.Free = st.Bavail * bs
		}
		out = append(out, d)
	}
	return out
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := reaper.Start(cmd); err != nil {
		return nil, err
	}
	err := reaper.Wait(cmd)
	return out.Bytes(), err
}
