package library

import (
	"context"
	_ "embed"
	"encoding/json"
	"slices"
	"sync"
)

//go:embed catalog.json
var catalogJSON []byte

// Suggestion categories, in display order.
const (
	SuggestLargestDense = "largest_dense"
	SuggestFastLongCtx  = "fast_long_context"
	SuggestMostCapable  = "most_capable_offload"
)

// CatalogEntry is a starter-catalog repo.
type CatalogEntry struct {
	Repo        string   `json:"repo"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"` // chat, vision, embedding, code, moe
}

// Catalog returns the embedded starter catalog.
func Catalog() []CatalogEntry {
	var entries []CatalogEntry
	if err := json.Unmarshal(catalogJSON, &entries); err != nil {
		panic("library: bad catalog.json: " + err.Error())
	}
	return entries
}

// Candidate is a catalog entry with its hub files and the architecture metadata of one of them.
type Candidate struct {
	CatalogEntry
	Variants []Variant `json:"variants"`
	Meta     Metadata  `json:"metadata"`
}

// ResolveCatalog looks up every entry's variants and header metadata, a few at a time. Entries the
// hub cannot answer for are left out; the hub client caches, so repeated calls are cheap.
func ResolveCatalog(ctx context.Context, c *Client, entries []CatalogEntry) []Candidate {
	out := make([]*Candidate, len(entries))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, e := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			info, err := c.Repo(ctx, e.Repo)
			if err != nil || len(info.Variants) == 0 {
				return
			}
			meta, err := c.ReadRemoteMetadata(ctx, e.Repo, info.Variants[0].Files[0].Name)
			if err != nil {
				return
			}
			out[i] = &Candidate{CatalogEntry: e, Variants: info.Variants, Meta: meta}
		}()
	}
	wg.Wait()
	var res []Candidate
	for _, c := range out {
		if c != nil {
			res = append(res, *c)
		}
	}
	return res
}

// Suggestion is one recommendation card.
type Suggestion struct {
	Category string   `json:"category"`
	Repo     string   `json:"repo"`
	Name     string   `json:"name"`
	Quant    string   `json:"quant"`
	Variant  Variant  `json:"variant"`
	Fit      Fit      `json:"fit"`
	Tags     []string `json:"tags"`
	Note     string   `json:"note"`
}

type option struct {
	c Candidate
	v Variant
	f Fit
}

// Suggest returns up to three cards: the largest dense model that fits on the GPU, the most capable
// MoE (or the smallest model, when there is none) that fits with room to grow, and the largest model
// that fits only with offload. A category with no qualifying model is omitted. Embedding models are
// never suggested. cacheType applies to both K and V.
func Suggest(cands []Candidate, hw Hardware, ctx int, cacheType string) []Suggestion {
	var fits, offload []option
	for _, c := range cands {
		if slices.Contains(c.Tags, "embedding") || c.Meta.IsMMProj {
			continue
		}
		for _, v := range c.Variants {
			f := Estimate(c.Meta, v.SizeBytes, ctx, cacheType, cacheType, 1, hw)
			o := option{c, v, f}
			switch f.Verdict {
			case VerdictFitsGPU, VerdictFitsRAM:
				fits = append(fits, o)
			case VerdictPartialOffload, VerdictOffloadExperts:
				offload = append(offload, o)
			}
		}
	}
	bySize := func(a, b option) int { return int(max(-1, min(1, a.v.SizeBytes-b.v.SizeBytes))) }
	largest := func(opts []option) (option, bool) {
		if len(opts) == 0 {
			return option{}, false
		}
		return slices.MaxFunc(opts, bySize), true
	}

	var out []Suggestion
	add := func(cat, note string, o option) {
		out = append(out, Suggestion{Category: cat, Repo: o.c.Repo, Name: o.c.Name, Quant: o.v.Quant,
			Variant: o.v, Fit: o.f, Tags: o.c.Tags, Note: note})
	}

	dense := slices.DeleteFunc(slices.Clone(fits), func(o option) bool { return o.c.Meta.IsMoE })
	top, haveDense := largest(dense)
	if haveDense {
		add(SuggestLargestDense, "The biggest dense model and quantisation that stays fully in memory at this context.", top)
	}

	pool := slices.DeleteFunc(slices.Clone(fits), func(o option) bool { return !o.c.Meta.IsMoE })
	note := "A mixture-of-experts model that fits with its KV cache, so context or slots can grow."
	if len(pool) == 0 {
		pool = slices.DeleteFunc(slices.Clone(fits), func(o option) bool { return haveDense && o.c.Repo == top.c.Repo && o.v.ID == top.v.ID })
		note = "The smallest model that fits, which leaves the most memory for context and slots."
		if len(pool) > 0 {
			add(SuggestFastLongCtx, note, slices.MinFunc(pool, bySize))
		}
	} else {
		best, _ := largest(pool)
		add(SuggestFastLongCtx, note, best)
	}

	if len(offload) > 0 {
		experts := slices.DeleteFunc(slices.Clone(offload), func(o option) bool { return o.f.Verdict != VerdictOffloadExperts })
		if best, ok := largest(experts); ok {
			add(SuggestMostCapable, "Keeps attention and the KV cache in VRAM and moves some expert weights to RAM with --n-cpu-moe.", best)
		} else if best, ok := largest(offload); ok {
			add(SuggestMostCapable, "Runs with some layers in system RAM, which is slower than a full GPU fit.", best)
		}
	}
	return out
}
