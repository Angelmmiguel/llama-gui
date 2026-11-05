package fakellama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/llama"
	"github.com/Angelmmiguel/llama-gui/internal/logs"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newServer(t *testing.T, tweak func(*Options)) (*httptest.Server, *llama.Client, *syncBuf) {
	t.Helper()
	dir := t.TempDir()
	preset := filepath.Join(dir, "preset.ini")
	ini := "[*]\nmetrics = 1\n\n[smolvlm]\nctx-size = 4096\nmodel = /m/a.gguf\n\n[other]\nmodel = /m/b.gguf\n"
	if err := os.WriteFile(preset, []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := Options{Host: "127.0.0.1", Preset: preset, Autoload: true, Metrics: true, JSONL: true, SlotSavePath: dir,
		LoadDelay: 20 * time.Millisecond, TokenDelay: time.Millisecond, Tokens: 8, Ctx: 2000}
	if tweak != nil {
		tweak(&opts)
	}
	logs := &syncBuf{}
	s, err := New(opts, logs)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, llama.New(ts.URL, ts.Client()), logs
}

func post(t *testing.T, url string, body string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func waitStatus(t *testing.T, c *llama.Client, id, want string) llama.Model {
	t.Helper()
	for range 200 {
		ms, err := c.Models(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			if m.ID == id && m.Status == want {
				return m
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never became %s", id, want)
	return llama.Model{}
}

func TestParseArgsTolerant(t *testing.T) {
	o, mode := ParseArgs(strings.Fields("--host 0.0.0.0 --port 9 --models-preset p.ini --models-max 2 --models-memory-margin 512 --no-models-autoload --metrics --slot-save-path /s --log-jsonl -lv 3 --weird --other=x 5 --ctx-size 4096"))
	if mode != ModeServe || o.Host != "0.0.0.0" || o.Port != 9 || o.Preset != "p.ini" || o.ModelsMax != 2 || o.Autoload || !o.Metrics || !o.JSONL || o.SlotSavePath != "/s" {
		t.Errorf("%+v %v", o, mode)
	}
	if _, m := ParseArgs([]string{"--version"}); m != ModeVersion {
		t.Error("version")
	}
}

func TestRunInfo(t *testing.T) {
	for _, tc := range []struct{ flag, file string }{{"--version", "version.txt"}, {"--list-devices", "list-devices.txt"}, {"--help", "help.txt"}} {
		var out bytes.Buffer
		if code := Run(context.Background(), []string{tc.flag}, func(string) string { return "" }, &out, io.Discard); code != 0 {
			t.Fatal(code)
		}
		want, _ := os.ReadFile("../llama/testdata/" + tc.file)
		if !bytes.Equal(out.Bytes(), want) {
			t.Errorf("%s differs from capture", tc.flag)
		}
	}
}

func TestRunCrashAndTerm(t *testing.T) {
	ini := filepath.Join(t.TempDir(), "p.ini")
	_ = os.WriteFile(ini, []byte("[a]\nmodel=x\n"), 0o600)
	args := []string{"--port", "0", "--models-preset", ini}
	env := func(k string) string {
		if k == "FAKE_LLAMA_CRASH_AFTER_MS" {
			return "30"
		}
		return ""
	}
	if code := Run(context.Background(), args, env, io.Discard, io.Discard); code != 1 {
		t.Errorf("crash code %d", code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	if code := Run(ctx, args, func(string) string { return "" }, io.Discard, io.Discard); code != 0 {
		t.Errorf("term code %d", code)
	}
}

func TestModelsLifecycle(t *testing.T) {
	_, c, _ := newServer(t, nil)
	ctx := context.Background()
	ms, err := c.Models(ctx)
	if err != nil || len(ms) != 2 || ms[0].ID != "smolvlm" || ms[0].Status != "unloaded" || ms[0].Port != 0 || ms[0].ContextLength != 8192 {
		t.Fatalf("%+v %v", ms, err)
	}
	if err := c.Load(ctx, "smolvlm"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, c, "smolvlm", "loading")
	m := waitStatus(t, c, "smolvlm", "loaded")
	if m.Port == 0 || m.Memory.Effective.NCtx != 4096 || len(m.Memory.Effective.Devices) != 1 {
		t.Errorf("%+v", m)
	}
	if err := c.Unload(ctx, "smolvlm"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, c, "smolvlm", "unloaded")
	var ue *llama.ErrUpstream
	if err := c.Load(ctx, "nope"); !asUpstream(err, &ue) || ue.Status != 400 || ue.Message != "model 'nope' not found" {
		t.Errorf("%v", err)
	}
}

func asUpstream(err error, target **llama.ErrUpstream) bool {
	var ue *llama.ErrUpstream
	ok := errors.As(err, &ue)
	*target = ue
	return ok
}

func TestModelsMaxEvicts(t *testing.T) {
	_, c, _ := newServer(t, func(o *Options) { o.ModelsMax = 1 })
	ctx := context.Background()
	_ = c.Load(ctx, "smolvlm")
	waitStatus(t, c, "smolvlm", "loaded")
	_ = c.Load(ctx, "other")
	waitStatus(t, c, "other", "loaded")
	waitStatus(t, c, "smolvlm", "unloaded")
}

func TestAutoloadOff(t *testing.T) {
	ts, c, _ := newServer(t, func(o *Options) { o.Autoload = false })
	resp, _ := post(t, ts.URL+"/v1/chat/completions", `{"model":"smolvlm","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 400 {
		t.Errorf("status %d", resp.StatusCode)
	}
	if _, err := c.Slots(context.Background(), "smolvlm"); err == nil {
		t.Error("slots of unloaded model should fail")
	}
}

func TestStreamAutoloadsAndShapes(t *testing.T) {
	ts, c, logBuf := newServer(t, nil)
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"smolvlm","stream":true,"messages":[{"role":"user","content":"hello there friend"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content type %q", ct)
	}
	var events, timings, done int
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Bytes()
		if bytes.HasPrefix(line, []byte("data: [DONE]")) {
			done++
		} else if bytes.HasPrefix(line, []byte("data:")) {
			events++
		}
		if tm, ok := llama.ScanSSELine(line); ok {
			timings++
			if tm.PredictedN != 8 || tm.PromptN != 3 {
				t.Errorf("%+v", tm)
			}
		}
	}
	if events != 8+2 || timings != 1 || done != 1 {
		t.Errorf("events %d timings %d done %d", events, timings, done)
	}
	slots, err := c.Slots(context.Background(), "smolvlm")
	if err != nil || len(slots) != 2 {
		t.Fatal(slots, err)
	}
	if s := slots[0]; s.IsProcessing || !s.HasTask || s.Tokens() != (llama.Tokens{Cached: 11, Total: 11}) {
		t.Errorf("%+v", s)
	}
	assertLogs(t, logBuf.String())
}

// assertLogs checks every emitted line parses as the real router format.
func assertLogs(t *testing.T, out string) {
	t.Helper()
	seen := map[string]bool{}
	for _, raw := range strings.Split(strings.TrimSpace(out), "\n") {
		var rec struct{ Type, Level, Msg string }
		if err := json.Unmarshal([]byte(raw), &rec); err != nil || rec.Type != "log" || !strings.HasSuffix(rec.Msg, "\n") {
			t.Fatalf("bad line %q", raw)
		}
		l := logs.Parser{}.Parse(raw, time.Now(), nil)
		if l.Port > 0 && rec.Level != "none" {
			t.Fatalf("child not level none: %q", raw)
		}
		for _, key := range []string{"spawning server instance", "model loaded", "launch_slot_", "print_timing", "stop processing", "proxying request"} {
			if strings.Contains(l.Message, key) {
				seen[key] = true
			}
		}
	}
	for _, key := range []string{"spawning server instance", "model loaded", "launch_slot_", "print_timing", "stop processing", "proxying request"} {
		if !seen[key] {
			t.Errorf("no %q line in logs", key)
		}
	}
}

func TestNonStreamAndCompletionsAndInfill(t *testing.T) {
	ts, _, _ := newServer(t, nil)
	_, b := post(t, ts.URL+"/v1/chat/completions", `{"model":"smolvlm","messages":[{"role":"user","content":[{"type":"text","text":"a b c"}]}]}`)
	var chat struct {
		Choices []struct {
			Message struct{ Content string }
		}
		Usage   map[string]any
		Timings llama.Timings
	}
	if err := json.Unmarshal(b, &chat); err != nil || len(chat.Choices) != 1 || chat.Choices[0].Message.Content == "" || chat.Timings.PredictedN != 8 || chat.Timings.PromptN != 3 {
		t.Fatalf("%s %v", b, err)
	}
	if tm, ok := llama.ParseTimings(b); !ok || tm.PredictedN != 8 {
		t.Error("ParseTimings")
	}
	_, b = post(t, ts.URL+"/v1/completions", `{"model":"smolvlm","prompt":"x y","max_tokens":3}`)
	if !strings.Contains(string(b), `"text_completion"`) || !strings.Contains(string(b), `"predicted_n":3`) {
		t.Errorf("%s", b)
	}
	_, b = post(t, ts.URL+"/infill", `{"model":"smolvlm","input_prefix":"a","input_suffix":"b"}`)
	if !strings.Contains(string(b), `"content"`) {
		t.Errorf("%s", b)
	}
	_, b = post(t, ts.URL+"/v1/embeddings", `{"model":"smolvlm","input":"one two"}`)
	if !strings.Contains(string(b), `"embedding"`) {
		t.Errorf("%s", b)
	}
}

func TestErrors(t *testing.T) {
	ts, _, _ := newServer(t, func(o *Options) { o.Ctx = 5 })
	resp, b := post(t, ts.URL+"/v1/chat/completions", `{"model":"nope","messages":[]}`)
	want, _ := os.ReadFile("../llama/testdata/err-unknown-model.txt")
	want = bytes.TrimSuffix(bytes.TrimSpace(want), []byte(" code=400"))
	if resp.StatusCode != 400 || strings.TrimSpace(string(b)) != string(want) {
		t.Errorf("%d %s", resp.StatusCode, b)
	}
	resp, b = post(t, ts.URL+"/v1/chat/completions", `{"model":"smolvlm","messages":[{"role":"user","content":"1 2 3 4 5 6 7"}]}`)
	if resp.StatusCode != 400 || !strings.Contains(string(b), "exceeds the available context size") || !strings.Contains(string(b), "exceed_context_size_error") {
		t.Errorf("%d %s", resp.StatusCode, b)
	}
}

func TestSlotsBusyAndCancel(t *testing.T) {
	ts, c, logBuf := newServer(t, func(o *Options) { o.TokenDelay = 10 * time.Millisecond })
	ctx := context.Background()
	_ = c.Load(ctx, "smolvlm")
	waitStatus(t, c, "smolvlm", "loaded")

	reqCtx, cancel := context.WithCancel(ctx)
	req, _ := http.NewRequestWithContext(reqCtx, "POST", ts.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"smolvlm","stream":true,"max_tokens":1000,"messages":[{"role":"user","content":"a b c d"}]}`))
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(resp.Body)
	_, _ = br.ReadString('\n')

	sample := func() llama.Slot {
		slots, err := c.Slots(ctx, "smolvlm")
		if err != nil || len(slots) != 2 {
			t.Fatal(slots, err)
		}
		return slots[0]
	}
	time.Sleep(40 * time.Millisecond)
	first := sample()
	time.Sleep(60 * time.Millisecond)
	second := sample()
	if !first.IsProcessing || second.NDecoded <= first.NDecoded || first.Tokens().Total != 4+first.NDecoded || first.Tokens().Prompt != 4 || first.Params["max_tokens"] != float64(1000) {
		t.Fatalf("first %+v second %+v", first, second)
	}
	metrics, _ := c.Metrics(ctx, "smolvlm")
	if metrics["requests_processing"] != 1 || metrics["tokens_predicted_total"] <= 0 {
		t.Errorf("%v", metrics)
	}

	cancel()
	resp.Body.Close()
	for range 100 {
		slots, _ := c.Slots(ctx, "smolvlm")
		if !slots[0].IsProcessing && !slots[1].IsProcessing {
			if !strings.Contains(logBuf.String(), "cancel task") {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("generation did not stop after disconnect")
}

func TestSlotActionsAndProps(t *testing.T) {
	_, c, _ := newServer(t, nil)
	ctx := context.Background()
	_ = c.Load(ctx, "smolvlm")
	waitStatus(t, c, "smolvlm", "loaded")
	if _, err := c.SlotAction(ctx, "smolvlm", 0, "save", "s0.bin"); err != nil {
		t.Fatal(err)
	}
	if r, err := c.SlotAction(ctx, "smolvlm", 0, "restore", "s0.bin"); err != nil || r["filename"] != "s0.bin" {
		t.Fatal(r, err)
	}
	if r, err := c.SlotAction(ctx, "smolvlm", 0, "erase", ""); err != nil || r["id_slot"] != float64(0) {
		t.Fatal(r, err)
	}
	var ue *llama.ErrUpstream
	if _, err := c.SlotAction(ctx, "smolvlm", 0, "save", "../evil"); !asUpstream(err, &ue) || ue.Status != 400 {
		t.Errorf("%v", err)
	}
	if _, err := c.SlotAction(ctx, "smolvlm", 9, "erase", ""); err == nil {
		t.Error("bad slot")
	}
	p, err := c.Props(ctx, "smolvlm")
	if err != nil || p["model_alias"] != "smolvlm" {
		t.Fatal(p, err)
	}
	if err := c.Health(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestMetricsDisabled(t *testing.T) {
	_, c, _ := newServer(t, func(o *Options) { o.Metrics = false })
	_ = c.Load(context.Background(), "smolvlm")
	waitStatus(t, c, "smolvlm", "loaded")
	if _, err := c.Metrics(context.Background(), "smolvlm"); err == nil {
		t.Error("want 501")
	}
}

func TestReloadRereadsThePreset(t *testing.T) {
	var preset string
	_, c, _ := newServer(t, func(o *Options) { preset = o.Preset })
	ctx := context.Background()
	if err := os.WriteFile(preset, []byte("[smolvlm]\nmodel = /m/a.gguf\n\n[added]\nmodel = /m/c.gguf\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ids := func(ms []llama.Model, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return strings.Join(out, ",")
	}
	if got := ids(c.Models(ctx)); got != "smolvlm,other" {
		t.Errorf("before reload: %s", got)
	}
	if got := ids(c.Reload(ctx)); got != "smolvlm,added" {
		t.Errorf("after reload: %s", got)
	}
}
