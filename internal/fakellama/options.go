// Package fakellama is a test double for llama-server in router mode. Its shapes
// and log lines follow the captures in internal/llama/testdata.
package fakellama

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Mode says what the process should do.
type Mode int

// Modes.
const (
	ModeServe Mode = iota
	ModeVersion
	ModeHelp
	ModeListDevices
)

// Options configures the fake.
type Options struct {
	Host, Preset, SlotSavePath string
	Port, ModelsMax            int
	Autoload, Metrics, JSONL   bool

	LoadDelay, TokenDelay, CrashAfter time.Duration
	Tokens                            int // tokens generated when the request sets no limit
	Ctx                               int // prompts with more words than this get a 400
}

// ParseArgs reads the flags llama-gui passes and ignores everything else.
func ParseArgs(args []string) (Options, Mode) {
	o := Options{Host: "127.0.0.1", Port: 8080, Autoload: true}
	mode := ModeServe
	for i := 0; i < len(args); i++ {
		name, val, hasVal := strings.Cut(args[i], "=")
		next := func() string {
			if hasVal {
				return val
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch name {
		case "--host":
			o.Host = next()
		case "--port":
			o.Port, _ = strconv.Atoi(next())
		case "--models-preset":
			o.Preset = next()
		case "--models-max":
			o.ModelsMax, _ = strconv.Atoi(next())
		case "--models-memory-margin", "-lv":
			next()
		case "--slot-save-path":
			o.SlotSavePath = next()
		case "--no-models-autoload":
			o.Autoload = false
		case "--metrics":
			o.Metrics = true
		case "--log-jsonl":
			o.JSONL = true
		case "--version":
			mode = ModeVersion
		case "--help", "-h", "--usage":
			mode = ModeHelp
		case "--list-devices":
			mode = ModeListDevices
		}
	}
	return o, mode
}

// ApplyEnv reads the FAKE_LLAMA_* knobs, falling back to the documented defaults.
func (o *Options) ApplyEnv(getenv func(string) string) {
	num := func(key string, def int) int {
		if n, err := strconv.Atoi(getenv(key)); err == nil {
			return n
		}
		return def
	}
	o.LoadDelay = time.Duration(num("FAKE_LLAMA_LOAD_MS", 50)) * time.Millisecond
	o.TokenDelay = time.Duration(num("FAKE_LLAMA_TOKEN_MS", 5)) * time.Millisecond
	o.Tokens = num("FAKE_LLAMA_TOKENS", 16)
	o.Ctx = num("FAKE_LLAMA_CTX", 2000)
	o.CrashAfter = time.Duration(num("FAKE_LLAMA_CRASH_AFTER_MS", 0)) * time.Millisecond
}

type kv struct{ key, val string }

type presetModel struct {
	id   string
	text string
	kvs  []kv
}

// parsePreset reads the router's INI: one section per model, [*] holds defaults.
func parsePreset(r io.Reader) []presetModel {
	var models []presetModel
	var global []kv
	cur := -1
	inGlobal := false
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || line[0] == ';' || line[0] == '#':
		case line[0] == '[' && line[len(line)-1] == ']':
			id := strings.TrimSpace(line[1 : len(line)-1])
			inGlobal = id == "*"
			if !inGlobal {
				models = append(models, presetModel{id: id})
				cur = len(models) - 1
			}
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			e := kv{strings.TrimSpace(k), strings.TrimSpace(v)}
			if inGlobal {
				global = append(global, e)
			} else if cur >= 0 {
				models[cur].kvs = append(models[cur].kvs, e)
				models[cur].text += line + "\n"
			}
		}
	}
	for i := range models {
		models[i].kvs = append(append([]kv{}, global...), models[i].kvs...)
		models[i].text = "[" + models[i].id + "]\n" + models[i].text + "\n"
	}
	return models
}

func loadPreset(path string) ([]presetModel, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parsePreset(f), nil
}
