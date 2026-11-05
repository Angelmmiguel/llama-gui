package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/llama"
)

var fakeBin string

func TestMain(m *testing.M) {
	code := func() int {
		dir, err := os.MkdirTemp("", "fake-llama")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer func() { _ = os.RemoveAll(dir) }()
		fakeBin = filepath.Join(dir, "llama-server")
		out, err := exec.Command("go", "build", "-o", fakeBin, "github.com/Angelmmiguel/llama-gui/cmd/fake-llama-server").CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "build fake: %v\n%s", err, out)
			return 1
		}
		return m.Run()
	}()
	os.Exit(code)
}

func TestBackoff(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{{0, time.Second}, {1, 2 * time.Second}, {2, 4 * time.Second}, {5, 32 * time.Second}, {6, 60 * time.Second}, {500, 60 * time.Second}}
	for _, c := range cases {
		if got := backoff(c.attempt, time.Second, time.Minute); got != c.want {
			t.Errorf("backoff(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
}

func TestIdleTracker(t *testing.T) {
	t0 := time.Unix(1000, 0)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	limit := 60 * time.Second
	var tr idleTracker
	one := func(inFlight int) []candidate { return []candidate{{model: "a", limit: limit, inFlight: inFlight}} }

	if got := tr.due(at(0), one(0)); got != nil {
		t.Fatalf("freshly loaded: %v", got)
	}
	if got := tr.due(at(59), one(0)); got != nil {
		t.Fatalf("before limit: %v", got)
	}
	tr.touch("a", at(30))
	if got := tr.due(at(89), one(0)); got != nil {
		t.Fatalf("touch must push it out: %v", got)
	}
	if got := tr.due(at(90), one(1)); got != nil {
		t.Fatalf("in flight: %v", got)
	}
	if got := tr.due(at(149), one(0)); got != nil {
		t.Fatalf("in flight counts as activity: %v", got)
	}
	if got := tr.due(at(150), one(0)); len(got) != 1 || got[0] != "a" {
		t.Fatalf("due: %v", got)
	}
	if got := tr.due(at(151), one(0)); got != nil {
		t.Fatalf("no hammering while the unload lands: %v", got)
	}
	// Gone, then back much later without any Touch.
	tr.due(at(152), nil)
	if got := tr.due(at(10000), one(0)); got != nil {
		t.Fatalf("reloaded model unloaded on sight: %v", got)
	}
	if got := tr.due(at(20000), []candidate{{model: "a"}}); got != nil {
		t.Fatalf("no policy: %v", got)
	}
}

func TestScanLines(t *testing.T) {
	var got []string
	long := strings.Repeat("x", 200000)
	in := "one\r\ntwo\n" + long + "\nfour"
	scanLines(strings.NewReader(in), 1000, func(s string) { got = append(got, s) })
	if len(got) != 4 || got[0] != "one" || got[1] != "two" || len(got[2]) != 1000 || got[3] != "four" {
		t.Fatalf("lines: %d %q", len(got), got[:2])
	}
}

func TestArgs(t *testing.T) {
	s := New(Options{PresetPath: "/d/p.ini", DataDir: "/d", ModelsMax: 2, MemoryMargin: 512, ExtraArgs: []string{"--foo"}})
	got := strings.Join(s.args(99), " ")
	want := "--host 127.0.0.1 --port 99 --models-preset /d/p.ini --models-max 2 --models-memory-margin 512 " +
		"--no-models-autoload --metrics --slot-save-path /d/slots --log-jsonl -lv 3 --foo"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func preset(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "preset.ini")
	if err := os.WriteFile(path, []byte("version = 1\n\n[a]\nmodel = /m/a.gguf\n\n[b]\nmodel = /m/b.gguf\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func options(t *testing.T) Options {
	t.Helper()
	dir := t.TempDir()
	return Options{
		Logger:        slog.New(slog.DiscardHandler),
		Binary:        func() (string, error) { return fakeBin, nil },
		PresetPath:    preset(t, dir),
		DataDir:       dir,
		Env:           []string{"FAKE_LLAMA_LOAD_MS=1"},
		BackoffMin:    20 * time.Millisecond,
		BackoffMax:    100 * time.Millisecond,
		StopTimeout:   5 * time.Second,
		PollInterval:  20 * time.Millisecond,
		HealthTimeout: 10 * time.Second,
	}
}

func stopOnCleanup(t *testing.T, s *Supervisor) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Stop(ctx); err != nil {
			t.Errorf("cleanup stop: %v", err)
		}
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func gone(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	// A zombie waiting for init counts: it runs nothing and holds nothing.
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	rest := string(data[strings.LastIndexByte(string(data), ')')+1:])
	return strings.HasPrefix(strings.TrimSpace(rest), "Z")
}

func modelStatus(t *testing.T, s *Supervisor, id string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	models, err := llama.New(s.State().BaseURL, nil).Models(ctx)
	if err != nil {
		return ""
	}
	for _, m := range models {
		if m.ID == id {
			return m.Status
		}
	}
	return ""
}

func TestStartStop(t *testing.T) {
	ctx := context.Background()
	opts := options(t)
	var lines atomic.Int64
	opts.OnLine = func(raw string, at time.Time) {
		if raw != "" && !at.IsZero() {
			lines.Add(1)
		}
	}
	s := New(opts)
	if st := s.State(); st.State != Stopped {
		t.Fatalf("initial %v", st)
	}
	if _, ok := s.Upstream(); ok {
		t.Fatal("upstream before start")
	}
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	st := s.State()
	if st.State != Running || st.PID == 0 || st.BaseURL == "" || st.Build != fakeBin {
		t.Fatalf("running: %+v", st)
	}
	u, ok := s.Upstream()
	if !ok || u.String() != st.BaseURL {
		t.Fatalf("upstream %v %v", u, ok)
	}
	if err := llama.New(st.BaseURL, nil).Health(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if got := s.State().PID; got != st.PID {
		t.Fatalf("second start spawned: %d", got)
	}
	waitFor(t, "log lines", func() bool { return lines.Load() > 0 })
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.State(); got.State != Stopped || got.PID != 0 || got.BaseURL != "" || got.LastError != "" {
		t.Fatalf("stopped: %+v", got)
	}
	if _, ok := s.Upstream(); ok {
		t.Fatal("upstream after stop")
	}
	waitFor(t, "process gone", func() bool { return gone(st.PID) })
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(opts.DataDir, "slots")); err != nil {
		t.Fatal(err)
	}
}

func TestNoBuild(t *testing.T) {
	var calls atomic.Int64
	opts := options(t)
	opts.Binary = func() (string, error) {
		calls.Add(1)
		return "", errors.New("nothing installed")
	}
	s := New(opts)
	err := s.Start(context.Background())
	if !errors.Is(err, ErrNoBuild) {
		t.Fatalf("Start = %v", err)
	}
	st := s.State()
	if st.State != Stopped || !strings.Contains(st.LastError, "nothing installed") {
		t.Fatalf("state %+v", st)
	}
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("retried %d times", calls.Load())
	}
	s.opts.Binary = func() (string, error) { return fakeBin, nil }
	stopOnCleanup(t, s)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start after install: %v", err)
	}
	if st := s.State(); st.State != Running || st.LastError != "" {
		t.Fatalf("state %+v", st)
	}
}

func TestCrashRestartAndReload(t *testing.T) {
	ctx := context.Background()
	opts := options(t)
	opts.Env = append(opts.Env, "FAKE_LLAMA_CRASH_AFTER_MS=500")
	s := New(opts)
	stopOnCleanup(t, s)
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	first := s.State().PID
	if err := llama.New(s.State().BaseURL, nil).Load(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a loaded and remembered", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.remembered) == 1 && s.remembered[0] == "a"
	})
	waitFor(t, "restart", func() bool {
		st := s.State()
		return st.RestartCount >= 1 && st.State == Running && st.PID != first
	})
	if !gone(first) {
		t.Fatal("old process alive")
	}
	if s.State().LastError == "" {
		t.Fatal("no crash reason kept")
	}
	waitFor(t, "a reloaded", func() bool { return modelStatus(t, s, "a") == "loaded" })

	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	count := s.State().RestartCount
	time.Sleep(200 * time.Millisecond)
	st := s.State()
	if st.State != Stopped || st.RestartCount != count {
		t.Fatalf("restarted after Stop: %+v", st)
	}
}

func TestStopDuringBackoff(t *testing.T) {
	opts := options(t)
	opts.Env = append(opts.Env, "FAKE_LLAMA_CRASH_AFTER_MS=1")
	opts.BackoffMin, opts.BackoffMax = time.Hour, time.Hour
	s := New(opts)
	_ = s.Start(context.Background())
	waitFor(t, "crash", func() bool { return s.State().State == Crashed })
	done := make(chan error, 1)
	go func() { done <- s.Stop(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop blocked on the backoff")
	}
	if st := s.State(); st.State != Stopped {
		t.Fatalf("state %+v", st)
	}
}

func TestStableRunResetsBackoff(t *testing.T) {
	// A clock that jumps makes every run look long, so the delay never grows past
	// the minimum; with a stuck clock the same setup would reach the hour-long maximum.
	opts := options(t)
	opts.Env = append(opts.Env, "FAKE_LLAMA_CRASH_AFTER_MS=1")
	opts.BackoffMin, opts.BackoffMax, opts.StableAfter = 10*time.Millisecond, time.Hour, time.Second
	var n atomic.Int64
	opts.Clock = func() time.Time { return time.Unix(n.Add(1)*10, 0) }
	s := New(opts)
	stopOnCleanup(t, s)
	_ = s.Start(context.Background())
	waitFor(t, "many restarts", func() bool { return s.State().RestartCount >= 8 })
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "llama-server")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	var pid int
	waitFor(t, "pid file", func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil && pid > 0
	})
	return pid
}

func TestNoOrphanChild(t *testing.T) {
	cases := []struct {
		name string
		env  string
		end  func(t *testing.T, s *Supervisor)
	}{
		{"stop", "", func(t *testing.T, s *Supervisor) {
			if err := s.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
		}},
		{"crash", "FAKE_LLAMA_CRASH_AFTER_MS=200", func(t *testing.T, s *Supervisor) {
			waitFor(t, "crash", func() bool { return s.State().RestartCount >= 1 })
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := options(t)
			pidFile := filepath.Join(t.TempDir(), "child")
			// The child stands in for a model server, and ignores SIGTERM as a slow one would.
			script := writeScript(t, fmt.Sprintf(`(trap '' TERM; exec sleep 300) & echo $! > %s
exec %s "$@"`, pidFile, fakeBin))
			opts.Binary = func() (string, error) { return script, nil }
			if c.env != "" {
				opts.Env = append(opts.Env, c.env)
			}
			s := New(opts)
			stopOnCleanup(t, s)
			if err := s.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			child := readPID(t, pidFile)
			if gone(child) {
				t.Fatal("child should be running")
			}
			c.end(t, s)
			waitFor(t, "child gone", func() bool { return gone(child) })
		})
	}
}

func TestStopEscalatesToSIGKILL(t *testing.T) {
	opts := options(t)
	script := writeScript(t, fmt.Sprintf(`trap '' TERM
%s "$@" &
while :; do sleep 1; done`, fakeBin))
	opts.Binary = func() (string, error) { return script, nil }
	opts.StopTimeout = 200 * time.Millisecond
	s := New(opts)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	pid := s.State().PID
	begin := time.Now()
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(begin); d < 200*time.Millisecond {
		t.Fatalf("killed without waiting: %v", d)
	}
	waitFor(t, "gone", func() bool { return gone(pid) })
}

func TestStopContextEscalates(t *testing.T) {
	opts := options(t)
	script := writeScript(t, fmt.Sprintf(`trap '' TERM
%s "$@" &
while :; do sleep 1; done`, fakeBin))
	opts.Binary = func() (string, error) { return script, nil }
	opts.StopTimeout = time.Hour
	s := New(opts)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	pid := s.State().PID
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := s.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop = %v", err)
	}
	if st := s.State(); st.State != Stopped {
		t.Fatalf("state %+v", st)
	}
	waitFor(t, "gone", func() bool { return gone(pid) })
}

func TestHealthDeadline(t *testing.T) {
	opts := options(t)
	script := writeScript(t, "exec sleep 300")
	opts.Binary = func() (string, error) { return script, nil }
	opts.HealthTimeout = 200 * time.Millisecond
	s := New(opts)
	stopOnCleanup(t, s)
	err := s.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not healthy") {
		t.Fatalf("Start = %v", err)
	}
	waitFor(t, "retry", func() bool { return s.State().RestartCount >= 1 })
}

func TestStopWhileStarting(t *testing.T) {
	opts := options(t)
	script := writeScript(t, "exec sleep 300")
	opts.Binary = func() (string, error) { return script, nil }
	s := New(opts)
	started := make(chan error, 1)
	go func() { started <- s.Start(context.Background()) }()
	waitFor(t, "starting", func() bool { st := s.State(); return st.State == Starting && st.PID != 0 })
	pid := s.State().PID
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-started; !errors.Is(err, ErrStopped) {
		t.Fatalf("Start = %v", err)
	}
	waitFor(t, "gone", func() bool { return gone(pid) })
}

func TestRestartSwitchesBuildAndReloads(t *testing.T) {
	ctx := context.Background()
	opts := options(t)
	dir := t.TempDir()
	second := filepath.Join(dir, "llama-server")
	if err := os.Symlink(fakeBin, second); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	current := fakeBin
	opts.Binary = func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		return current, nil
	}
	opts.PollInterval = time.Hour // only Restart's own query can remember the model
	s := New(opts)
	stopOnCleanup(t, s)
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := llama.New(s.State().BaseURL, nil).Load(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "b loaded", func() bool { return modelStatus(t, s, "b") == "loaded" })

	mu.Lock()
	current = second
	mu.Unlock()
	old := s.State().PID
	if err := s.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	st := s.State()
	if st.PID == old || st.Build != second || st.State != Running {
		t.Fatalf("after restart: %+v", st)
	}
	if st.RestartCount != 0 {
		t.Fatalf("deliberate restart counted as crash: %d", st.RestartCount)
	}
	waitFor(t, "b reloaded", func() bool { return modelStatus(t, s, "b") == "loaded" })
	if got := modelStatus(t, s, "a"); got != "unloaded" {
		t.Fatalf("a = %q", got)
	}
}

func TestStopForgetsLoaded(t *testing.T) {
	ctx := context.Background()
	s := New(options(t))
	stopOnCleanup(t, s)
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := llama.New(s.State().BaseURL, nil).Load(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "remembered", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.remembered) == 1
	})
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := modelStatus(t, s, "a"); got != "unloaded" {
		t.Fatalf("a = %q after Stop then Start", got)
	}
}

func TestIdleUnload(t *testing.T) {
	ctx := context.Background()
	opts := options(t)
	var inFlight atomic.Int64
	opts.IdlePolicy = func(model string) (time.Duration, bool) { return 50 * time.Millisecond, model == "a" }
	opts.InFlight = func(string) int { return int(inFlight.Load()) }
	inFlight.Store(1)
	s := New(opts)
	stopOnCleanup(t, s)
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	client := llama.New(s.State().BaseURL, nil)
	for _, m := range []string{"a", "b"} {
		if err := client.Load(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "both loaded", func() bool { return modelStatus(t, s, "a") == "loaded" && modelStatus(t, s, "b") == "loaded" })
	time.Sleep(200 * time.Millisecond)
	if got := modelStatus(t, s, "a"); got != "loaded" {
		t.Fatalf("unloaded with a request in flight: %q", got)
	}
	inFlight.Store(0)
	waitFor(t, "a unloaded", func() bool { return modelStatus(t, s, "a") == "unloaded" })
	if got := modelStatus(t, s, "b"); got != "loaded" {
		t.Fatalf("b has no policy: %q", got)
	}
}

func TestTouchDelaysUnload(t *testing.T) {
	ctx := context.Background()
	opts := options(t)
	opts.IdlePolicy = func(string) (time.Duration, bool) { return 300 * time.Millisecond, true }
	s := New(opts)
	stopOnCleanup(t, s)
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := llama.New(s.State().BaseURL, nil).Load(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "loaded", func() bool { return modelStatus(t, s, "a") == "loaded" })
	for range 12 {
		s.Touch("a")
		time.Sleep(50 * time.Millisecond)
	}
	if got := modelStatus(t, s, "a"); got != "loaded" {
		t.Fatalf("a = %q while touched", got)
	}
	waitFor(t, "unloaded", func() bool { return modelStatus(t, s, "a") == "unloaded" })
}
