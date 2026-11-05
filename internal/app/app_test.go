package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/config"
	"github.com/Angelmmiguel/llama-gui/internal/library"
	"github.com/Angelmmiguel/llama-gui/internal/store"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	return config.Config{
		Addr:         "127.0.0.1:0",
		Data:         filepath.Join(dir, "nested", "data"),
		Builds:       filepath.Join(dir, "builds"),
		Models:       filepath.Join(dir, "models"),
		LlamaVersion: "none",
		LlamaBackend: "auto",
		LlamaUpdates: "off",
		ModelsMax:    4,
		Capture:      "meta",
		Retention:    time.Hour,
		Version:      "test",
	}
}

func listen(t *testing.T, addr string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func get(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

func TestRunServesAndStops(t *testing.T) {
	cfg := testConfig(t)
	ln := listen(t, cfg.Addr)
	base := "http://" + ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, quiet(), ln) }()

	if code, body := get(t, base+"/api/v1/healthz"); code != 200 || body["status"] != "ok" {
		t.Errorf("healthz: %d %v", code, body)
	}
	if code, body := get(t, base+"/api/v1/status"); code != 200 || body["version"] != "test" ||
		body["capture"] != "meta" || body["state"] != "stopped" {
		t.Errorf("status: %d %v", code, body)
	}
	for _, path := range []string{"/slots", "/metrics"} {
		code, body := get(t, base+path)
		e, _ := body["error"].(map[string]any)
		if code != 503 || e["message"] != "llama-server is not running" {
			t.Errorf("%s: %d %v", path, code, body)
		}
	}
	waitFor(t, "the missing build to show", func() bool {
		_, body := get(t, base+"/api/v1/status")
		return strings.Contains(fmt.Sprint(body["last_error"]), "no llama-server build")
	})
	if _, err := os.Stat(filepath.Join(cfg.Data, DBFile)); err != nil {
		t.Errorf("database: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run: %v", err)
		}
	case <-time.After(shutdownTimeout + 5*time.Second):
		t.Fatal("run did not stop")
	}
}

func TestRunReportsABusyPort(t *testing.T) {
	ln := listen(t, "127.0.0.1:0")
	defer func() { _ = ln.Close() }()
	cfg := testConfig(t)
	cfg.Addr = ln.Addr().String()
	if err := Run(context.Background(), cfg, quiet()); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Errorf("err = %v", err)
	}
}

func TestRunReportsAnUnusableDataDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	cfg.Data = filepath.Join(file, "data")
	if err := run(context.Background(), cfg, quiet(), listen(t, cfg.Addr)); err == nil {
		t.Error("want an error")
	}
}

func TestServeReportsAListenerFailure(t *testing.T) {
	ln := listen(t, "127.0.0.1:0")
	_ = ln.Close()
	err := serve(context.Background(), ln, http.NotFoundHandler(), time.Second, quiet())
	if err == nil {
		t.Error("want an error")
	}
}

// A stream that outlives the drain is closed rather than failing the shutdown,
// and does not hold the process past the deadline.
func TestServeGivesUpOnStreams(t *testing.T) {
	ln := listen(t, "127.0.0.1:0")
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.(http.Flusher).Flush()
		close(started)
		<-release
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, ln, handler, 20*time.Millisecond, quiet()) }()

	resp, err := http.Get("http://" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve: %v", err)
		}
	case <-time.After(shutdownTimeout + 5*time.Second):
		t.Fatal("serve did not stop")
	}
}

func TestLoopbackWarning(t *testing.T) {
	for _, tc := range []struct {
		addr string
		warn bool
	}{
		{"127.0.0.1:0", false},
		{"[::1]:0", false},
		{":0", true},
		{"0.0.0.0:0", true},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			ln, err := net.Listen("tcp", tc.addr)
			if err != nil {
				t.Skip(err)
			}
			var logs strings.Builder
			log := slog.New(slog.NewTextHandler(&logs, nil))
			cfg := testConfig(t)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := run(ctx, cfg, log, ln); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(logs.String(), "no authentication"); got != tc.warn {
				t.Errorf("warned = %v, want %v:\n%s", got, tc.warn, logs.String())
			}
		})
	}
}

func TestPruneLoop(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	insert := func(ts time.Time, raw string) {
		t.Helper()
		if err := st.InsertLogLines(ctx, []store.LogLine{{TS: ts, Raw: raw}}); err != nil {
			t.Fatal(err)
		}
	}
	remaining := func() []store.LogLine {
		t.Helper()
		lines, _, err := st.ListLogLines(ctx, store.LogFilter{})
		if err != nil {
			t.Fatal(err)
		}
		return lines
	}
	waitFor := func(n int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for len(remaining()) != n {
			if time.Now().After(deadline) {
				t.Fatalf("lines = %+v, want %d", remaining(), n)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	insert(time.Now().Add(-2*time.Hour), "old at startup")
	insert(time.Now(), "fresh")
	done := make(chan struct{})
	go func() {
		pruneLoop(ctx, st, time.Hour, 10*time.Millisecond, quiet())
		close(done)
	}()
	waitFor(1)

	insert(time.Now().Add(-2*time.Hour), "old on a tick")
	waitFor(1)
	if got := remaining()[0].Raw; got != "fresh" {
		t.Errorf("kept %q", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pruneLoop did not stop")
	}
}

func TestIsLoopbackRejectsOtherAddresses(t *testing.T) {
	if isLoopback(&net.UnixAddr{Name: "x"}) {
		t.Error("a non-TCP address is not known to be loopback")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLibraryHubReportsWhyTheCatalogIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	_, err := libraryHub{library.NewClient(srv.Client(), srv.URL, "")}.Candidates(context.Background())
	if !errors.Is(err, library.ErrRateLimited) {
		t.Errorf("err = %v", err)
	}
}

func TestLibraryRoutesAreWired(t *testing.T) {
	cfg := testConfig(t)
	cfg.MemoryMargin = 2048
	ln := listen(t, cfg.Addr)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, quiet(), ln) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	base := "http://" + ln.Addr().String() + "/api/v1"
	for range 100 {
		if resp, err := http.Get(base + "/healthz"); err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(cfg.Models, "a-Q8_0.gguf"), []byte("not a real header"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, body := get(t, base+"/library/local")
	models, _ := body["models"].([]any)
	hw, _ := body["hardware"].(map[string]any)
	if code != http.StatusOK || len(models) != 1 || hw["headroom_bytes"] != float64(2048<<20) {
		t.Errorf("local: %d %v", code, body)
	}
	if code, body := get(t, base+"/library/disk"); code != http.StatusOK || body["gguf_bytes"] != float64(17) {
		t.Errorf("disk: %d %v", code, body)
	}
	if code, body := get(t, base+"/library/downloads"); code != http.StatusOK || body["jobs"] == nil {
		t.Errorf("downloads: %d %v", code, body)
	}
}
