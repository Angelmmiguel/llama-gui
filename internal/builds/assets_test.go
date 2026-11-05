package builds

import (
	"errors"
	"strings"
	"testing"

	"github.com/Angelmmiguel/llama-gui/internal/hardware"
	"github.com/Angelmmiguel/llama-gui/internal/releases"
)

// realAssetNames is the asset list of the real b12137 release.
var realAssetNames = strings.Fields(`
cudart-llama-b12137-bin-ubuntu-cuda-12.8-x64.tar.gz
cudart-llama-b12137-bin-ubuntu-cuda-13.4-arm64.tar.gz
cudart-llama-b12137-bin-ubuntu-cuda-13.4-x64.tar.gz
cudart-llama-bin-win-cuda-12.4-x64.zip
llama-b12137-bin-android-arm64-snapdragon.tar.gz
llama-b12137-bin-android-arm64.tar.gz
llama-b12137-bin-linux-arm64-snapdragon.tar.gz
llama-b12137-bin-macos-arm64.tar.gz
llama-b12137-bin-ubuntu-26.04-s390x.tar.gz
llama-b12137-bin-ubuntu-arm64.tar.gz
llama-b12137-bin-ubuntu-cuda-12.8-x64.tar.gz
llama-b12137-bin-ubuntu-cuda-13.4-arm64.tar.gz
llama-b12137-bin-ubuntu-cuda-13.4-x64.tar.gz
llama-b12137-bin-ubuntu-openvino-2026.4.1-x64.tar.gz
llama-b12137-bin-ubuntu-rocm-10.0-x64.tar.gz
llama-b12137-bin-ubuntu-s390x.tar.gz
llama-b12137-bin-ubuntu-sycl-fp16-x64.tar.gz
llama-b12137-bin-ubuntu-sycl-fp32-x64.tar.gz
llama-b12137-bin-ubuntu-vulkan-arm64.tar.gz
llama-b12137-bin-ubuntu-vulkan-x64.tar.gz
llama-b12137-bin-ubuntu-x64.tar.gz
llama-b12137-bin-ubuntu-zdnn-s390x.tar.gz
llama-b12137-bin-win-cpu-x64.zip
llama-b12137-ui.tar.gz
`)

func realRelease() releases.Release {
	rel := releases.Release{Tag: "b12137", BinaryTag: "b12137"}
	for _, name := range realAssetNames {
		rel.Assets = append(rel.Assets, releases.Asset{Name: name, Size: 10})
	}
	return rel
}

func TestSelectAssets(t *testing.T) {
	const prefix = "llama-b12137-bin-ubuntu-"
	for _, tc := range []struct {
		backend, arch, cuda, want, cudart string
	}{
		{BackendCPU, ArchX64, "", prefix + "x64.tar.gz", ""},
		{BackendCPU, ArchARM64, "", prefix + "arm64.tar.gz", ""},
		{BackendVulkan, ArchX64, "", prefix + "vulkan-x64.tar.gz", ""},
		{BackendVulkan, ArchARM64, "", prefix + "vulkan-arm64.tar.gz", ""},
		{BackendROCm, ArchX64, "", prefix + "rocm-10.0-x64.tar.gz", ""},
		{BackendSYCLFP16, ArchX64, "", prefix + "sycl-fp16-x64.tar.gz", ""},
		{BackendSYCLFP32, ArchX64, "", prefix + "sycl-fp32-x64.tar.gz", ""},
		{BackendOpenVINO, ArchX64, "", prefix + "openvino-2026.4.1-x64.tar.gz", ""},
		{BackendCUDA, ArchX64, "14.1", prefix + "cuda-13.4-x64.tar.gz", "cudart-llama-b12137-bin-ubuntu-cuda-13.4-x64.tar.gz"},
		{BackendCUDA, ArchX64, "13.4", prefix + "cuda-13.4-x64.tar.gz", "cudart-llama-b12137-bin-ubuntu-cuda-13.4-x64.tar.gz"},
		{BackendCUDA, ArchX64, "13.0", prefix + "cuda-13.4-x64.tar.gz", "cudart-llama-b12137-bin-ubuntu-cuda-13.4-x64.tar.gz"},
		{BackendCUDA, ArchX64, "12.4", prefix + "cuda-12.8-x64.tar.gz", "cudart-llama-b12137-bin-ubuntu-cuda-12.8-x64.tar.gz"},
		{BackendCUDA, ArchARM64, "13.4", prefix + "cuda-13.4-arm64.tar.gz", "cudart-llama-b12137-bin-ubuntu-cuda-13.4-arm64.tar.gz"},
	} {
		sel, err := SelectAssets(realRelease(), tc.backend, tc.arch, GPUInfo{CUDAVersion: tc.cuda})
		if err != nil {
			t.Errorf("%s/%s/%s: %v", tc.backend, tc.arch, tc.cuda, err)
			continue
		}
		if sel.Main.Name != tc.want {
			t.Errorf("%s/%s/%s: main = %s", tc.backend, tc.arch, tc.cuda, sel.Main.Name)
		}
		got := ""
		if sel.CUDART != nil {
			got = sel.CUDART.Name
		}
		if got != tc.cudart {
			t.Errorf("%s/%s/%s: cudart = %s", tc.backend, tc.arch, tc.cuda, got)
		}
	}
}

func TestSelectAssetsErrors(t *testing.T) {
	for _, tc := range []struct {
		backend, arch, cuda string
		want                error
	}{
		{BackendCUDA, ArchX64, "11.8", ErrNoAsset},
		{BackendCUDA, ArchARM64, "12.4", ErrNoAsset},
		{BackendCUDA, ArchX64, "", ErrNoAsset},
		{BackendCUDA, ArchX64, "garbage", ErrNoAsset},
		{BackendROCm, ArchARM64, "", ErrNoAsset},
		{BackendCPU, "s390x", "", ErrNoAsset},
		{"zdnn", ArchX64, "", ErrInvalidBackend},
		{"../x", ArchX64, "", ErrInvalidBackend},
	} {
		if _, err := SelectAssets(realRelease(), tc.backend, tc.arch, GPUInfo{CUDAVersion: tc.cuda}); !errors.Is(err, tc.want) {
			t.Errorf("%s/%s/%s: %v", tc.backend, tc.arch, tc.cuda, err)
		}
	}
	bad := releases.Release{Tag: "../x", BinaryTag: "../x"}
	if _, err := SelectAssets(bad, BackendCPU, ArchX64, GPUInfo{}); !errors.Is(err, releases.ErrInvalidTag) {
		t.Errorf("bad tag: %v", err)
	}
}

func TestSelectAssetsErrorIsClear(t *testing.T) {
	_, err := SelectAssets(realRelease(), BackendCUDA, ArchX64, GPUInfo{CUDAVersion: "11.8"})
	for _, want := range []string{"11.8", "12.8", "13.4"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, missing %s", err, want)
		}
	}
}

func TestSelectPicksNewestOfSeveral(t *testing.T) {
	rel := releases.Release{Tag: "b1", BinaryTag: "b1", Assets: []releases.Asset{
		{Name: "llama-b1-bin-ubuntu-rocm-9.10-x64.tar.gz"},
		{Name: "llama-b1-bin-ubuntu-rocm-10.0-x64.tar.gz"},
		{Name: "llama-b1-bin-ubuntu-rocm-9.2-x64.tar.gz"},
		{Name: "llama-b1-bin-ubuntu-cuda-12.4-x64.tar.gz"},
		{Name: "llama-b1-bin-ubuntu-cuda-12.8-x64.tar.gz"},
	}}
	if sel, err := SelectAssets(rel, BackendROCm, ArchX64, GPUInfo{}); err != nil || sel.Variant != "10.0" {
		t.Errorf("rocm = %+v, %v", sel, err)
	}
	if sel, err := SelectAssets(rel, BackendCUDA, ArchX64, GPUInfo{CUDAVersion: "12.2"}); err != nil || sel.Variant != "12.8" || sel.CUDART != nil {
		t.Errorf("cuda = %+v, %v", sel, err)
	}
}

func TestAvailable(t *testing.T) {
	options := func(snap hardware.Snapshot, arch string) map[string]Option {
		t.Helper()
		list, err := availableOptions(realRelease(), arch, snap)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]Option{}
		for _, o := range list {
			out[o.Backend] = o
		}
		return out
	}

	bare := options(hardware.Snapshot{}, ArchX64)
	if len(bare) != len(backends) {
		t.Errorf("options = %+v", bare)
	}
	if bare[BackendCPU].Reason != "" {
		t.Errorf("cpu = %+v", bare[BackendCPU])
	}
	for _, b := range []string{BackendCUDA, BackendVulkan, BackendROCm, BackendSYCLFP16, BackendOpenVINO} {
		if bare[b].Reason == "" {
			t.Errorf("%s has no reason on a bare host", b)
		}
	}

	nvidia := options(hardware.Snapshot{
		GPUs: []hardware.GPU{{Name: "x"}}, CUDAVersion: "12.4",
		RenderNodes: []hardware.RenderNode{{Vendor: hardware.VendorNVIDIA}},
	}, ArchX64)
	if c := nvidia[BackendCUDA]; c.Reason != "" || c.Variant != "12.8" || c.Size != 20 {
		t.Errorf("cuda = %+v", c)
	}
	if nvidia[BackendVulkan].Reason != "" || nvidia[BackendROCm].Reason == "" {
		t.Errorf("options = %+v", nvidia)
	}

	old := options(hardware.Snapshot{GPUs: []hardware.GPU{{Name: "x"}}, CUDAVersion: "11.8"}, ArchX64)
	if !strings.Contains(old[BackendCUDA].Reason, "11.8") {
		t.Errorf("cuda = %+v", old[BackendCUDA])
	}

	arm := options(hardware.Snapshot{}, ArchARM64)
	if _, ok := arm[BackendROCm]; ok || len(arm) != 3 {
		t.Errorf("arm = %+v", arm)
	}
}
