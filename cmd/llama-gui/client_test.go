package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/app"
	"github.com/Angelmmiguel/llama-gui/internal/builds"
	"github.com/Angelmmiguel/llama-gui/internal/store"
)

// fakeAPI answers canned JSON per "METHOD path" and records what it was asked.
type fakeAPI struct {
	mu      sync.Mutex
	answers map[string]string
	status  map[string]int
	calls   []string
	srv     *httptest.Server
}

func newFakeAPI(t *testing.T, answers map[string]string) *fakeAPI {
	t.Helper()
	f := &fakeAPI{answers: answers, status: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.RequestURI()
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, strings.TrimSpace(key+" "+string(body)))
		answer, ok := f.answers[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"no such route: `+key+`"}`)
			return
		}
		if code := f.status[key]; code != 0 {
			w.WriteHeader(code)
		}
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) run(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	args = append(args, "--addr", strings.TrimPrefix(f.srv.URL, "http://"))
	code := run(args, noEnv, &out, &errOut)
	return code, out.String(), errOut.String()
}

const buildsAnswer = `{"builds":[
	{"id":"b2-cpu","tag":"b2","release_tag":"v1","channel":"stable","backend":"cpu","active":true,"supported":true},
	{"id":"b1-cpu","tag":"b1","release_tag":"b1","channel":"nightly","backend":"cpu"},
	{"id":"b1-vulkan","tag":"b1","release_tag":"b1","channel":"nightly","backend":"vulkan","supported":true}]}`

func TestStatusCommand(t *testing.T) {
	f := newFakeAPI(t, map[string]string{
		"GET /api/v1/status": `{"state":"running","pid":7,"version":"v9","uptime_seconds":61,"capture":"meta","models_loaded":1,
			"update_available":true,"update":{"latest":"v2"},"active_build":{"id":"b2-cpu"}}`,
		"GET /api/v1/slots": `{"models":[{"id":"smol","slots":[{"id":1,"state":"decode","total":10,"n_ctx":100,"speed":12.5}]}]}`,
	})
	code, out, errOut := f.run("status")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	for _, want := range []string{"running (pid 7)", "v9", "b2-cpu", "v2 available", "1 loaded", "smol/1 decode, 10/100 tokens, 12.5 tok/s"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestStatusWithNothingListening(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()
	code, _, errOut := cli(t, "status", "--addr", addr)
	if code != 1 || !strings.Contains(errOut, "is the service running") {
		t.Errorf("%d %q", code, errOut)
	}
}

func TestAddrFromEnv(t *testing.T) {
	f := newFakeAPI(t, map[string]string{"GET /api/v1/models": `{"models":[]}`})
	env := func(k string) string {
		if k == "LLAMA_GUI_ADDR" {
			return strings.TrimPrefix(f.srv.URL, "http://")
		}
		return ""
	}
	if code := run([]string{"models", "list"}, env, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Errorf("exit %d", code)
	}
}

func TestLogsCommand(t *testing.T) {
	f := newFakeAPI(t, map[string]string{
		"GET /api/v1/logs?limit=2": `{"lines":[
			{"ts":"2026-01-01T00:00:02Z","level":"warn","model":"smol","message":"newer"},
			{"ts":"2026-01-01T00:00:01Z","level":"info","raw":"older raw"}]}`,
	})
	code, out, errOut := f.run("logs", "-n", "2")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "older raw") || !strings.HasSuffix(lines[1], "[smol] newer") {
		t.Errorf("oldest first: %q", lines)
	}
}

func TestLogsFollow(t *testing.T) {
	f := newFakeAPI(t, map[string]string{
		"GET /api/v1/logs/stream?backlog=100": "event: log\nid: 1\ndata: {\"level\":\"info\",\"message\":\"one\"}\n\n" +
			"event: event\ndata: {\"kind\":\"x\",\"message\":\"not a line\"}\n\n" +
			"event: log\nid: 2\ndata: {\"level\":\"info\",\"message\":\"two\"}\n\n",
	})
	code, out, errOut := f.run("logs", "-f")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	if !strings.Contains(out, "one") || !strings.Contains(out, "two") || strings.Contains(out, "not a line") {
		t.Errorf("%q", out)
	}
}

func TestLlamaListAndAvailable(t *testing.T) {
	f := newFakeAPI(t, map[string]string{
		"GET /api/v1/builds":                          buildsAnswer,
		"GET /api/v1/builds/available?channel=stable": `{"releases":[{"tag":"v1","channel":"stable","options":[{"backend":"cpu"},{"backend":"cuda","reason":"no GPU"}]}]}`,
	})
	code, out, _ := f.run("llama", "list")
	if code != 0 || !strings.Contains(out, "active") || !strings.Contains(out, "unsupported") {
		t.Errorf("%d %s", code, out)
	}
	code, out, _ = f.run("llama", "available", "--channel", "stable")
	if code != 0 || !strings.Contains(out, "v1") || !strings.Contains(out, "cpu") || strings.Contains(out, "cuda") {
		t.Errorf("%d %s", code, out)
	}
}

func TestLlamaInstallWaits(t *testing.T) {
	jobPoll = time.Millisecond
	f := newFakeAPI(t, map[string]string{
		"POST /api/v1/builds/install": `{"id":"b3-cpu"}`,
		"GET /api/v1/builds/jobs":     `{"jobs":[{"id":"b3-cpu","state":"downloading","done":1,"total":2}]}`,
	})
	go func() {
		time.Sleep(10 * time.Millisecond)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.answers["GET /api/v1/builds/jobs"] = `{"jobs":[{"id":"b3-cpu","state":"done"}]}`
	}()
	code, out, errOut := f.run("llama", "install", "b3", "--backend", "cpu")
	if code != 0 || !strings.Contains(out, "downloading 50%") || !strings.Contains(out, "installed b3-cpu") {
		t.Errorf("%d %q %q", code, out, errOut)
	}
	if want := `POST /api/v1/builds/install {"backend":"cpu","version":"b3"}`; f.calls[0] != want {
		t.Errorf("call %q", f.calls[0])
	}
}

func TestLlamaInstallFailures(t *testing.T) {
	jobPoll = time.Millisecond
	f := newFakeAPI(t, map[string]string{
		"POST /api/v1/builds/install": `{"id":"b3-cpu"}`,
		"GET /api/v1/builds/jobs":     `{"jobs":[{"id":"b3-cpu","state":"failed","error":"digest mismatch"}]}`,
	})
	code, _, errOut := f.run("llama", "install", "b3")
	if code != 1 || !strings.Contains(errOut, "digest mismatch") {
		t.Errorf("%d %q", code, errOut)
	}

	f = newFakeAPI(t, map[string]string{
		"POST /api/v1/builds/install": `{"id":"b3-cpu"}`,
		"GET /api/v1/builds/jobs":     `{"jobs":[]}`,
	})
	code, out, _ := f.run("llama", "install", "b3")
	if code != 0 || !strings.Contains(out, "already installed") {
		t.Errorf("%d %q", code, out)
	}

	f = newFakeAPI(t, map[string]string{"POST /api/v1/builds/install": `{"error":"invalid backend"}`})
	f.status["POST /api/v1/builds/install"] = http.StatusBadRequest
	code, _, errOut = f.run("llama", "install", "b3")
	if code != 1 || !strings.Contains(errOut, "invalid backend") {
		t.Errorf("%d %q", code, errOut)
	}
}

func TestLlamaUseAndRemove(t *testing.T) {
	f := newFakeAPI(t, map[string]string{
		"GET /api/v1/builds":                  buildsAnswer,
		"POST /api/v1/builds/b2-cpu/activate": `{"active":"b2-cpu"}`,
		"POST /api/v1/builds/b1-cpu/activate": `{"active":"b1-cpu"}`,
		"DELETE /api/v1/builds/b1-vulkan":     ``,
	})
	for _, version := range []string{"v1", "b2", "b2-cpu"} {
		if code, out, errOut := f.run("llama", "use", version); code != 0 || !strings.Contains(out, "b2-cpu") {
			t.Errorf("use %s: %d %q %q", version, code, out, errOut)
		}
	}
	if code, _, errOut := f.run("llama", "use", "b1"); code != 1 || !strings.Contains(errOut, "b1-cpu, b1-vulkan") {
		t.Errorf("ambiguous: %d %q", code, errOut)
	}
	if code, _, errOut := f.run("llama", "use", "b9"); code != 1 || !strings.Contains(errOut, "not installed") {
		t.Errorf("missing: %d %q", code, errOut)
	}
	if code, out, errOut := f.run("llama", "remove", "b1-vulkan"); code != 0 || !strings.Contains(out, "removed") {
		t.Errorf("remove: %d %q %q", code, out, errOut)
	}
}

func TestModelsCommand(t *testing.T) {
	f := newFakeAPI(t, map[string]string{
		"GET /api/v1/models":              `{"models":[{"id":"smol","status":"loaded"}]}`,
		"POST /api/v1/models/smol/load":   `{"id":"smol"}`,
		"POST /api/v1/models/smol/unload": `{"id":"smol"}`,
	})
	if code, out, _ := f.run("models", "list"); code != 0 || !strings.Contains(out, "smol") || !strings.Contains(out, "loaded") {
		t.Errorf("%d %q", code, out)
	}
	for _, sub := range []string{"load", "unload"} {
		if code, out, errOut := f.run("models", sub, "smol"); code != 0 || !strings.Contains(out, sub) {
			t.Errorf("%s: %d %q %q", sub, code, out, errOut)
		}
	}
	if code, _, errOut := f.run("models", "load", "other"); code != 1 || !strings.Contains(errOut, "no such route") {
		t.Errorf("%d %q", code, errOut)
	}
	if code, _, errOut := f.run("models", "pull", "x"); code != 2 || !strings.Contains(errOut, "M7") {
		t.Errorf("%d %q", code, errOut)
	}
}

// doctorEnv points doctor at temporary directories and local sites.
func doctorEnv(t *testing.T) (dirs struct{ data, builds, models string }, flags []string) {
	t.Helper()
	root := t.TempDir()
	dirs.data, dirs.builds, dirs.models = filepath.Join(root, "data"), filepath.Join(root, "builds"), filepath.Join(root, "models")
	if err := os.MkdirAll(dirs.models, 0o750); err != nil {
		t.Fatal(err)
	}
	site := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(site.Close)
	saved := doctorSites
	t.Cleanup(func() { doctorSites = saved })
	doctorSites = []struct{ name, url string }{{"Up", site.URL}, {"Down", "http://127.0.0.1:1"}}
	return dirs, []string{"doctor", "--data", dirs.data, "--builds", dirs.builds, "--models", dirs.models}
}

func setActive(t *testing.T, dataDir, id string) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dataDir, app.DBFile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.SetKV(context.Background(), builds.ActiveBuildKey, id); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorPassesWithAWorkingBuild(t *testing.T) {
	dirs, args := doctorEnv(t)
	setActive(t, dirs.data, "b1-cpu")
	bin := filepath.Join(dirs.builds, "b1-cpu", "llama-server")
	if err := os.MkdirAll(filepath.Dir(bin), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'version: 1 (build 2, commit 3)'\n"), 0o700); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	code, out, _ := cli(t, args...)
	if code != 0 {
		t.Errorf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{
		"PASS  builds", "PASS  data", "PASS  models", "disk", "gpu",
		"PASS  network      Up is reachable", "WARN  network      Down is not reachable",
		"PASS  active build b1-cpu runs: version: 1 (build 2, commit 3)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestDoctorFailsWhenTheBuildDoesNotRun(t *testing.T) {
	dirs, args := doctorEnv(t)
	setActive(t, dirs.data, "b1-cpu")
	code, out, _ := cli(t, args...)
	if code != 1 || !strings.Contains(out, "FAIL  active build") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

func TestDoctorFailsOnAnUnusableDirectory(t *testing.T) {
	_, args := doctorEnv(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	args = append(args, "--builds", filepath.Join(file, "sub"))
	code, out, _ := cli(t, args...)
	if code != 1 || !strings.Contains(out, "FAIL  builds") {
		t.Errorf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "WARN  active build none yet") {
		t.Errorf("no database yet:\n%s", out)
	}
}

func TestDoctorRejectsBadConfig(t *testing.T) {
	if code, _, errOut := cli(t, "doctor", "--capture", "nope"); code != 2 || !strings.Contains(errOut, "capture") {
		t.Errorf("%d %q", code, errOut)
	}
	if code, out, _ := cli(t, "doctor", "-h"); code != 0 || !strings.Contains(out, "usage:") {
		t.Errorf("%d %q", code, out)
	}
}
