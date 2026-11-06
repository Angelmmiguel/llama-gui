// Command llama-gui-demo runs the real service against the fake llama-server with
// two emulated GPUs, sample models and steady traffic, so every screen has live data
// on a machine without a GPU. It is a development tool and never ships in the image.
//
//	go run ./cmd/llama-gui-demo --addr 127.0.0.1:18400 --dir ./tmp/demo
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/builds"
	"github.com/Angelmmiguel/llama-gui/internal/releases"
)

const fakeSMI = `#!/bin/sh
case "$*" in
  *--query-gpu*)
    u0=$(( $(date +%s) % 40 + 45 )); u1=$(( $(date +%s) % 30 + 20 ))
    echo "0, NVIDIA GeForce RTX 4090, GPU-demo-0, 575.64, 24564, 19010, $u0, 71"
    echo "1, NVIDIA GeForce RTX 4090, GPU-demo-1, 575.64, 24564, 17522, $u1, 64" ;;
  *) echo "<nvidia_smi_log><cuda_version>12.8</cuda_version></nvidia_smi_log>" ;;
esac
`

// fakeBench stands in for llama-bench. It honours the setup and times nothing:
// each line is a real capture with the fields the setup decides replaced, so the
// charts have what was asked for. @PERCENT@ makes one build faster than another.
const fakeBench = `#!/bin/sh
here=$(dirname "$0")
case "$1" in --help) cat "$here/llama-bench.help"; exit 0 ;; esac
p=512 n=128 fa=auto ctk=f16 ctv=f16 d=0
while [ $# -gt 1 ]; do
  case "$1" in
    -p) p=$2 ;; -n) n=$2 ;; -fa) fa=$2 ;; -ctk) ctk=$2 ;; -ctv) ctv=$2 ;; -d) d=$2 ;;
  esac
  shift 2
done
template=$(head -n 1 "$here/llama-bench.jsonl")
emit() { # n_prompt n_gen depth flash k v centi-tokens/s
  printf '%s\n' "$template" | sed \
    -e "s/\"backends\": \"[^\"]*\"/\"backends\": \"CUDA\"/" -e "s/\"n_gpu_layers\": [-0-9]*/\"n_gpu_layers\": 99/" \
    -e "s/\"n_prompt\": [0-9]*/\"n_prompt\": $1/" -e "s/\"n_gen\": [0-9]*/\"n_gen\": $2/" -e "s/\"n_depth\": [0-9]*/\"n_depth\": $3/" \
    -e "s/\"flash_attn\": [-0-9a-z]*/\"flash_attn\": $4/" -e "s/\"type_k\": \"[^\"]*\"/\"type_k\": \"$5\"/" -e "s/\"type_v\": \"[^\"]*\"/\"type_v\": \"$6\"/" \
    -e "s/\"avg_ts\": [0-9.]*/\"avg_ts\": $(( $7 / 100 )).$(( $7 % 100 / 10 ))$(( $7 % 10 ))/" -e "s/\"stddev_ts\": [0-9.]*/\"stddev_ts\": 0.$(( $7 % 7 + 2 ))/"
  sleep 0.15
}
cost() { case "$1" in q8_0) echo 2 ;; q4_0) echo 7 ;; *) echo 0 ;; esac; }
IFS=,
for a in $fa; do
  case "$a" in on) flash=1 knee=65536 ;; off) flash=0 knee=26000 ;; *) flash=-1 knee=45000 ;; esac
  for k in $ctk; do for v in $ctv; do for depth in $d; do
    left=$(( (100 - $(cost "$k") - $(cost "$v")) * @PERCENT@ ))
    fade=$(( knee * 1000 / (knee + depth) ))
    emit "$p" 0 "$depth" "$flash" "$k" "$v" $(( 530000 * left / 10000 * fade / 1000 ))
    emit 0 "$n" "$depth" "$flash" "$k" "$v" $(( 17000 * left / 10000 * fade / 1000 ))
  done; done; done
done
`

// demoModel is a sparse GGUF with realistic metadata and a realistic size.
type demoModel struct {
	alias, file, arch, quant string
	gib                      float64
	blocks, embd, heads, ctx uint32
	experts                  uint32
	parallel                 int
	loaded, onDemand         bool
	idle                     int
}

var models = []demoModel{
	{"qwen3-coder", "Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf", "qwen3moe", "Q4_K_M", 17.3, 48, 2048, 32, 65536, 8, 4, true, false, 0},
	{"gpt-oss-20b", "gpt-oss-20b-mxfp4.gguf", "gpt-oss", "MXFP4", 12.1, 24, 2880, 64, 32768, 32, 2, true, true, 600},
	{"nomic-embed", "nomic-embed-text-v1.5.f16.gguf", "nomic-bert", "F16", 0.27, 12, 768, 12, 8192, 0, 4, true, true, 600},
	{"gemma-3-12b", "gemma-3-12b-it-Q5_K_M.gguf", "gemma3", "Q5_K_M", 8.4, 48, 3840, 16, 32768, 0, 2, false, true, 600},
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18400", "listen address")
	dir := flag.String("dir", "./tmp/demo", "state directory")
	traffic := flag.Bool("traffic", true, "generate steady requests")
	flag.Parse()
	if err := run(*addr, *dir, *traffic); err != nil {
		log.Fatal(err)
	}
}

func run(addr, dir string, traffic bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	bin := filepath.Join(dir, "bin")
	buildsDir, modelsDir, data := filepath.Join(dir, "builds"), filepath.Join(dir, "models"), filepath.Join(dir, "data")
	for _, d := range []string{bin, buildsDir, modelsDir, data} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
	}
	for _, pkg := range []string{"llama-gui", "fake-llama-server"} {
		out, err := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(bin, pkg), "./cmd/"+pkg).CombinedOutput()
		if err != nil {
			return fmt.Errorf("build %s: %w\n%s", pkg, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "nvidia-smi"), []byte(fakeSMI), 0o755); err != nil { //nolint:gosec // an executable
		return err
	}
	var active string
	for _, b := range []struct {
		tag     string
		percent int
	}{{"b12098", 91}, {"b12110", 100}} {
		if active, err = installFake(buildsDir, filepath.Join(bin, "fake-llama-server"), b.tag, b.percent); err != nil {
			return err
		}
	}
	for _, m := range models {
		if err := writeGGUF(filepath.Join(modelsDir, m.file), m); err != nil {
			return err
		}
	}

	github, err := serveFakeGitHub()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, filepath.Join(bin, "llama-gui"), "serve", "--addr", addr, "--data", data,
		"--builds", buildsDir, "--models", modelsDir, "--llama-version", "none")
	cmd.Env = append(os.Environ(), "LLAMA_GUI_GITHUB_API="+github, "PATH="+bin+":"+os.Getenv("PATH"), "FAKE_LLAMA_GPUS=2", "FAKE_LLAMA_TOKEN_MS=25", "FAKE_LLAMA_TOKENS=160", "FAKE_LLAMA_LOAD_MS=800")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 15 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	base := "http://" + addr
	if err := setup(ctx, base, active); err != nil {
		stop()
		_ = cmd.Wait()
		return err
	}
	log.Printf("demo ready at %s/ui/", base)
	if traffic {
		for i := range 3 {
			go generate(ctx, base, i)
		}
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// installFake puts the fake server where a real install would.
func installFake(dir, binary, tag string, benchPercent int) (string, error) {
	id, err := builds.BuildID(tag, builds.BackendCUDA)
	if err != nil {
		return "", err
	}
	target := filepath.Join(dir, id)
	if err := os.MkdirAll(target, 0o750); err != nil {
		return "", err
	}
	server := filepath.Join(target, "llama-server")
	if err := os.Remove(server); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Link(binary, server); err != nil {
		return "", err
	}
	if err := installFakeBench(target, benchPercent); err != nil {
		return "", err
	}
	man, err := json.Marshal(builds.Manifest{
		ID: id, Tag: tag, ReleaseTag: tag, Channel: releases.ChannelNightly, Backend: builds.BackendCUDA, Variant: "12.8", Arch: builds.ArchX64,
		InstalledAt: time.Now(),
		Probe:       builds.Probe{Version: "0.9.0-dev", Build: tag, Commit: "2ada83079", Router: true, Flags: supportedFlags},
	})
	if err != nil {
		return "", err
	}
	return id, os.WriteFile(filepath.Join(target, "manifest.json"), man, 0o600)
}

// installFakeBench copies the captures next to the script. They are read from the
// source tree, which is also where this command builds the fake server from.
func installFakeBench(target string, percent int) error {
	for _, name := range []string{"llama-bench.jsonl", "help.txt"} {
		data, err := os.ReadFile(filepath.Join("internal", "bench", "testdata", name))
		if err != nil {
			return fmt.Errorf("run the demo from the repository root: %w", err)
		}
		if err := os.WriteFile(filepath.Join(target, strings.Replace(name, "help.txt", "llama-bench.help", 1)), data, 0o600); err != nil {
			return err
		}
	}
	script := strings.ReplaceAll(fakeBench, "@PERCENT@", strconv.Itoa(percent))
	return os.WriteFile(filepath.Join(target, "llama-bench"), []byte(script), 0o755) //nolint:gosec // an executable
}

var supportedFlags = []string{"--models-preset", "--ctx-size", "--parallel", "--n-gpu-layers", "--split-mode", "--tensor-split",
	"--flash-attn", "--cache-type-k", "--cache-type-v", "--cache-reuse", "--batch-size", "--ubatch-size", "--threads", "--temp",
	"--top-p", "--top-k", "--min-p", "--repeat-penalty", "--jinja", "--mlock", "--model", "--mmproj"}

func setup(ctx context.Context, base, active string) error {
	api := base + "/api/v1"
	for range 100 {
		resp, err := http.Get(api + "/healthz") //nolint:gosec // the address is our own
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := post(ctx, api+"/builds/"+active+"/activate", ""); err != nil {
		return fmt.Errorf("activate: %w", err)
	}
	for _, m := range models {
		body, err := json.Marshal(map[string]any{
			"name": m.alias, "alias": m.alias, "model_file": m.file, "ctx_size": m.ctx, "parallel": m.parallel, "ngl": "99",
			"threads": 16, "batch": 2048, "ubatch": 512, "on_demand": m.onDemand, "idle_unload_seconds": m.idle, "load_at_start": m.loaded,
		})
		if err != nil {
			return err
		}
		// A profile surviving from the last run is fine.
		if err := post(ctx, api+"/profiles", string(body)); err != nil {
			log.Printf("profile %s: %v", m.alias, err)
		}
	}
	for _, m := range models {
		if m.loaded {
			for range 20 {
				if err := post(ctx, api+"/models/"+m.alias+"/load", ""); err == nil {
					break
				}
				time.Sleep(300 * time.Millisecond)
			}
		}
	}
	return nil
}

func post(ctx context.Context, url, body string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", resp.Status, data)
	}
	return nil
}

// generate sends a realistic mix: mostly streamed chat, some embeddings and infill,
// and now and then a prompt over the context limit.
func generate(ctx context.Context, base string, worker int) {
	for ctx.Err() == nil {
		var path, body string
		n := rand.IntN(100) //nolint:gosec // only traffic shape
		chat := func(model string, words, tokens int) string {
			b, _ := json.Marshal(map[string]any{"model": model, "stream": true, "max_tokens": tokens,
				"messages": []map[string]string{{"role": "user", "content": strings.Repeat("word ", words)}}})
			return string(b)
		}
		switch {
		case n < 5:
			path, body = "/v1/chat/completions", chat("qwen3-coder", 3000, 16)
		case n < 15:
			path, body = "/v1/embeddings", `{"model":"nomic-embed","input":"chunk of a document"}`
		case n < 30:
			path, body = "/infill", `{"model":"qwen3-coder","input_prefix":"func main() {","input_suffix":"}","n_predict":24}`
		case n < 65:
			path, body = "/v1/chat/completions", chat("gpt-oss-20b", 40+rand.IntN(800), 40+rand.IntN(120)) //nolint:gosec // only traffic shape
		default:
			path, body = "/v1/chat/completions", chat("qwen3-coder", 40+rand.IntN(1500), 60+rand.IntN(200)) //nolint:gosec // only traffic shape
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader([]byte(body)))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		time.Sleep(time.Duration(500+rand.IntN(2500)*(worker+1)) * time.Millisecond) //nolint:gosec // only traffic shape
	}
}

// writeGGUF writes a valid header and extends the file sparsely to the model's size.
func writeGGUF(path string, m demoModel) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	var kv bytes.Buffer
	str := func(s string) {
		_ = binary.Write(&kv, binary.LittleEndian, uint64(len(s)))
		kv.WriteString(s)
	}
	count := uint64(0)
	addStr := func(k, v string) {
		str(k)
		_ = binary.Write(&kv, binary.LittleEndian, uint32(8))
		str(v)
		count++
	}
	addU32 := func(k string, v uint32) {
		str(k)
		_ = binary.Write(&kv, binary.LittleEndian, uint32(4))
		_ = binary.Write(&kv, binary.LittleEndian, v)
		count++
	}
	addStr("general.architecture", m.arch)
	addStr("general.name", strings.TrimSuffix(m.file, ".gguf"))
	addU32(m.arch+".block_count", m.blocks)
	addU32(m.arch+".embedding_length", m.embd)
	addU32(m.arch+".attention.head_count", m.heads)
	addU32(m.arch+".attention.head_count_kv", m.heads)
	addU32(m.arch+".context_length", m.ctx)
	if m.experts > 0 {
		addU32(m.arch+".expert_count", m.experts)
	}
	var head bytes.Buffer
	head.WriteString("GGUF")
	_ = binary.Write(&head, binary.LittleEndian, uint32(3))
	_ = binary.Write(&head, binary.LittleEndian, uint64(0))
	_ = binary.Write(&head, binary.LittleEndian, count)
	head.Write(kv.Bytes())
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(head.Bytes()); err != nil {
		return err
	}
	return f.Truncate(int64(m.gib * (1 << 30)))
}
