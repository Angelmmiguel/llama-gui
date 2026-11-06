package profiles

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func itoa(n int) string { return strconv.Itoa(n) }

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// typedKVs renders the profile's own fields, in the order the preset lists them.
func (p Profile) typedKVs(modelsDir string) []kv {
	var out []kv
	add := func(k, v string) {
		if v != "" {
			out = append(out, kv{k, v})
		}
	}
	addInt := func(k string, n int) {
		if n != 0 {
			add(k, itoa(n))
		}
	}
	addIntP := func(k string, n *int) {
		if n != nil {
			add(k, itoa(*n))
		}
	}
	addF := func(k string, f *float64) {
		if f != nil {
			add(k, ftoa(*f))
		}
	}
	add("model", filepath.Join(modelsDir, p.ModelFile))
	if p.MMProj != "" {
		add("mmproj", filepath.Join(modelsDir, p.MMProj))
	}
	addInt("ctx-size", p.CtxSize)
	addInt("parallel", p.Parallel)
	add("n-gpu-layers", p.NGL)
	add("split-mode", p.SplitMode)
	add("tensor-split", p.TensorSplit)
	add("flash-attn", p.FlashAttn)
	add("cache-type-k", p.CacheTypeK)
	add("cache-type-v", p.CacheTypeV)
	addIntP("cache-reuse", p.CacheReuse)
	addInt("batch-size", p.Batch)
	addInt("ubatch-size", p.UBatch)
	addInt("threads", p.Threads)
	add("spec-type", p.SpecType)
	addInt("spec-draft-n-max", p.SpecDraftMax)
	if p.SpecDraftModel != "" {
		add("spec-draft-model", filepath.Join(modelsDir, p.SpecDraftModel))
	}
	add("spec-draft-ngl", p.SpecDraftNGL)
	addF("temp", p.Temp)
	addF("top-p", p.TopP)
	addIntP("top-k", p.TopK)
	addF("min-p", p.MinP)
	addF("repeat-penalty", p.RepeatPenalty)
	if p.Jinja != nil {
		if *p.Jinja {
			add("jinja", "1")
		} else {
			add("no-jinja", "1")
		}
	}
	if p.MLock {
		add("mlock", "1")
	}
	if p.LoadAtStart {
		add("load-on-startup", "1")
	}
	return out
}

// Skipped is something RenderPreset left out. Flag is empty when the whole
// profile was dropped because it no longer validates.
type Skipped struct {
	Alias  string `json:"alias"`
	Flag   string `json:"flag,omitempty"`
	Reason string `json:"reason"`
}

// RenderPreset produces the router's INI. supported is asked about each flag by
// its long name, without dashes; nil accepts all. Flags the build lacks are
// reported instead of failing, since a profile can outlive a build switch. model
// and load-on-startup always pass: the first is required, and the second is a
// preset key rather than a command-line flag.
func RenderPreset(profiles []Profile, modelsDir string, supported func(flag string) bool) (string, []Skipped) {
	sorted := append([]Profile(nil), profiles...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Alias < sorted[j].Alias })

	var b strings.Builder
	b.WriteString("version = 1\n\n")
	var skipped []Skipped
	for _, p := range sorted {
		if err := p.Validate(); err != nil {
			skipped = append(skipped, Skipped{Alias: p.Alias, Reason: err.Error()})
			continue
		}
		extra, err := p.extraKVs()
		if err != nil {
			skipped = append(skipped, Skipped{Alias: p.Alias, Reason: err.Error()})
			continue
		}
		fmt.Fprintf(&b, "[%s]\n", p.Alias)
		for _, e := range append(p.typedKVs(modelsDir), extra...) {
			if e.key == "mlock" && usesLoadMode(supported) {
				e = kv{"load-mode", mlockLoadMode}
			}
			if e.key != "model" && e.key != "load-on-startup" && supported != nil && !supported(e.key) {
				skipped = append(skipped, Skipped{Alias: p.Alias, Flag: e.key, Reason: "not supported by this build"})
				continue
			}
			fmt.Fprintf(&b, "%s = %s\n", e.key, e.val)
		}
		b.WriteByte('\n')
	}
	return b.String(), skipped
}

// mlockLoadMode is what --mlock did: it locked the memory map, and mmap was the default.
const mlockLoadMode = "mmap+mlock"

// usesLoadMode says whether this build has replaced --mlock with --load-mode.
func usesLoadMode(supported func(flag string) bool) bool {
	return supported != nil && !supported("mlock") && supported("load-mode")
}

// WritePreset replaces the file at path atomically.
func WritePreset(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("profiles: write preset: %w", err)
	}
	f, err := os.CreateTemp(dir, ".preset-*")
	if err != nil {
		return fmt.Errorf("profiles: write preset: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.WriteString(content)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("profiles: write preset: %w", err)
	}
	return nil
}

// Command renders the profile as the equivalent command line, for display. The
// router builds the real one from the preset.
func Command(p Profile, modelsDir string, supported func(flag string) bool) string {
	var lines [][]string
	first := []string{"-m", shellQuote(filepath.Join(modelsDir, p.ModelFile))}
	if p.MMProj != "" {
		first = append(first, "--mmproj", shellQuote(filepath.Join(modelsDir, p.MMProj)))
	}
	lines = append(lines, append(first, "--alias", p.Alias))

	var second, third []string
	flag := func(dst *[]string, name, v string) {
		if v != "" {
			*dst = append(*dst, name, v)
		}
	}
	num := func(dst *[]string, name string, n int) {
		if n != 0 {
			flag(dst, name, itoa(n))
		}
	}
	flag(&second, "-c", itoa(p.CtxSize))
	num(&second, "-np", p.Parallel)
	flag(&second, "-ngl", p.NGL)
	flag(&second, "-fa", p.FlashAttn)

	flag(&third, "-ctk", p.CacheTypeK)
	flag(&third, "-ctv", p.CacheTypeV)
	flag(&third, "-sm", p.SplitMode)
	flag(&third, "-ts", p.TensorSplit)
	if p.CacheReuse != nil {
		flag(&third, "--cache-reuse", itoa(*p.CacheReuse))
	}
	num(&third, "-b", p.Batch)
	num(&third, "-ub", p.UBatch)
	num(&third, "-t", p.Threads)
	for _, f := range []struct {
		name string
		val  *float64
	}{{"--temp", p.Temp}, {"--top-p", p.TopP}} {
		if f.val != nil {
			flag(&third, f.name, ftoa(*f.val))
		}
	}
	if p.TopK != nil {
		flag(&third, "--top-k", itoa(*p.TopK))
	}
	for _, f := range []struct {
		name string
		val  *float64
	}{{"--min-p", p.MinP}, {"--repeat-penalty", p.RepeatPenalty}} {
		if f.val != nil {
			flag(&third, f.name, ftoa(*f.val))
		}
	}
	flag(&third, "--spec-type", p.SpecType)
	num(&third, "--spec-draft-n-max", p.SpecDraftMax)
	if p.SpecDraftModel != "" {
		flag(&third, "--spec-draft-model", shellQuote(filepath.Join(modelsDir, p.SpecDraftModel)))
	}
	flag(&third, "--spec-draft-ngl", p.SpecDraftNGL)
	if p.Jinja != nil {
		if *p.Jinja {
			third = append(third, "--jinja")
		} else {
			third = append(third, "--no-jinja")
		}
	}
	if p.MLock {
		if usesLoadMode(supported) {
			third = append(third, "--load-mode", mlockLoadMode)
		} else {
			third = append(third, "--mlock")
		}
	}
	for _, a := range p.ExtraArgs {
		third = append(third, strings.Fields(a)...)
	}
	lines = append(lines, second)
	if len(third) > 0 {
		lines = append(lines, third)
	}

	var b strings.Builder
	b.WriteString("llama-server")
	for _, l := range lines {
		b.WriteString(" \\\n  ")
		b.WriteString(strings.Join(l, " "))
	}
	return b.String()
}

func shellQuote(s string) string {
	if !strings.ContainsAny(s, " '\"$`&|<>()*?!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
