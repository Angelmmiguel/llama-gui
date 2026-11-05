package library

import "strings"

// Verdicts of a memory estimate.
const (
	VerdictFitsGPU        = "fits_gpu"
	VerdictPartialOffload = "partial_offload"
	VerdictOffloadExperts = "offload_experts"
	VerdictFitsRAM        = "fits_ram" // host-only: no GPU memory, so the verdict is about RAM
	VerdictTooLarge       = "too_large"
)

const (
	computeBase     = 800_000_000 // compute buffer: 0.8 GB plus a context-linked term, as in the design
	computePerCtx   = 700_000_000.0 / 65536
	kvCellPad       = 256 // llama.cpp pads the KV cache to a multiple of this many cells
	fallbackKVPerGB = 16 << 10
)

// Hardware is the memory available to models. VRAMBytes is the sum over GPUs, zero for host-only.
// HeadroomBytes is kept free on each device for the desktop, driver and other processes.
type Hardware struct {
	VRAMBytes     int64 `json:"vram_bytes"`
	RAMBytes      int64 `json:"ram_bytes"`
	HeadroomBytes int64 `json:"headroom_bytes"`
}

// Fit is the memory estimate of one model file at a context and cache type.
type Fit struct {
	WeightsBytes   int64  `json:"weights_bytes"`
	KVBytes        int64  `json:"kv_bytes"`
	ComputeBytes   int64  `json:"compute_bytes"`
	TotalBytes     int64  `json:"total_bytes"`
	Verdict        string `json:"verdict"`
	OffloadPercent int    `json:"offload_percent"` // share of the total that spills to RAM
	MoE            bool   `json:"moe"`
	Estimated      bool   `json:"estimated"`  // KV came from a rule of thumb: metadata was incomplete
	Calibrated     bool   `json:"calibrated"` // numbers are the router's measurements
}

// cacheElemSize is the bytes per KV element of a cache type: f16 is 2, and the quantised types are
// block sizes (q8_0 is 34 bytes per 32 values).
func cacheElemSize(t string) float64 {
	switch strings.ToLower(t) {
	case "f32":
		return 4
	case "q8_0":
		return 34.0 / 32
	case "q5_1":
		return 24.0 / 32
	case "q5_0":
		return 22.0 / 32
	case "q4_1":
		return 20.0 / 32
	case "q4_0", "iq4_nl":
		return 18.0 / 32
	}
	return 2 // f16, bf16 and anything unknown
}

// Estimate predicts the memory a model needs. ctx is the context per slot and parallel the slot
// count, because llama-server's -c is the total and splits it across slots (pass -c / -np).
//
// KV is cells x layers x (K head dim + V head dim) x KV heads x element size, which is what
// llama.cpp allocates for an unquantised-attention model. It overestimates models with sliding-window
// layers (Gemma 3) and with per-layer KV head counts (the largest is used), and ignores padding
// beyond 256 cells; Calibrate replaces it once the router has measured the model. Weights are the
// file size, and the compute buffer is a flat 0.8 GB plus 0.7 GB per 64k cells.
func Estimate(meta Metadata, fileBytes int64, ctx int, cacheTypeK, cacheTypeV string, parallel int, hw Hardware) Fit {
	cells := int64(max(ctx, 0)) * int64(max(parallel, 1))
	cells = (cells + kvCellPad - 1) / kvCellPad * kvCellPad

	f := Fit{WeightsBytes: fileBytes, MoE: meta.IsMoE || meta.ExpertCount > 1}
	nKV := meta.HeadCountKV
	if nKV == 0 {
		nKV = meta.HeadCount // no GQA: llama.cpp defaults n_head_kv to n_head
	}
	kHead := meta.KeyLength
	if kHead == 0 && meta.HeadCount > 0 {
		kHead = meta.EmbeddingLength / meta.HeadCount
	}
	vHead := meta.ValueLength
	if vHead == 0 {
		vHead = kHead
	}
	if meta.BlockCount > 0 && nKV > 0 && kHead > 0 {
		perCell := float64(meta.BlockCount) * float64(nKV) *
			(float64(kHead)*cacheElemSize(cacheTypeK) + float64(vHead)*cacheElemSize(cacheTypeV))
		f.KVBytes = int64(perCell * float64(cells))
	} else {
		perCell := float64(fileBytes) / 1e9 * fallbackKVPerGB * (cacheElemSize(cacheTypeK) + cacheElemSize(cacheTypeV)) / 4
		f.KVBytes = int64(perCell * float64(cells))
		f.Estimated = true
	}
	f.ComputeBytes = computeBase + int64(computePerCtx*float64(cells))
	f.finish(hw)
	return f
}

// finish sums the total and picks the verdict.
func (f *Fit) finish(hw Hardware) {
	f.TotalBytes = f.WeightsBytes + f.KVBytes + f.ComputeBytes
	f.OffloadPercent = 0
	ram := max(hw.RAMBytes-hw.HeadroomBytes, 0)
	if hw.VRAMBytes <= 0 {
		f.Verdict = VerdictTooLarge
		if f.TotalBytes <= ram {
			f.Verdict = VerdictFitsRAM
		}
		return
	}
	vram := max(hw.VRAMBytes-hw.HeadroomBytes, 0)
	switch {
	case f.TotalBytes <= vram:
		f.Verdict = VerdictFitsGPU
	case f.TotalBytes <= vram+ram:
		f.OffloadPercent = int((f.TotalBytes - vram) * 100 / f.TotalBytes) // spill share, rounded down
		f.Verdict = VerdictPartialOffload
		// Offloading experts moves only weights, so the KV cache and compute buffer must fit alone.
		if f.MoE && f.KVBytes+f.ComputeBytes <= vram {
			f.Verdict = VerdictOffloadExperts
		}
	default:
		f.Verdict = VerdictTooLarge
	}
}

// Measured is what the router reports for a loaded model (memory.effective, summed over devices).
type Measured struct {
	ModelBytes   int64
	ContextBytes int64
	ComputeBytes int64
}

// Calibrate replaces the estimate's numbers with the router's measurements and re-derives the
// verdict. An all-zero measurement (model not loaded) leaves the estimate untouched.
func Calibrate(f Fit, m Measured, hw Hardware) Fit {
	if m.ModelBytes <= 0 && m.ContextBytes <= 0 && m.ComputeBytes <= 0 {
		return f
	}
	f.WeightsBytes, f.KVBytes, f.ComputeBytes = m.ModelBytes, m.ContextBytes, m.ComputeBytes
	f.Estimated, f.Calibrated = false, true
	f.finish(hw)
	return f
}
