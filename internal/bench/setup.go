// Package bench runs llama-bench against a model while the router is paused,
// parses its jsonl output and derives what the Bench page shows.
package bench

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Limits keep one request from scheduling a day-long run.
const (
	DefaultPrompt = 512
	DefaultGen    = 128
	DefaultReps   = 3
	maxTests      = 256
	maxDepth      = 1 << 20
	maxDepths     = 12
	maxReps       = 50
	maxTokens     = 1 << 17
	secondsPerRep = 2.4 // from the design; a rough guide, not a promise
)

var (
	flashModes = []string{"on", "off", "auto"}
	cacheTypes = []string{"f32", "f16", "bf16", "q8_0", "q5_1", "q5_0", "q4_1", "q4_0", "iq4_nl"}
	nglPattern = regexp.MustCompile(`^(-1|[0-9]{1,3})$`)
)

// ErrInvalid wraps every Setup validation failure.
var ErrInvalid = errors.New("bench: invalid setup")

// ErrUnsupported means the build's llama-bench lacks a flag the setup needs.
var ErrUnsupported = errors.New("bench: flag not supported by this build")

// Setup is what the user picks. Cache types apply to both K and V, and
// llama-bench crosses the two lists, so n types mean n*n configs.
type Setup struct {
	ModelFile   string   `json:"model_file"` // relative to the models directory
	FlashAttn   []string `json:"flash_attn"` // on, off or auto
	CacheTypes  []string `json:"cache_types"`
	Depths      []int    `json:"depths"`
	Repetitions int      `json:"repetitions"`
	NPrompt     int      `json:"n_prompt"`
	NGen        int      `json:"n_gen"`
	NGL         string   `json:"ngl"` // empty keeps the build's default
}

// Normalize fills defaults and drops duplicates, so Validate and Args agree.
func (s Setup) Normalize() Setup {
	s.FlashAttn = dedupe(s.FlashAttn)
	s.CacheTypes = dedupe(s.CacheTypes)
	s.Depths = dedupe(s.Depths)
	if len(s.Depths) == 0 {
		s.Depths = []int{0}
	}
	if s.Repetitions == 0 {
		s.Repetitions = DefaultReps
	}
	if s.NPrompt == 0 {
		s.NPrompt = DefaultPrompt
	}
	if s.NGen == 0 {
		s.NGen = DefaultGen
	}
	return s
}

func dedupe[T comparable](in []T) []T {
	var out []T
	for _, v := range in {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// Validate checks the setup against strict allowlists. It does not touch the
// filesystem; the runner resolves the model file inside the models directory.
func (s Setup) Validate() error {
	s = s.Normalize()
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
	}
	if s.ModelFile == "" || !filepath.IsLocal(s.ModelFile) || !strings.HasSuffix(strings.ToLower(s.ModelFile), ".gguf") ||
		strings.HasPrefix(s.ModelFile, "-") || strings.ContainsAny(s.ModelFile, "\x00\n\r,") {
		return bad("model file %q", s.ModelFile)
	}
	for _, v := range s.FlashAttn {
		if !slices.Contains(flashModes, v) {
			return bad("flash attention %q", v)
		}
	}
	for _, v := range s.CacheTypes {
		if !slices.Contains(cacheTypes, v) {
			return bad("cache type %q", v)
		}
	}
	if len(s.Depths) > maxDepths {
		return bad("at most %d depths", maxDepths)
	}
	for _, d := range s.Depths {
		if d < 0 || d > maxDepth {
			return bad("depth %d", d)
		}
	}
	if s.Repetitions < 1 || s.Repetitions > maxReps {
		return bad("repetitions %d", s.Repetitions)
	}
	if s.NPrompt < 1 || s.NPrompt > maxTokens || s.NGen < 1 || s.NGen > maxTokens {
		return bad("prompt and generation lengths")
	}
	if s.NGL != "" && !nglPattern.MatchString(s.NGL) {
		return bad("gpu layers %q", s.NGL)
	}
	if n := s.Estimate().Tests; n > maxTests {
		return bad("%d tests, at most %d", n, maxTests)
	}
	return nil
}

// Estimate is the test count and a rough duration.
type Estimate struct {
	Tests   int `json:"tests"`
	Seconds int `json:"seconds"`
}

// Estimate counts one prompt and one generation test per combination.
func (s Setup) Estimate() Estimate {
	s = s.Normalize()
	configs := max(len(s.FlashAttn), 1) * max(len(s.CacheTypes), 1) * max(len(s.CacheTypes), 1)
	tests := configs * len(s.Depths) * 2
	return Estimate{Tests: tests, Seconds: int(float64(tests*s.Repetitions)*secondsPerRep + 0.5)}
}

// Args builds llama-bench's argv for a model at modelPath. supported reports
// whether the build's --help lists a flag. A flag the setup needs but the build
// lacks is an ErrUnsupported rather than a silently different benchmark.
func (s Setup) Args(modelPath string, supported func(flag string) bool) ([]string, error) {
	s = s.Normalize()
	if err := s.Validate(); err != nil {
		return nil, err
	}
	need := func(flag string) error {
		if supported != nil && !supported(flag) {
			return fmt.Errorf("%w: %s", ErrUnsupported, flag)
		}
		return nil
	}
	args := []string{"-m", modelPath,
		"-p", strconv.Itoa(s.NPrompt), "-n", strconv.Itoa(s.NGen), "-r", strconv.Itoa(s.Repetitions)}
	if s.NGL != "" {
		if err := need("-ngl"); err != nil {
			return nil, err
		}
		args = append(args, "-ngl", s.NGL)
	}
	if len(s.FlashAttn) > 0 {
		if err := need("-fa"); err != nil {
			return nil, err
		}
		args = append(args, "-fa", strings.Join(s.FlashAttn, ","))
	}
	if len(s.CacheTypes) > 0 {
		if err := need("-ctk"); err != nil {
			return nil, err
		}
		if err := need("-ctv"); err != nil {
			return nil, err
		}
		list := strings.Join(s.CacheTypes, ",")
		args = append(args, "-ctk", list, "-ctv", list)
	}
	if !slices.Equal(s.Depths, []int{0}) {
		if err := need("-d"); err != nil {
			return nil, err
		}
		args = append(args, "-d", joinInts(s.Depths))
	}
	return append(args, "-o", "jsonl"), nil
}

// Command is the display form of Args, as the design shows it.
func (s Setup) Command() string {
	args, err := s.Args(s.Normalize().ModelFile, nil)
	if err != nil {
		return ""
	}
	// Pairs go on lines of their own after the model, like the mockup.
	out := "llama-bench " + args[0] + " " + args[1]
	rest := args[2:]
	line := ""
	for i := 0; i < len(rest); i += 2 {
		pair := rest[i] + " " + rest[i+1]
		if line != "" && len(line)+len(pair) > 36 {
			out += " \\\n  " + line
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += pair
	}
	return out + " \\\n  " + line
}

func joinInts(v []int) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}
