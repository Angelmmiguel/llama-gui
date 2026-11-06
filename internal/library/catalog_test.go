package library

import (
	"slices"
	"testing"
)

func TestCatalogValid(t *testing.T) {
	entries := Catalog()
	if len(entries) < 10 {
		t.Fatalf("%d entries", len(entries))
	}
	seen := map[string]bool{}
	valid := []string{"chat", "vision", "embedding", "code", "moe"}
	for _, e := range entries {
		if !ValidRepo(e.Repo) || seen[e.Repo] || e.Name == "" || e.Description == "" || len(e.Tags) == 0 {
			t.Errorf("bad entry %+v", e)
		}
		seen[e.Repo] = true
		for _, tag := range e.Tags {
			if !slices.Contains(valid, tag) {
				t.Errorf("%s: unknown tag %q", e.Repo, tag)
			}
		}
	}
}

func cand(name string, moe bool, sizesGB map[string]int64, tags ...string) Candidate {
	meta := Metadata{BlockCount: 32, HeadCount: 32, HeadCountKV: 8, KeyLength: 128, IsMoE: moe}
	if moe {
		meta.ExpertCount, meta.BlockCount = 64, 16
	}
	c := Candidate{CatalogEntry: CatalogEntry{Repo: "x/" + name, Name: name, Tags: tags}, Meta: meta}
	for q, s := range sizesGB {
		c.Variants = append(c.Variants, Variant{ID: q, Quant: q, SizeBytes: s * gb, Files: []File{{Name: q + ".gguf"}}})
	}
	return c
}

func TestSuggest(t *testing.T) {
	cands := []Candidate{
		cand("dense-big", false, map[string]int64{"Q4_K_M": 20, "Q8_0": 40}, "chat"),
		cand("dense-small", false, map[string]int64{"Q4_K_M": 5, "Q8_0": 9}, "chat"),
		cand("moe", true, map[string]int64{"Q4_K_M": 15, "Q8_0": 30}, "chat", "moe"),
		cand("moe-huge", true, map[string]int64{"MXFP4": 60}, "chat", "moe"),
		cand("embed", false, map[string]int64{"F16": 1}, "embedding"),
	}
	hw := Hardware{VRAMBytes: 24 * gb, RAMBytes: 48 * gb, HeadroomBytes: gb}
	got := Suggest(cands, hw, 8192, "f16")
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	if got[0].Category != SuggestLargestDense || got[0].Name != "dense-big" || got[0].Quant != "Q4_K_M" {
		t.Errorf("dense: %+v", got[0])
	}
	if got[1].Category != SuggestFastLongCtx || got[1].Name != "moe" || got[1].Quant != "Q4_K_M" {
		t.Errorf("fast: %+v", got[1])
	}
	if got[2].Category != SuggestMostCapable || got[2].Name != "moe-huge" || got[2].Fit.Verdict != VerdictOffloadExperts {
		t.Errorf("capable: %+v", got[2])
	}

	// Without MoE the fast card falls back to the smallest model that fits; nothing offloads on tiny hardware.
	got = Suggest(cands[:2], Hardware{VRAMBytes: 12 * gb, RAMBytes: 4 * gb}, 8192, "q8_0")
	if len(got) != 2 || got[1].Category != SuggestFastLongCtx || got[1].Variant.SizeBytes != 5*gb || got[0].Quant != "Q8_0" {
		t.Errorf("fallback: %+v", got)
	}
	if got := Suggest(cands, Hardware{VRAMBytes: gb, RAMBytes: gb}, 8192, "f16"); len(got) != 0 {
		t.Errorf("nothing should qualify: %+v", got)
	}
	host := Suggest(cands, Hardware{RAMBytes: 24 * gb}, 8192, "f16")
	if len(host) == 0 || host[0].Fit.Verdict != VerdictFitsRAM {
		t.Errorf("host-only: %+v", host)
	}
}

func TestSuggestLeavesRoomToGrow(t *testing.T) {
	cands := []Candidate{cand("moe", true, map[string]int64{"Q4_0": 67, "Q8_0": 120}, "chat", "moe")}
	got := Suggest(cands, Hardware{VRAMBytes: 128 * gb, RAMBytes: 128 * gb}, 32768, "f16")
	if len(got) == 0 || got[0].Category != SuggestFastLongCtx || got[0].Quant != "Q4_0" {
		t.Errorf("%+v", got)
	}
}

func TestSuggestSkipsUnquantisedFiles(t *testing.T) {
	cands := []Candidate{cand("m", false, map[string]int64{"Q4_K_M": 5, "BF16": 18, "F16": 19, "F32": 20}, "chat")}
	got := Suggest(cands, Hardware{VRAMBytes: 48 * gb, RAMBytes: 64 * gb}, 8192, "f16")
	if len(got) == 0 || got[0].Quant != "Q4_K_M" {
		t.Errorf("%+v", got)
	}
}
