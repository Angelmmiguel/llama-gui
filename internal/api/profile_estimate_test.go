package api

import (
	"encoding/binary"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Angelmmiguel/llama-gui/internal/builds"
	"github.com/Angelmmiguel/llama-gui/internal/hardware"
	"github.com/Angelmmiguel/llama-gui/internal/llama"
	"github.com/Angelmmiguel/llama-gui/internal/profiles"
)

func TestPreviewProfile(t *testing.T) {
	h := newHarness(t)
	code, body := h.sendJSON(t, http.MethodPost, "/api/v1/profiles/preview", validProfile)
	if code != http.StatusOK || body["command"] != "llama-server -m a.gguf" || body["error"] != nil {
		t.Errorf("valid: %d %v", code, body)
	}
	code, body = h.sendJSON(t, http.MethodPost, "/api/v1/profiles/preview",
		`{"name":"x","alias":"x","model_file":"a.gguf","ctx_size":1,"extra_args":["--host 1"]}`)
	if code != http.StatusOK || body["command"] == nil || body["error"] == nil {
		t.Errorf("invalid still renders with the reason: %d %v", code, body)
	}
	h.table(t, []route{
		{"unknown field", http.MethodPost, "/api/v1/profiles/preview", `{"nope":1}`, http.StatusBadRequest, "invalid JSON"},
	})
}

// writeModel puts a file of this size in the models directory. It holds no valid
// GGUF, so estimates over it are the rule of thumb.
func writeModel(t *testing.T, dir, name string, size int64) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, name)) //nolint:gosec // a test's temp dir
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := binary.Write(f, binary.LittleEndian, uint32(0)); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
}

func estimateHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	writeModel(t, h.models, "a.gguf", 4*gib)
	total, used := uint64(32*gib), uint64(8*gib)
	vram := uint64(24 * gib)
	h.hw = hardware.Snapshot{
		GPUs:   []hardware.GPU{{MemoryTotal: &vram}},
		Memory: hardware.Memory{Total: total, Available: total - used},
	}
	return h
}

func num(t *testing.T, m map[string]any, keys ...string) int64 {
	t.Helper()
	var cur any = m
	for _, k := range keys {
		next, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("%v: %v has no %s", keys, cur, k)
		}
		cur = next[k]
	}
	f, ok := cur.(float64)
	if !ok {
		t.Fatalf("%v: %v", keys, cur)
	}
	return int64(f)
}

func TestEstimateProfileWithoutMeasurement(t *testing.T) {
	h := estimateHarness(t)
	h.router.models = []llama.Model{
		{ID: "other", Status: "loaded", Memory: llama.Memory{Effective: llama.MemoryEstimate{Devices: []llama.Device{
			{Kind: "device", Model: 3 * gib, Context: gib}, {Kind: "host", Model: gib},
		}}}},
		{ID: "idle", Status: "unloaded", Memory: llama.Memory{Status: "measured", Max: llama.MemoryEstimate{Devices: []llama.Device{
			{Kind: "device", Model: 9 * gib},
		}}}},
	}
	code, body := h.sendJSON(t, http.MethodPost, "/api/v1/profiles/estimate", validProfile)
	if code != http.StatusOK || body["source"] != sourceEstimate || body["rough"] != true || body["router_up"] != true {
		t.Fatalf("%d %v", code, body)
	}
	if got := num(t, body, "gpu", "other"); got != 4*gib {
		t.Errorf("other models on the GPU: %d", got)
	}
	if got := num(t, body, "gpu", "weights"); got != 4*gib {
		t.Errorf("weights: %d", got)
	}
	if got := num(t, body, "ram", "other"); got != 8*gib {
		t.Errorf("RAM in use: %d", got)
	}
	if body["verdict"] != verdictFits || body["verdict_on"] != "gpu" {
		t.Errorf("verdict: %v", body)
	}
}

func TestEstimateProfileOffload(t *testing.T) {
	for _, tc := range []struct {
		ngl    string
		layers int
		want   float64
	}{
		{"", 48, 1}, {"auto", 48, 1}, {"all", 48, 1}, {"-1", 48, 1}, {"99", 48, 1}, {"99", 0, 1},
		{"0", 48, 0}, {"12", 48, 0.25}, {"48", 48, 1},
	} {
		if got := offloadShare(tc.ngl, tc.layers); got != tc.want {
			t.Errorf("%q of %d: %v", tc.ngl, tc.layers, got)
		}
	}

	h := estimateHarness(t)
	_, body := h.sendJSON(t, http.MethodPost, "/api/v1/profiles/estimate",
		`{"name":"x","alias":"x","model_file":"a.gguf","ctx_size":4096,"ngl":"0"}`)
	if got := num(t, body, "gpu", "weights"); got != 0 {
		t.Errorf("-ngl 0 left %d on the GPU", got)
	}
	if got := num(t, body, "ram", "weights"); got != 4*gib {
		t.Errorf("-ngl 0 RAM weights: %d", got)
	}
}

func TestEstimateProfileMeasured(t *testing.T) {
	h := estimateHarness(t)
	h.profiles.items["p"] = profiles.Profile{ID: "p", Name: "x", Alias: "x", ModelFile: "a.gguf", CtxSize: 4096}
	h.router.models = []llama.Model{{ID: "x", Status: "loaded", Memory: llama.Memory{Effective: llama.MemoryEstimate{Devices: []llama.Device{
		{Kind: "device", Model: 3 * gib, Context: gib, Compute: gib}, {Kind: "host", Model: gib},
	}}}}}

	_, body := h.sendJSON(t, http.MethodPost, "/api/v1/profiles/estimate",
		`{"id":"p","name":"x","alias":"x","model_file":"a.gguf","ctx_size":4096}`)
	if body["source"] != sourceMeasured || body["rough"] != false {
		t.Fatalf("same settings: %v", body)
	}
	if num(t, body, "gpu", "weights") != 3*gib || num(t, body, "ram", "weights") != gib || num(t, body, "gpu", "other") != 0 {
		t.Errorf("placement: %v", body)
	}
	if got := num(t, body, "ram", "other"); got != 7*gib {
		t.Errorf("the model's own RAM stays out of other: %d", got)
	}

	_, body = h.sendJSON(t, http.MethodPost, "/api/v1/profiles/estimate",
		`{"id":"p","name":"x","alias":"x","model_file":"a.gguf","ctx_size":8192}`)
	if body["source"] != sourceScaled {
		t.Fatalf("changed context: %v", body)
	}
	if got := num(t, body, "gpu", "kv"); got <= gib {
		t.Errorf("twice the context should grow the measured KV: %d", got)
	}

	_, body = h.sendJSON(t, http.MethodPost, "/api/v1/profiles/estimate",
		`{"name":"y","alias":"y","model_file":"a.gguf","ctx_size":4096}`)
	if body["source"] != sourceEstimate {
		t.Errorf("a new profile has no measurement: %v", body)
	}
	if got := num(t, body, "gpu", "other"); got != 5*gib {
		t.Errorf("a new profile counts the loaded model as other: %d", got)
	}
}

func TestEstimateProfileCPU(t *testing.T) {
	h := estimateHarness(t)
	h.builds.list = []builds.Manifest{{ID: "b1-cpu", Tag: "b1", Backend: "cpu"}}
	h.builds.active = "b1-cpu"
	_, body := h.sendJSON(t, http.MethodPost, "/api/v1/profiles/estimate", validProfile)
	if body["gpu"] != nil || body["verdict_on"] != "ram" || num(t, body, "ram", "weights") != 4*gib {
		t.Errorf("CPU build: %v", body)
	}

	h = estimateHarness(t)
	h.hw.GPUs = nil
	h.up = false
	_, body = h.sendJSON(t, http.MethodPost, "/api/v1/profiles/estimate", validProfile)
	if body["gpu"] != nil || body["router_up"] != false {
		t.Errorf("no GPU, router down: %v", body)
	}
}

func TestEstimateProfileVerdicts(t *testing.T) {
	h := estimateHarness(t)
	writeModel(t, h.models, "big.gguf", 40*gib)
	_, body := h.sendJSON(t, http.MethodPost, "/api/v1/profiles/estimate",
		`{"name":"x","alias":"x","model_file":"big.gguf","ctx_size":4096}`)
	if body["verdict"] != verdictOver || num(t, body, "free_bytes") >= 0 {
		t.Errorf("too large: %v", body)
	}
	h.table(t, []route{
		{"no context", http.MethodPost, "/api/v1/profiles/estimate", `{"model_file":"a.gguf"}`, http.StatusBadRequest, "ctx_size"},
		{"unknown file", http.MethodPost, "/api/v1/profiles/estimate", `{"model_file":"../a.gguf","ctx_size":1}`, http.StatusNotFound, "not in the models"},
	})
}
