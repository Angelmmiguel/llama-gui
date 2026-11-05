package builds

import (
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/Angelmmiguel/llama-gui/internal/hardware"
	"github.com/Angelmmiguel/llama-gui/internal/releases"
)

// Backends of llama.cpp prebuilts.
const (
	BackendCPU      = hardware.BackendCPU
	BackendVulkan   = hardware.BackendVulkan
	BackendCUDA     = hardware.BackendCUDA
	BackendROCm     = "rocm"
	BackendSYCLFP16 = "sycl-fp16"
	BackendSYCLFP32 = "sycl-fp32"
	BackendOpenVINO = "openvino"
)

// Architectures as named in release assets.
const (
	ArchX64   = "x64"
	ArchARM64 = "arm64"
)

var (
	// ErrInvalidBackend means the backend is not one of Backends.
	ErrInvalidBackend = errors.New("invalid backend")
	// ErrNoAsset means the release has nothing usable for the backend and architecture.
	ErrNoAsset = errors.New("no matching release asset")
)

var backends = []string{BackendCPU, BackendVulkan, BackendCUDA, BackendROCm, BackendSYCLFP16, BackendSYCLFP32, BackendOpenVINO}

// Backends lists the known backends.
func Backends() []string { return slices.Clone(backends) }

// ValidateBackend accepts only known backends, which makes them safe in a path.
func ValidateBackend(backend string) error {
	if !slices.Contains(backends, backend) {
		return fmt.Errorf("%w: %q", ErrInvalidBackend, backend)
	}
	return nil
}

// HostArch names the architecture of this binary the way release assets do, or returns "" when
// no prebuilts exist for it.
func HostArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return ArchX64
	case "arm64":
		return ArchARM64
	}
	return ""
}

// GPUInfo is what asset selection needs to know about the host.
type GPUInfo struct {
	// CUDAVersion is the driver's CUDA version, as in the nvidia-smi header.
	CUDAVersion string
}

// Selection is what to download for a build.
type Selection struct {
	Main releases.Asset
	// CUDART holds the CUDA runtime libraries, nil when not needed or not offered.
	CUDART *releases.Asset
	// Variant is the toolkit version in the asset name, such as "13.4".
	Variant string
}

type candidate struct {
	asset   releases.Asset
	version string
}

// candidates finds the Linux prebuilts of a release, by backend. Everything else in the release
// (s390x, snapdragon, macOS, Windows, the web UI) does not match the pattern.
func candidates(rel releases.Release, arch string) (map[string][]candidate, error) {
	tag := rel.BinaryTag
	if tag == "" {
		tag = rel.Tag
	}
	if err := releases.ValidateTag(tag); err != nil {
		return nil, err
	}
	if arch != ArchX64 && arch != ArchARM64 {
		return nil, fmt.Errorf("%w: unsupported architecture %q", ErrNoAsset, arch)
	}
	re := regexp.MustCompile(`^llama-` + regexp.QuoteMeta(tag) + `-bin-ubuntu-(?:(vulkan|sycl-fp16|sycl-fp32|cuda|rocm|openvino)(?:-(\d+(?:\.\d+)*))?-)?` + arch + `\.tar\.gz$`)
	found := map[string][]candidate{}
	for _, a := range rel.Assets {
		m := re.FindStringSubmatch(a.Name)
		if m == nil {
			continue
		}
		backend := m[1]
		if backend == "" {
			backend = BackendCPU
		}
		found[backend] = append(found[backend], candidate{a, m[2]})
	}
	for _, list := range found {
		slices.SortStableFunc(list, func(a, b candidate) int { return compareVersions(b.version, a.version) })
	}
	return found, nil
}

// SelectAssets chooses the assets of a backend, newest toolkit first. CUDA takes the highest
// major the driver supports: within a major, CUDA's minor version compatibility lets a build run
// on an older driver.
func SelectAssets(rel releases.Release, backend, arch string, gpu GPUInfo) (Selection, error) {
	if err := ValidateBackend(backend); err != nil {
		return Selection{}, err
	}
	found, err := candidates(rel, arch)
	if err != nil {
		return Selection{}, err
	}
	list := found[backend]
	if len(list) == 0 {
		return Selection{}, fmt.Errorf("%w: %s has no %s build for %s", ErrNoAsset, rel.Tag, backend, arch)
	}
	pick := list[0]
	if backend == BackendCUDA {
		if pick, err = pickCUDA(list, arch, gpu); err != nil {
			return Selection{}, err
		}
	}
	sel := Selection{Main: pick.asset, Variant: pick.version}
	if backend == BackendCUDA {
		name := "cudart-" + strings.TrimSuffix(pick.asset.Name, archiveSuffix) + archiveSuffix
		if i := slices.IndexFunc(rel.Assets, func(a releases.Asset) bool { return a.Name == name }); i >= 0 {
			sel.CUDART = &rel.Assets[i]
		}
	}
	return sel, nil
}

func pickCUDA(list []candidate, arch string, gpu GPUInfo) (candidate, error) {
	offered := make([]string, len(list))
	for i, c := range list {
		offered[i] = c.version
	}
	driver := versionParts(gpu.CUDAVersion)
	if len(driver) == 0 {
		return candidate{}, fmt.Errorf("%w: no NVIDIA driver detected to choose among CUDA %s for %s", ErrNoAsset, strings.Join(offered, ", "), arch)
	}
	// list is newest first, so the first of the major is the highest minor.
	for _, c := range list {
		if parts := versionParts(c.version); len(parts) > 0 && parts[0] <= driver[0] {
			return c, nil
		}
	}
	return candidate{}, fmt.Errorf("%w: driver supports CUDA %s, release offers %s for %s", ErrNoAsset, gpu.CUDAVersion, strings.Join(offered, ", "), arch)
}

func versionParts(v string) []int {
	if v == "" {
		return nil
	}
	fields := strings.Split(v, ".")
	parts := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil
		}
		parts = append(parts, n)
	}
	return parts
}

func compareVersions(a, b string) int { return slices.Compare(versionParts(a), versionParts(b)) }

// Option is a backend a release can be installed with.
type Option struct {
	Backend string `json:"backend"`
	Asset   string `json:"asset"`
	// Size is the download in bytes, including the CUDA runtime.
	Size    int64  `json:"size"`
	Variant string `json:"variant,omitempty"`
	// Reason says why the hardware cannot use it, empty if it can.
	Reason string `json:"reason,omitempty"`
}

func availableOptions(rel releases.Release, arch string, snap hardware.Snapshot) ([]Option, error) {
	found, err := candidates(rel, arch)
	if err != nil {
		return nil, err
	}
	gpu := GPUInfo{CUDAVersion: snap.CUDAVersion}
	options := []Option{}
	for _, backend := range backends {
		if len(found[backend]) == 0 {
			continue
		}
		reason := hardwareGap(backend, snap)
		sel, err := SelectAssets(rel, backend, arch, gpu)
		switch {
		case errors.Is(err, ErrNoAsset):
			// Show the newest even if unusable, as the UI lists it with the reason.
			if reason == "" {
				reason = err.Error()
			}
			c := found[backend][0]
			sel = Selection{Main: c.asset, Variant: c.version}
		case err != nil:
			return nil, err
		}
		o := Option{Backend: backend, Asset: sel.Main.Name, Size: sel.Main.Size, Variant: sel.Variant, Reason: reason}
		if sel.CUDART != nil {
			o.Size += sel.CUDART.Size
		}
		options = append(options, o)
	}
	return options, nil
}

func hardwareGap(backend string, snap hardware.Snapshot) string {
	hasVendor := func(vendor string) bool {
		return slices.ContainsFunc(snap.RenderNodes, func(n hardware.RenderNode) bool { return n.Vendor == vendor })
	}
	switch backend {
	case BackendCUDA:
		if len(snap.GPUs) == 0 {
			return "no NVIDIA GPU detected"
		}
	case BackendVulkan:
		if len(snap.RenderNodes) == 0 {
			return "no render node in /dev/dri"
		}
	case BackendROCm:
		if !hasVendor(hardware.VendorAMD) {
			return "no AMD GPU detected"
		}
	case BackendSYCLFP16, BackendSYCLFP32, BackendOpenVINO:
		if !hasVendor(hardware.VendorIntel) {
			return "no Intel GPU detected"
		}
	}
	return ""
}
