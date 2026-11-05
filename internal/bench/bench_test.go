package bench

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/store"
)

func capture(t *testing.T) []Result {
	t.Helper()
	f, err := os.Open("testdata/llama-bench.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	res, err := ParseOutput(f)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestParseCapture(t *testing.T) {
	res := capture(t)
	if len(res) != 16 {
		t.Fatalf("parsed %d results, want 16", len(res))
	}
	first := res[0]
	if first.Test != "pp16" || first.Kind != "pp" || first.FlashAttn != "auto" || first.CacheK != "f16" ||
		first.Backend != "CPU" || first.NGL != "-1" || first.TokensPerSec < 538 || first.TokensPerSec > 539 {
		t.Errorf("first = %+v", first)
	}
	if got := res[3]; got.Test != "tg8 @ d32" || got.Depth != 32 {
		t.Errorf("res[3] = %+v", got)
	}
}

func TestParseLineTolerance(t *testing.T) {
	for _, line := range []string{"", "load_backend: loaded CPU", "{not json", `{"build_number": 1}`} {
		if _, ok, _ := ParseLine(line); ok {
			t.Errorf("%q parsed", line)
		}
	}
	r, ok, err := ParseLine(`{"n_gen": 4, "flash_attn": true, "type_k":"f16","type_v":"f16","avg_ts": 3.5, "future": {"x": 1}}`)
	if err != nil || !ok || r.FlashAttn != "on" || r.Test != "tg4" || r.TokensPerSec != 3.5 {
		t.Errorf("got %+v %v %v", r, ok, err)
	}
	if _, _, err := ParseLine("{not json"); err == nil {
		t.Error("broken JSON should be an error the caller can skip")
	}
}

func TestHelpSupport(t *testing.T) {
	help, _ := os.ReadFile("testdata/help.txt")
	sup := ParseHelp(string(help))
	for _, f := range []string{"-d", "-fa", "-ctk", "-ctv", "-ngl", "-r"} {
		if !sup(f) {
			t.Errorf("%s unsupported", f)
		}
	}
	if sup("-zz") || ParseHelp("  --delay <n>")("-d") {
		t.Error("matched a flag that is not there")
	}
}

func TestSetupValidate(t *testing.T) {
	ok := Setup{ModelFile: "a/m.gguf", FlashAttn: []string{"on", "auto"}, CacheTypes: []string{"f16", "q8_0"}, Depths: []int{0, 4096}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Setup){
		"escape":    func(s *Setup) { s.ModelFile = "../m.gguf" },
		"absolute":  func(s *Setup) { s.ModelFile = "/etc/m.gguf" },
		"flag-like": func(s *Setup) { s.ModelFile = "-x.gguf" },
		"extension": func(s *Setup) { s.ModelFile = "m.bin" },
		"fa":        func(s *Setup) { s.FlashAttn = []string{"1; rm"} },
		"cache":     func(s *Setup) { s.CacheTypes = []string{"f16,q4"} },
		"depth":     func(s *Setup) { s.Depths = []int{-1} },
		"reps":      func(s *Setup) { s.Repetitions = 1000 },
		"ngl":       func(s *Setup) { s.NGL = "99;" },
		"too many": func(s *Setup) {
			s.CacheTypes = []string{"f32", "f16", "bf16", "q8_0", "q4_0"}
			s.FlashAttn = []string{"on", "off", "auto"}
		},
	}
	for name, mutate := range cases {
		s := ok
		mutate(&s)
		if err := s.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestArgsCommandEstimate(t *testing.T) {
	s := Setup{ModelFile: "m.gguf", FlashAttn: []string{"on"}, CacheTypes: []string{"f16", "q8_0"}, Depths: []int{0, 4096}, Repetitions: 5, NGL: "99"}
	args, err := s.Args("/models/m.gguf", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "-m /models/m.gguf -p 512 -n 128 -r 5 -ngl 99 -fa on -ctk f16,q8_0 -ctv f16,q8_0 -d 0,4096 -o jsonl"
	if got := strings.Join(args, " "); got != want {
		t.Errorf("args = %s", got)
	}
	// cross product of K and V: 1 fa * 2 * 2 * 2 depths * (pp, tg)
	if e := s.Estimate(); e.Tests != 16 || e.Seconds != 192 {
		t.Errorf("estimate = %+v", e)
	}
	cmd := s.Command()
	if !strings.HasPrefix(cmd, "llama-bench -m m.gguf \\\n  -p 512") || !strings.HasSuffix(cmd, "-o jsonl") {
		t.Errorf("command = %q", cmd)
	}

	noD := func(f string) bool { return f != "-d" }
	if _, err := s.Args("m", noD); !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "-d") {
		t.Errorf("depth on a build without -d: %v", err)
	}
	s.Depths = nil
	if a, err := s.Args("m", noD); err != nil || strings.Contains(strings.Join(a, " "), "-d ") {
		t.Errorf("depth 0 should omit -d: %v %v", a, err)
	}
}

func TestSeriesAndTakeaway(t *testing.T) {
	run := Run{Results: capture(t)}
	c := Series(run)
	if len(c.Decode) != 4 || len(c.Prefill) != 4 {
		t.Fatalf("lines = %d decode, %d prefill", len(c.Decode), len(c.Prefill))
	}
	l := c.Decode[1]
	if l.Label != "fa auto · k f16 v q8_0" || len(l.Points) != 2 || l.Points[0].Depth != 0 || l.Points[1].Depth != 32 {
		t.Errorf("line = %+v", l)
	}
	if c.Decode[0].Label != "fa auto · f16" {
		t.Errorf("label = %q", c.Decode[0].Label)
	}

	got := Takeaway(run)
	if !strings.HasPrefix(got, "Decoding at 32 tokens of context, fa auto · ") || !strings.Contains(got, "faster than") {
		t.Errorf("takeaway = %q", got)
	}
	if strings.Contains(strings.ToLower(got), "memory") || strings.Contains(strings.ToLower(got), "vram") {
		t.Errorf("claims memory: %q", got)
	}

	one := Run{Results: run.Results[:4]}
	if got := Takeaway(one); got != "" {
		t.Errorf("single config takeaway = %q", got)
	}
	flat := Run{Results: []Result{
		{Kind: "tg", FlashAttn: "on", CacheK: "f16", CacheV: "f16", TokensPerSec: 100},
		{Kind: "tg", FlashAttn: "off", CacheK: "f16", CacheV: "f16", TokensPerSec: 100.5},
	}}
	if got := Takeaway(flat); !strings.Contains(got, "within 1%") || !strings.Contains(got, "empty context") {
		t.Errorf("flat takeaway = %q", got)
	}
}

func TestMarkdown(t *testing.T) {
	md := Markdown(Run{Build: "b1", Setup: Setup{ModelFile: "m.gguf"}, Results: capture(t)[:2]})
	lines := strings.Split(strings.TrimSpace(md), "\n")
	if len(lines) != 6 || lines[0] != "`m.gguf` · build `b1`" || !strings.Contains(lines[4], "| CPU | auto | f16 / f16 | pp16 | 538.18 ± 0.00 |") {
		t.Errorf("markdown:\n%s", md)
	}
}

func TestHistory(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	mk := func(id int64, build string, at time.Duration, tps ...float64) Run {
		r := Run{ID: id, Build: build, State: StateDone, StartedAt: t0.Add(at), Setup: Setup{ModelFile: "m.gguf"}}
		for _, v := range tps {
			r.Results = append(r.Results, Result{Kind: "tg", N: 128, TokensPerSec: v})
		}
		r.Results = append(r.Results, Result{Kind: "tg", N: 128, Depth: 4096, TokensPerSec: 999})
		return r
	}
	runs := []Run{
		mk(3, "b2", 2*time.Hour, 110),
		mk(1, "b1", 0, 100, 90),
		mk(2, "b1", time.Hour, 105),
		{ID: 4, Build: "b3", State: StateFailed, Setup: Setup{ModelFile: "m.gguf"}},
		mk(5, "bx", 3*time.Hour, 500),
	}
	runs[4].Setup.ModelFile = "other.gguf"
	h := History(runs, "m.gguf")
	if len(h) != 2 || h[0].Build != "b1" || h[0].TokensPerSec != 105 || h[0].DeltaPercent != nil || h[0].RunID != 2 {
		t.Fatalf("history = %+v", h)
	}
	if h[1].Build != "b2" || h[1].DeltaPercent == nil || *h[1].DeltaPercent < 4.7 || *h[1].DeltaPercent > 4.8 {
		t.Errorf("second = %+v", h[1])
	}
}

// --- runner ---

const fakeScript = `#!/bin/sh
case "$*" in
  *--help*) cat %[1]s/help.txt; exit 0 ;;
esac
echo "$@" > %[2]s/argv
echo "load_backend: loaded CPU backend" >&2
case "$FAKE_MODE" in
  hang) echo '{"n_prompt":16,"n_gen":0,"type_k":"f16","type_v":"f16","avg_ts":1}'; sleep 30 & wait ;;
  fail) echo "boom" >&2; exit 3 ;;
esac
echo "garbage line"
[ -n "$FAKE_BROKEN" ] && echo "{broken"
cat %[1]s/llama-bench.jsonl
`

type env struct {
	r       *Runner
	store   *store.Store
	dir     string
	paused  atomic.Int32
	resumed atomic.Int32
}

func newEnv(t *testing.T, mode string, pause func(context.Context) (func(), error)) *env {
	t.Helper()
	dir := t.TempDir()
	wd, _ := os.Getwd()
	bin := filepath.Join(dir, "llama-bench")
	script := strings.NewReplacer("%[1]s", filepath.Join(wd, "testdata"), "%[2]s", dir).Replace(fakeScript)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	models := filepath.Join(dir, "models")
	_ = os.MkdirAll(models, 0o755)
	_ = os.WriteFile(filepath.Join(models, "m.gguf"), []byte("x"), 0o644)
	t.Setenv("FAKE_MODE", mode)
	st, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &env{store: st, dir: dir}
	if pause == nil {
		pause = func(context.Context) (func(), error) {
			e.paused.Add(1)
			return func() { e.resumed.Add(1) }, nil
		}
	}
	e.r = NewRunner(Options{
		Binary: func() (string, string, error) { return bin, "b12132", nil },
		Models: models, Pause: pause, Store: st,
	})
	return e
}

func wait(t *testing.T, r *Runner) Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := r.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	run, _ := r.Current()
	return run
}

var smallSetup = Setup{ModelFile: "m.gguf", FlashAttn: []string{"auto"}, CacheTypes: []string{"f16", "q8_0"}, Depths: []int{0, 32}, Repetitions: 1, NPrompt: 16, NGen: 8}

func TestRunnerHappyPath(t *testing.T) {
	e := newEnv(t, "", nil)
	run, err := e.r.Start(context.Background(), smallSetup)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != StateRunning || run.Total != 16 || run.ID == 0 || run.Build != "b12132" {
		t.Errorf("started = %+v", run)
	}
	run = wait(t, e.r)
	if run.State != StateDone || len(run.Results) != 16 || run.Done != 16 || run.Error != "" {
		t.Fatalf("finished = %+v", run)
	}
	if e.paused.Load() != 1 || e.resumed.Load() != 1 {
		t.Errorf("paused %d resumed %d", e.paused.Load(), e.resumed.Load())
	}
	argv, _ := os.ReadFile(filepath.Join(e.dir, "argv"))
	if !strings.Contains(string(argv), "-d 0,32 -o jsonl") || !strings.Contains(string(argv), filepath.Join("models", "m.gguf")) {
		t.Errorf("argv = %s", argv)
	}

	got, err := e.r.Get(context.Background(), run.ID)
	if err != nil || got.State != StateDone || len(got.Results) != 16 || got.Setup.ModelFile != "m.gguf" || got.FinishedAt.IsZero() {
		t.Errorf("stored = %+v %v", got, err)
	}
	if list, _ := e.r.List(context.Background(), "m.gguf", 5); len(list) != 1 {
		t.Errorf("list = %d", len(list))
	}
	if _, err := e.r.Start(context.Background(), smallSetup); err != nil {
		t.Errorf("second run after the first finished: %v", err)
	}
	wait(t, e.r)
}

func TestRunnerBusyAndCancel(t *testing.T) {
	e := newEnv(t, "hang", nil)
	if _, err := e.r.Start(context.Background(), smallSetup); err != nil {
		t.Fatal(err)
	}
	if _, err := e.r.Start(context.Background(), smallSetup); !errors.Is(err, ErrBusy) {
		t.Errorf("second Start = %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if run, _ := e.r.Current(); run.Done == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no progress")
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	if err := e.r.Cancel(); err != nil {
		t.Fatal(err)
	}
	run := wait(t, e.r)
	if run.State != StateCanceled || time.Since(start) > 5*time.Second {
		t.Errorf("run = %+v after %v", run, time.Since(start))
	}
	if e.resumed.Load() != 1 {
		t.Errorf("resumed %d", e.resumed.Load())
	}
	if got, _ := e.r.Get(context.Background(), run.ID); got.State != StateCanceled || len(got.Results) != 1 {
		t.Errorf("stored = %+v", got)
	}
	if err := e.r.Cancel(); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Cancel when idle = %v", err)
	}
}

func TestRunnerFailure(t *testing.T) {
	e := newEnv(t, "fail", nil)
	if _, err := e.r.Start(context.Background(), smallSetup); err != nil {
		t.Fatal(err)
	}
	run := wait(t, e.r)
	if run.State != StateFailed || !strings.Contains(run.Error, "boom") || e.resumed.Load() != 1 {
		t.Errorf("run = %+v, resumed %d", run, e.resumed.Load())
	}
}

func TestRunnerPauseFailureAndPanic(t *testing.T) {
	e := newEnv(t, "", func(context.Context) (func(), error) { return nil, errors.New("router stuck") })
	if _, err := e.r.Start(context.Background(), smallSetup); err != nil {
		t.Fatal(err)
	}
	if run := wait(t, e.r); run.State != StateFailed || !strings.Contains(run.Error, "router stuck") {
		t.Errorf("run = %+v", run)
	}

}

type panicHandler struct{ slog.Handler }

func (panicHandler) Enabled(context.Context, slog.Level) bool { return true }
func (panicHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		panic("logger exploded")
	}
	return nil
}

func TestRunnerResumesAfterPanic(t *testing.T) {
	e := newEnv(t, "", nil)
	t.Setenv("FAKE_BROKEN", "1")
	e.r.opt.Logger = slog.New(panicHandler{})
	if _, err := e.r.Start(context.Background(), smallSetup); err != nil {
		t.Fatal(err)
	}
	run := wait(t, e.r)
	if run.State != StateFailed || !strings.Contains(run.Error, "panic") || e.resumed.Load() != 1 {
		t.Errorf("run = %+v, resumed %d", run, e.resumed.Load())
	}
}

func TestRunnerRejects(t *testing.T) {
	e := newEnv(t, "", nil)
	bad := smallSetup
	bad.ModelFile = "missing.gguf"
	if _, err := e.r.Start(context.Background(), bad); !errors.Is(err, ErrModel) {
		t.Errorf("missing model = %v", err)
	}
	outside := filepath.Join(e.dir, "secret.gguf")
	_ = os.WriteFile(outside, []byte("x"), 0o644)
	_ = os.Symlink(outside, filepath.Join(e.dir, "models", "link.gguf"))
	bad.ModelFile = "link.gguf"
	if _, err := e.r.Start(context.Background(), bad); !errors.Is(err, ErrModel) {
		t.Errorf("symlink out = %v", err)
	}
	bad.ModelFile = "../x.gguf"
	if _, err := e.r.Start(context.Background(), bad); !errors.Is(err, ErrInvalid) {
		t.Errorf("traversal = %v", err)
	}
	if _, ok := e.r.Current(); ok {
		t.Error("rejected starts left a current run")
	}
}
