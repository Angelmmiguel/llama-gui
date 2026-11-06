// Package profiles owns launch profiles: their shape, validation, storage as
// JSON documents and rendering to the router's preset INI.
package profiles

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// ErrInvalid marks a profile that failed validation.
var ErrInvalid = errors.New("invalid profile")

// Profile is one model configuration. Zero values mean "leave it to upstream";
// pointers are used where the zero value is itself meaningful.
type Profile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Alias     string `json:"alias"`
	ModelFile string `json:"model_file"`       // relative to the models dir
	MMProj    string `json:"mmproj,omitempty"` // relative to the models dir
	Jinja     *bool  `json:"jinja,omitempty"`  // upstream defaults to on, so false has to be said
	MLock     bool   `json:"mlock,omitempty"`
	CtxSize   int    `json:"ctx_size"` // required: --fit would otherwise take all memory
	Parallel  int    `json:"parallel,omitempty"`
	NGL       string `json:"ngl,omitempty"` // a number, auto or all

	SplitMode   string `json:"split_mode,omitempty"`
	TensorSplit string `json:"tensor_split,omitempty"`
	FlashAttn   string `json:"flash_attn,omitempty"` // auto, on or off
	CacheTypeK  string `json:"cache_type_k,omitempty"`
	CacheTypeV  string `json:"cache_type_v,omitempty"`
	CacheReuse  *int   `json:"cache_reuse,omitempty"`
	Batch       int    `json:"batch,omitempty"`
	UBatch      int    `json:"ubatch,omitempty"`
	Threads     int    `json:"threads,omitempty"`

	SpecType       string `json:"spec_type,omitempty"` // comma-separated --spec-type values
	SpecDraftMax   int    `json:"spec_draft_max,omitempty"`
	SpecDraftModel string `json:"spec_draft_model,omitempty"` // relative to the models dir
	SpecDraftNGL   string `json:"spec_draft_ngl,omitempty"`   // a number, auto or all

	Temp          *float64 `json:"temp,omitempty"`
	TopP          *float64 `json:"top_p,omitempty"`
	TopK          *int     `json:"top_k,omitempty"`
	MinP          *float64 `json:"min_p,omitempty"`
	RepeatPenalty *float64 `json:"repeat_penalty,omitempty"`

	LoadAtStart       bool     `json:"load_at_start,omitempty"`
	IdleUnloadSeconds int      `json:"idle_unload_seconds,omitempty"`
	OnDemand          bool     `json:"on_demand,omitempty"`
	ExtraArgs         []string `json:"extra_args,omitempty"`
}

var (
	aliasRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	nglRe         = regexp.MustCompile(`^(auto|all|-?[0-9]+)$`)
	tensorSplitRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(,[0-9]+(\.[0-9]+)?)*$`)
	cacheTypeRe   = regexp.MustCompile(`^[a-z0-9_]+$`)
	specTypeRe    = regexp.MustCompile(`^[a-z0-9-]+(,[a-z0-9-]+)*$`)
	flagKeyRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

func invalid(field, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalid, field, fmt.Sprintf(format, args...))
}

// Validate checks everything that ends up in the preset or a URL.
func (p Profile) Validate() error {
	if strings.TrimSpace(p.Name) == "" || strings.ContainsFunc(p.Name, unicode.IsControl) {
		return invalid("name", "required, without control characters")
	}
	if len(p.Alias) > 128 || !aliasRe.MatchString(p.Alias) {
		return invalid("alias", "must match %s", aliasRe)
	}
	if err := checkModelPath("model_file", p.ModelFile); err != nil {
		return err
	}
	if p.MMProj != "" {
		if err := checkModelPath("mmproj", p.MMProj); err != nil {
			return err
		}
	}
	if p.CtxSize <= 0 {
		return invalid("ctx_size", "required and positive")
	}
	if err := p.validateSpeculation(); err != nil {
		return err
	}
	for _, c := range []struct {
		field string
		val   int
		min   int
	}{
		{"parallel", p.Parallel, -1}, {"threads", p.Threads, -1}, {"batch", p.Batch, 0}, {"ubatch", p.UBatch, 0},
		{"idle_unload_seconds", p.IdleUnloadSeconds, 0},
	} {
		if c.val < c.min {
			return invalid(c.field, "must be at least %d", c.min)
		}
	}
	for _, c := range []struct {
		field string
		val   *int
	}{{"cache_reuse", p.CacheReuse}, {"top_k", p.TopK}} {
		if c.val != nil && *c.val < 0 {
			return invalid(c.field, "must not be negative")
		}
	}
	for _, c := range []struct {
		field string
		val   *float64
	}{{"temp", p.Temp}, {"top_p", p.TopP}, {"min_p", p.MinP}, {"repeat_penalty", p.RepeatPenalty}} {
		if c.val != nil && (*c.val < 0 || math.IsNaN(*c.val) || math.IsInf(*c.val, 0)) {
			return invalid(c.field, "must be a non-negative number")
		}
	}
	if p.NGL != "" && !nglRe.MatchString(p.NGL) {
		return invalid("ngl", "must be a number, auto or all")
	}
	switch p.SplitMode {
	case "", "none", "layer", "row", "tensor":
	default:
		return invalid("split_mode", "must be none, layer, row or tensor")
	}
	if p.TensorSplit != "" && !tensorSplitRe.MatchString(p.TensorSplit) {
		return invalid("tensor_split", "must be comma-separated numbers")
	}
	switch p.FlashAttn {
	case "", "auto", "on", "off":
	default:
		return invalid("flash_attn", "must be auto, on or off")
	}
	for _, c := range []struct{ field, val string }{{"cache_type_k", p.CacheTypeK}, {"cache_type_v", p.CacheTypeV}} {
		if c.val != "" && !cacheTypeRe.MatchString(c.val) {
			return invalid(c.field, "must match %s", cacheTypeRe)
		}
	}
	_, err := p.extraKVs()
	return err
}

func checkModelPath(field, path string) error {
	if path == "" {
		return invalid(field, "required")
	}
	if strings.HasPrefix(path, "/") || strings.ContainsRune(path, '\\') {
		return invalid(field, "must be relative to the models dir")
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return invalid(field, "must stay inside the models dir")
		}
	}
	return checkValue(field, path)
}

// checkValue rejects anything that could end a value or start a new key or
// section once written to the INI.
func checkValue(field, v string) error {
	if strings.ContainsAny(v, "[]=;#") || strings.ContainsFunc(v, unicode.IsControl) {
		return invalid(field, "must not contain control characters or any of [ ] = ; #")
	}
	if strings.TrimSpace(v) != v {
		return invalid(field, "must not start or end with spaces")
	}
	return nil
}

// typesWithDraftFile are the speculative decoding types that read a second GGUF file. MTP uses
// heads inside the main model, and the n-gram types need no model at all.
var typesWithDraftFile = []string{"draft", "draft-simple", "draft-eagle3", "draft-dflash", "draft-dspark"}

func (p Profile) validateSpeculation() error {
	if p.SpecType != "" && !specTypeRe.MatchString(p.SpecType) {
		return invalid("spec_type", "must be a comma-separated list of types")
	}
	if p.SpecDraftMax < 0 {
		return invalid("spec_draft_max", "must not be negative")
	}
	if p.SpecDraftNGL != "" && !nglRe.MatchString(p.SpecDraftNGL) {
		return invalid("spec_draft_ngl", "must be a number, auto or all")
	}
	if p.SpecDraftModel != "" {
		return checkModelPath("spec_draft_model", p.SpecDraftModel)
	}
	for t := range strings.SplitSeq(p.SpecType, ",") {
		if slices.Contains(typesWithDraftFile, t) {
			return invalid("spec_draft_model", "--spec-type %s needs a draft model file", t)
		}
	}
	return nil
}

// reserved reports flags the router owns or that would bypass validation. The router expands
// short names in a preset, so "md" is --model-draft and each short name has to be listed too.
func reserved(key string) bool {
	switch key {
	case "host", "port", "reuse-port", "alias", "a", "api-key", "api-prefix", "load-on-startup",
		"log-jsonl", "no-log-jsonl", "log-disable":
		return true
	case "model", "m", "mmproj", "mm", "model-draft", "md", "model-vocoder", "lora", "lora-scaled", "path":
		return true
	case "hf", "hfr", "hff", "hft", "hfd", "hfrd", "mu", "mmu", "dr", "lcs", "lcd", "jf":
		return true
	// These let a request reach beyond the model: running commands and editing files through
	// built-in tools, MCP servers, RPC workers and tunnels to outside this machine.
	case "tools", "agent", "ag", "no-agent", "no-ag", "rpc", "connect", "connect-code":
		return true
	case "spec-default":
		return false
	}
	// Anything naming a file, directory or remote source would write or read outside
	// the models directory as the service user, for example --log-file over the database.
	// A -default flag picks a model to download.
	for _, suffix := range []string{"-file", "-path", "-dir", "-url", "-model", "-repo", "-hf", "-default"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	for _, prefix := range []string{
		"models-", "no-models-", "hf-", "ssl-", "control-vector", "lookup-cache-", "fim-",
		"tools-", "mcp-", "ui-mcp-", "webui-mcp-", "no-ui-mcp-", "no-webui-mcp-",
	} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

type kv struct{ key, val string }

// extraKVs turns ExtraArgs into INI keys: --foo bar is foo = bar, --foo=bar too, and
// a bare --flag is flag = 1. An element may hold one token or several.
func (p Profile) extraKVs() ([]kv, error) {
	var toks []string
	for _, a := range p.ExtraArgs {
		toks = append(toks, strings.Fields(a)...)
	}
	typed := map[string]bool{}
	for _, e := range p.typedKVs("") {
		typed[e.key] = true
	}
	var out []kv
	for i := 0; i < len(toks); i++ {
		name, ok := strings.CutPrefix(toks[i], "--")
		if !ok {
			return nil, invalid("extra_args", "%q is not a long flag", toks[i])
		}
		key, val, hasVal := strings.Cut(name, "=")
		if !hasVal {
			val = "1"
			if i+1 < len(toks) && !strings.HasPrefix(toks[i+1], "--") {
				i++
				val = toks[i]
			}
		}
		if !flagKeyRe.MatchString(key) {
			return nil, invalid("extra_args", "%q is not a long flag", toks[i])
		}
		if reserved(key) {
			return nil, invalid("extra_args", "--%s is reserved", key)
		}
		if typed[key] {
			return nil, invalid("extra_args", "--%s repeats a profile setting", key)
		}
		typed[key] = true
		if err := checkValue("extra_args", val); err != nil {
			return nil, err
		}
		out = append(out, kv{key, val})
	}
	return out, nil
}
