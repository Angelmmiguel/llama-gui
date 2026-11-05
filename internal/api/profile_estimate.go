package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Angelmmiguel/llama-gui/internal/builds"
	"github.com/Angelmmiguel/llama-gui/internal/library"
	"github.com/Angelmmiguel/llama-gui/internal/llama"
	"github.com/Angelmmiguel/llama-gui/internal/profiles"
	"github.com/Angelmmiguel/llama-gui/internal/store"
)

// Sources of a profile's memory figures.
const (
	sourceMeasured = "measured" // the router's own numbers for exactly these settings
	sourceScaled   = "scaled"   // those numbers, adjusted by how the estimate changes
	sourceEstimate = "estimate" // library.Estimate alone
)

// Verdicts of a profile's memory figures.
const (
	verdictFits  = "fits"
	verdictTight = "tight"
	verdictOver  = "over"
)

// memoryUse is one kind of memory as the profile would use it. Other is what stays
// in use without this model: other loaded models for VRAM, everything else for RAM.
type memoryUse struct {
	Capacity int64 `json:"capacity"`
	Other    int64 `json:"other"`
	Weights  int64 `json:"weights"`
	KV       int64 `json:"kv"`
	Compute  int64 `json:"compute"`
}

func (m memoryUse) own() int64 { return m.Weights + m.KV + m.Compute }

func (m memoryUse) free() int64 { return m.Capacity - m.Other - m.own() }

type profileEstimate struct {
	Source string `json:"source"`
	// Rough says the file's metadata was incomplete, so KV is a rule of thumb.
	Rough bool `json:"rough"`
	// Layers is the model's layer count, 0 when unknown.
	Layers int `json:"layers"`
	// RouterUp false means other loaded models are unknown rather than none.
	RouterUp bool `json:"router_up"`
	// GPU is nil when this model would run on the CPU: no GPU memory, or a CPU build.
	GPU *memoryUse `json:"gpu"`
	RAM memoryUse  `json:"ram"`
	// Verdict is the tighter of the two memories, named by VerdictOn: gpu or ram.
	Verdict   string `json:"verdict"`
	VerdictOn string `json:"verdict_on"`
	FreeBytes int64  `json:"free_bytes"`
}

// previewProfile renders unsaved form values, so the page's command comes from the
// same code as the saved one. A profile that does not validate still renders: the
// reason comes alongside.
func (a *api) previewProfile(w http.ResponseWriter, r *http.Request) {
	var p profiles.Profile
	if !a.decodeJSON(w, r, &p, false) {
		return
	}
	out := map[string]string{"command": a.Profiles.Command(p)}
	if err := p.Validate(); err != nil {
		out["error"] = err.Error()
	}
	WriteJSON(w, http.StatusOK, out)
}

func (a *api) estimateProfile(w http.ResponseWriter, r *http.Request) {
	var p profiles.Profile
	if !a.decodeJSON(w, r, &p, false) {
		return
	}
	if p.CtxSize <= 0 {
		badRequest(w, "ctx_size: required and positive")
		return
	}
	// Going through the scan means the path is only ever compared, never opened.
	models, err := library.Scan(a.ModelsDir)
	if err != nil {
		a.failLibrary(w, err)
		return
	}
	i := slices.IndexFunc(models, func(m library.LocalModel) bool { return ownsFile(m, p.ModelFile) })
	if i < 0 {
		WriteError(w, http.StatusNotFound, p.ModelFile+" is not in the models directory")
		return
	}
	WriteJSON(w, http.StatusOK, a.estimate(r.Context(), p, models[i]))
}

func ownsFile(m library.LocalModel, file string) bool {
	return file == m.Path || strings.HasPrefix(file, m.Path+"/")
}

func (a *api) estimate(ctx context.Context, p profiles.Profile, file library.LocalModel) profileEstimate {
	snap := a.Hardware(ctx)
	hw := a.machine(ctx)
	var meta library.Metadata
	if file.Metadata != nil {
		meta = *file.Metadata
	}
	predict := func(p profiles.Profile) library.Fit {
		slots := max(p.Parallel, 1)
		return library.Estimate(meta, file.SizeBytes, max(p.CtxSize/slots, 1), cacheType(p.CacheTypeK), cacheType(p.CacheTypeV), slots, hw)
	}
	fit := predict(p)
	out := profileEstimate{Source: sourceEstimate, Rough: fit.Estimated, Layers: meta.BlockCount}

	saved, alias := a.savedProfile(ctx, p)
	routerModels, up := a.routerModels(ctx)
	out.RouterUp = up
	self := func(m llama.Model) bool { return m.ID == p.Alias || m.ID == alias }

	gpuAvailable := hw.VRAMBytes > 0 && !a.activeOnCPU(ctx)
	if gpuAvailable {
		out.GPU = &memoryUse{Capacity: hw.VRAMBytes}
	}
	out.RAM.Capacity = hw.RAMBytes
	if inUse := int64(snap.Memory.Total) - int64(snap.Memory.Available); inUse > 0 { //nolint:gosec // memory sizes fit int64
		out.RAM.Other = inUse
	}
	for _, m := range routerModels {
		if m.Status != "loaded" {
			continue
		}
		for _, d := range m.Memory.Effective.Devices {
			bytes := d.Model + d.Context + d.Compute
			switch {
			case self(m) && d.Kind != "device":
				// RAM.Other came from the machine, so this model's share has to leave it.
				out.RAM.Other = max(out.RAM.Other-bytes, 0)
			case !self(m) && d.Kind == "device" && out.GPU != nil:
				out.GPU.Other += bytes
			}
		}
	}

	devices := measuredDevices(routerModels, self)
	sameFile := saved != nil && saved.ModelFile == p.ModelFile
	switch {
	case len(devices) > 0 && sameFile && sameMemorySettings(*saved, p):
		out.Source, out.Rough = sourceMeasured, false
		out.place(devices)
	case len(devices) > 0 && sameFile:
		before := predict(*saved)
		var measured library.Measured
		for _, d := range devices {
			measured.ModelBytes += d.Model
			measured.ContextBytes += d.Context
			measured.ComputeBytes += d.Compute
		}
		out.Source, out.Rough = sourceScaled, false
		out.split(measured.ModelBytes, scale(measured.ContextBytes, fit.KVBytes, before.KVBytes),
			scale(measured.ComputeBytes, fit.ComputeBytes, before.ComputeBytes), p.NGL, meta.BlockCount)
	default:
		out.split(fit.WeightsBytes, fit.KVBytes, fit.ComputeBytes, p.NGL, meta.BlockCount)
	}
	out.judge(hw.HeadroomBytes)
	return out
}

// cacheType is what upstream uses when a profile leaves the KV type unset.
func cacheType(t string) string {
	if t == "" {
		return "f16"
	}
	return t
}

// savedProfile returns the stored version of a form, with the alias the router knows it by.
func (a *api) savedProfile(ctx context.Context, p profiles.Profile) (*profiles.Profile, string) {
	if p.ID == "" {
		return nil, p.Alias
	}
	saved, err := a.Profiles.Get(ctx, p.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			a.log.Warn("reading a profile to estimate", "error", err)
		}
		return nil, p.Alias
	}
	return &saved, saved.Alias
}

func (a *api) routerModels(ctx context.Context) ([]llama.Model, bool) {
	router, ctx, cancel, err := a.router(ctx)
	if err != nil {
		return nil, false
	}
	defer cancel()
	models, err := router.Models(ctx)
	if err != nil {
		a.log.Warn("listing router models to estimate", "error", err)
		return nil, false
	}
	return models, true
}

func (a *api) activeOnCPU(ctx context.Context) bool {
	id, err := a.Builds.Active(ctx)
	if err != nil || id == "" {
		return false
	}
	man, err := a.Builds.Get(id)
	return err == nil && man.Backend == builds.BackendCPU
}

// measuredDevices is the router's reading for this model: what it holds while
// loaded, otherwise what it remembers from the last load.
func measuredDevices(models []llama.Model, self func(llama.Model) bool) []llama.Device {
	i := slices.IndexFunc(models, self)
	if i < 0 {
		return nil
	}
	m := models[i].Memory
	if len(m.Effective.Devices) > 0 {
		return m.Effective.Devices
	}
	if m.Status == "measured" {
		return m.Max.Devices
	}
	return nil
}

// sameMemorySettings says the router's measurement still describes these settings.
func sameMemorySettings(a, b profiles.Profile) bool {
	return a.MMProj == b.MMProj && a.CtxSize == b.CtxSize && a.Parallel == b.Parallel && a.NGL == b.NGL &&
		a.SplitMode == b.SplitMode && a.TensorSplit == b.TensorSplit && a.FlashAttn == b.FlashAttn &&
		a.CacheTypeK == b.CacheTypeK && a.CacheTypeV == b.CacheTypeV && a.Batch == b.Batch && a.UBatch == b.UBatch
}

func scale(measured, now, before int64) int64 {
	if before <= 0 {
		return measured
	}
	return int64(float64(measured) * float64(now) / float64(before))
}

// place assigns the router's reading device by device.
func (e *profileEstimate) place(devices []llama.Device) {
	for _, d := range devices {
		to := &e.RAM
		if d.Kind == "device" && e.GPU != nil {
			to = e.GPU
		}
		to.Weights += d.Model
		to.KV += d.Context
		to.Compute += d.Compute
	}
}

// split assigns predicted figures: weights by the share of layers offloaded, and
// the KV cache and compute buffers wherever any layer runs.
func (e *profileEstimate) split(weights, kv, compute int64, ngl string, layers int) {
	share := offloadShare(ngl, layers)
	if e.GPU == nil || share == 0 {
		e.RAM.Weights, e.RAM.KV, e.RAM.Compute = weights, kv, compute
		return
	}
	e.GPU.Weights = int64(float64(weights) * share)
	e.GPU.KV, e.GPU.Compute = kv, compute
	e.RAM.Weights = weights - e.GPU.Weights
}

// offloadShare is the part of the layers an -ngl value puts on the GPU. Without a
// layer count any positive number reads as all of them, as with the usual -ngl 99.
func offloadShare(ngl string, layers int) float64 {
	n, err := strconv.Atoi(ngl)
	switch {
	case err != nil, n < 0:
		return 1
	case n == 0:
		return 0
	case layers <= 0, n >= layers:
		return 1
	}
	return float64(n) / float64(layers)
}

func (e *profileEstimate) judge(headroom int64) {
	rank := func(m memoryUse) int {
		switch {
		case m.free() < 0:
			return 2
		case m.free() < headroom:
			return 1
		}
		return 0
	}
	worst, on := e.RAM, "ram"
	if e.GPU != nil && rank(*e.GPU) >= rank(e.RAM) {
		worst, on = *e.GPU, "gpu"
	}
	e.Verdict, e.VerdictOn, e.FreeBytes = []string{verdictFits, verdictTight, verdictOver}[rank(worst)], on, worst.free()
}
