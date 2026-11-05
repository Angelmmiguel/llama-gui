package library

import (
	"math"
	"testing"
)

const gb = 1_000_000_000

func TestEstimateKV(t *testing.T) {
	// Llama-3.1-8B: 32 layers, 8 KV heads, head dim 128. f16 KV is 128 KiB per token.
	meta := Metadata{BlockCount: 32, HeadCount: 32, HeadCountKV: 8, EmbeddingLength: 4096}
	hw := Hardware{VRAMBytes: 24 * gb, RAMBytes: 32 * gb}
	f := Estimate(meta, 5*gb, 8192, "f16", "f16", 1, hw)
	if f.KVBytes != 8192*131072 || f.Estimated {
		t.Errorf("f16 kv %d estimated=%v", f.KVBytes, f.Estimated)
	}
	if want := int64(800_000_000 + 700_000_000.0*8192/65536); f.ComputeBytes != want {
		t.Errorf("compute %d want %d", f.ComputeBytes, want)
	}
	q := Estimate(meta, 5*gb, 8192, "q8_0", "q8_0", 1, hw)
	if r := float64(q.KVBytes) / float64(f.KVBytes); math.Abs(r-1.0625/2) > 1e-6 {
		t.Errorf("q8_0 ratio %v", r)
	}
	if p := Estimate(meta, 5*gb, 8192, "f16", "f16", 4, hw); p.KVBytes != 4*f.KVBytes {
		t.Errorf("parallel kv %d", p.KVBytes)
	}
	if f.TotalBytes != f.WeightsBytes+f.KVBytes+f.ComputeBytes || f.Verdict != VerdictFitsGPU {
		t.Errorf("%+v", f)
	}
}

func TestEstimateFallback(t *testing.T) {
	f := Estimate(Metadata{}, 10*gb, 8192, "", "", 1, Hardware{VRAMBytes: 24 * gb})
	if !f.Estimated || f.KVBytes <= 0 {
		t.Errorf("%+v", f)
	}
}

func TestVerdicts(t *testing.T) {
	dense := Metadata{BlockCount: 32, HeadCount: 32, HeadCountKV: 8, KeyLength: 128}
	moe := dense
	moe.ExpertCount, moe.IsMoE = 64, true
	hw := Hardware{VRAMBytes: 24 * gb, RAMBytes: 32 * gb, HeadroomBytes: gb}
	cases := []struct {
		name    string
		meta    Metadata
		file    int64
		hw      Hardware
		verdict string
		pct     bool
	}{
		{"fits", dense, 10 * gb, hw, VerdictFitsGPU, false},
		{"partial", dense, 30 * gb, hw, VerdictPartialOffload, true},
		{"experts", moe, 30 * gb, hw, VerdictOffloadExperts, true},
		{"too large", dense, 80 * gb, hw, VerdictTooLarge, false},
		{"host fits", dense, 10 * gb, Hardware{RAMBytes: 32 * gb}, VerdictFitsRAM, false},
		{"host too large", dense, 40 * gb, Hardware{RAMBytes: 32 * gb}, VerdictTooLarge, false},
		{"headroom matters", dense, 22500 * 1e6, hw, VerdictPartialOffload, true},
	}
	for _, c := range cases {
		f := Estimate(c.meta, c.file, 8192, "f16", "f16", 1, c.hw)
		if f.Verdict != c.verdict || (f.OffloadPercent > 0) != c.pct {
			t.Errorf("%s: %s (%d%%), want %s; %+v", c.name, f.Verdict, f.OffloadPercent, c.verdict, f)
		}
	}
	// A KV cache larger than VRAM cannot be saved by moving experts.
	f := Estimate(moe, 30*gb, 8192*32, "f16", "f16", 1, hw)
	if f.Verdict == VerdictOffloadExperts {
		t.Errorf("experts verdict with huge KV: %+v", f)
	}
}

func TestCalibrate(t *testing.T) {
	meta := Metadata{BlockCount: 32, HeadCount: 32, HeadCountKV: 8, KeyLength: 128}
	hw := Hardware{VRAMBytes: 24 * gb, RAMBytes: 8 * gb}
	est := Estimate(meta, 20*gb, 8192, "f16", "f16", 1, hw)
	got := Calibrate(est, Measured{ModelBytes: 26 * gb, ContextBytes: gb, ComputeBytes: gb}, hw)
	if !got.Calibrated || got.Estimated || got.WeightsBytes != 26*gb || got.TotalBytes != 28*gb || got.Verdict != VerdictPartialOffload {
		t.Errorf("%+v", got)
	}
	if same := Calibrate(est, Measured{}, hw); same != est {
		t.Errorf("zero measurement changed the fit: %+v", same)
	}
}
