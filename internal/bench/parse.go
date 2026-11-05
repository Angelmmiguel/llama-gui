package bench

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Result is one finished test of one configuration.
type Result struct {
	Backend      string  `json:"backend"`
	NGL          string  `json:"ngl"`
	FlashAttn    string  `json:"flash_attn"` // on, off or auto
	CacheK       string  `json:"cache_k"`
	CacheV       string  `json:"cache_v"`
	Test         string  `json:"test"` // pp512, tg128 @ d4096
	Kind         string  `json:"kind"` // pp, tg or pg
	N            int     `json:"n"`    // tokens processed or generated
	Depth        int     `json:"depth"`
	TokensPerSec float64 `json:"tokens_per_sec"`
	StdDev       float64 `json:"stddev"`
}

// rawResult holds the llama-bench jsonl fields we use. Everything else is
// ignored, as are fields a build omits. flash_attn is a number on recent
// builds (-1 auto) and a boolean on older ones.
type rawResult struct {
	Backends  string          `json:"backends"`
	NGL       json.Number     `json:"n_gpu_layers"`
	FlashAttn json.RawMessage `json:"flash_attn"`
	TypeK     string          `json:"type_k"`
	TypeV     string          `json:"type_v"`
	NPrompt   int             `json:"n_prompt"`
	NGen      int             `json:"n_gen"`
	NDepth    int             `json:"n_depth"`
	AvgTS     float64         `json:"avg_ts"`
	StddevTS  float64         `json:"stddev_ts"`
}

// ParseLine decodes one jsonl line. ok is false for blank lines, non-JSON
// output such as backend load messages, and lines without a test.
func ParseLine(line string) (r Result, ok bool, err error) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return Result{}, false, nil
	}
	var raw rawResult
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return Result{}, false, fmt.Errorf("bench: parse line: %w", err)
	}
	if raw.NPrompt == 0 && raw.NGen == 0 {
		return Result{}, false, nil
	}
	r = Result{
		Backend:      raw.Backends,
		NGL:          raw.NGL.String(),
		FlashAttn:    flashName(raw.FlashAttn),
		CacheK:       raw.TypeK,
		CacheV:       raw.TypeV,
		Depth:        raw.NDepth,
		TokensPerSec: raw.AvgTS,
		StdDev:       raw.StddevTS,
	}
	switch {
	case raw.NPrompt > 0 && raw.NGen > 0:
		r.Kind, r.N = "pg", raw.NPrompt+raw.NGen
		r.Test = fmt.Sprintf("pp%d+tg%d", raw.NPrompt, raw.NGen)
	case raw.NPrompt > 0:
		r.Kind, r.N = "pp", raw.NPrompt
		r.Test = "pp" + strconv.Itoa(raw.NPrompt)
	default:
		r.Kind, r.N = "tg", raw.NGen
		r.Test = "tg" + strconv.Itoa(raw.NGen)
	}
	if raw.NDepth > 0 {
		r.Test += " @ d" + strconv.Itoa(raw.NDepth)
	}
	return r, true, nil
}

func flashName(v json.RawMessage) string {
	switch strings.TrimSpace(string(v)) {
	case "1", "true":
		return "on"
	case "0", "false":
		return "off"
	default:
		return "auto"
	}
}

// ParseOutput reads a whole jsonl stream, skipping lines it cannot parse.
func ParseOutput(r io.Reader) ([]Result, error) {
	var out []Result
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if res, ok, err := ParseLine(sc.Text()); err == nil && ok {
			out = append(out, res)
		}
	}
	return out, sc.Err()
}

// ParseHelp turns llama-bench --help into a flag predicate. It matches whole
// flag tokens, so -d does not match --delay.
func ParseHelp(help string) func(flag string) bool {
	known := map[string]bool{}
	for _, m := range flagToken.FindAllStringSubmatch(help, -1) {
		known[m[1]] = true
	}
	return func(flag string) bool { return known[flag] }
}

var flagToken = regexp.MustCompile(`(?m)(?:^|[\s,])(--?[A-Za-z][A-Za-z0-9-]*)`)
