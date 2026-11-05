package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/builds"
	"github.com/Angelmmiguel/llama-gui/internal/config"
	"github.com/Angelmmiguel/llama-gui/internal/releases"
	"github.com/Angelmmiguel/llama-gui/internal/store"
)

// fakeBuild puts the fake server where a real install would, with a manifest.
func fakeBuild(t *testing.T, buildsDir, binary, tag string) string {
	t.Helper()
	id, err := builds.BuildID(tag, builds.BackendCPU)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(buildsDir, id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(binary, filepath.Join(dir, "llama-server")); err != nil {
		t.Fatal(err)
	}
	man, err := json.Marshal(builds.Manifest{
		ID: id, Tag: tag, ReleaseTag: tag, Channel: releases.ChannelNightly, Backend: builds.BackendCPU, Arch: builds.ArchX64,
		InstalledAt: time.Now(),
		Probe:       builds.Probe{Version: "0.9.0-dev", Build: tag, Router: true, Flags: []string{"--models-preset", "--ctx-size"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), man, 0o600); err != nil {
		t.Fatal(err)
	}
	return id
}

type e2e struct {
	t    *testing.T
	base string
}

func (e e2e) do(method, path, body string) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.base+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			e.t.Fatalf("%s %s: %q: %v", method, path, raw, err)
		}
	}
	return resp.StatusCode, out
}

func (e e2e) get(path string) map[string]any {
	e.t.Helper()
	code, body := e.do(http.MethodGet, path, "")
	if code != http.StatusOK {
		e.t.Fatalf("GET %s: %d %v", path, code, body)
	}
	return body
}

func (e e2e) list(path, key string) []map[string]any {
	e.t.Helper()
	raw, _ := e.get(path)[key].([]any)
	out := make([]map[string]any, len(raw))
	for i, v := range raw {
		out[i], _ = v.(map[string]any)
	}
	return out
}

func (e e2e) modelStatus(id string) string {
	e.t.Helper()
	for _, m := range e.list("/api/v1/models", "models") {
		if m["id"] == id {
			s, _ := m["status"].(string)
			return s
		}
	}
	return ""
}

func TestEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the fake server")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-llama-server")
	build := exec.Command("go", "build", "-o", binary, "github.com/Angelmmiguel/llama-gui/cmd/fake-llama-server")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	cfg := testConfig(t)
	firstID := fakeBuild(t, cfg.Builds, binary, "b12132")
	secondID := fakeBuild(t, cfg.Builds, binary, "b12133")

	if err := os.MkdirAll(cfg.Data, 0o750); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(cfg.Data, DBFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV(context.Background(), builds.ActiveBuildKey, firstID); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	ln := listen(t, cfg.Addr)
	e := e2e{t: t, base: "http://" + ln.Addr().String()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, quiet(), ln) }()

	waitFor(t, "the router", func() bool {
		code, body := e.do(http.MethodGet, "/api/v1/status", "")
		return code == http.StatusOK && body["state"] == "running"
	})
	status := e.get("/api/v1/status")
	active := status["active_build"].(map[string]any)
	if active["id"] != firstID || active["tag"] != "b12132" || active["backend"] != "cpu" || status["capture"] != config.CaptureMeta {
		t.Errorf("status: %v", status)
	}
	firstPID := status["pid"].(float64)

	code, profile := e.do(http.MethodPost, "/api/v1/profiles",
		`{"name":"Smol","alias":"smol","model_file":"smol.gguf","ctx_size":4096,"parallel":2}`)
	if code != http.StatusCreated {
		t.Fatalf("create profile: %d %v", code, profile)
	}
	if skipped := e.list("/api/v1/profiles", "skipped"); len(skipped) != 1 || skipped[0]["flag"] != "parallel" {
		t.Errorf("the build's flags did not filter the preset: %v", skipped)
	}
	if code, body := e.do(http.MethodPost, "/api/v1/profiles", `{"alias":"bad alias"}`); code != http.StatusBadRequest || body["error"] == "" {
		t.Errorf("invalid profile: %d %v", code, body)
	}
	waitFor(t, "the preset to reach the router", func() bool { return e.modelStatus("smol") == "unloaded" })

	if code, body := e.do(http.MethodPost, "/api/v1/models/smol/load", ""); code != http.StatusAccepted {
		t.Fatalf("load: %d %v", code, body)
	}
	waitFor(t, "the model to load", func() bool { return e.modelStatus("smol") == "loaded" })

	waitFor(t, "telemetry to report the model", func() bool {
		for _, m := range e.list("/api/v1/telemetry", "models") {
			if m["id"] == "smol" && m["status"] == "loaded" {
				slots, _ := m["slots"].([]any)
				return len(slots) > 0
			}
		}
		return false
	})
	if slots := e.list("/api/v1/slots", "models"); len(slots) != 1 || slots[0]["id"] != "smol" {
		t.Errorf("slots: %v", slots)
	}

	// The telemetry stream stays open through the shutdown below.
	stream, err := http.Get(e.base + "/api/v1/telemetry/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Body.Close() }()
	if line, err := bufio.NewReader(stream.Body).ReadString('\n'); err != nil || !strings.HasPrefix(line, "data: {") {
		t.Errorf("stream: %q %v", line, err)
	}

	resp, err := http.Post(e.base+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"smol","stream":true,"messages":[{"role":"user","content":"hello there"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	chat, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(string(chat), "data: [DONE]") {
		t.Fatalf("chat: %d %s %v", resp.StatusCode, chat, err)
	}

	waitFor(t, "the request in history", func() bool { return len(e.list("/api/v1/requests", "requests")) == 1 })
	req := e.list("/api/v1/requests", "requests")[0]
	if req["model"] != "smol" || req["status"] != 200.0 || req["stream"] != true || req["ttft_ms"].(float64) <= 0 ||
		req["predicted_n"].(float64) <= 0 || req["prompt_ms"].(float64) <= 0 {
		t.Errorf("request: %v", req)
	}
	detail := e.get(fmt.Sprintf("/api/v1/requests/%v", req["id"]))
	if gaps, _ := detail["gaps"].([]any); len(gaps) == 0 || detail["body"] != nil {
		t.Errorf("detail: %v", detail)
	}
	if kpis := e.get("/api/v1/requests/summary")["kpis"].(map[string]any); kpis["requests"] != 1.0 {
		t.Errorf("summary: %v", kpis)
	}
	waitFor(t, "aggregates", func() bool {
		stats, _ := e.get("/api/v1/telemetry")["requests"].(map[string]any)
		return stats["per_minute"] == 1.0
	})

	waitFor(t, "the model's logs", func() bool { return len(e.list("/api/v1/logs?model=smol", "lines")) > 0 })
	waitFor(t, "the events", func() bool {
		var kinds []string
		for _, ev := range e.list("/api/v1/events", "events") {
			kinds = append(kinds, ev["kind"].(string))
		}
		return slices.Contains(kinds, "router.started") && slices.Contains(kinds, "model.loaded")
	})

	if code, body := e.do(http.MethodPost, "/api/v1/slots/smol/0/erase", ""); code != http.StatusOK {
		t.Errorf("erase: %d %v", code, body)
	}

	// Switching builds restarts the router and restores what was loaded.
	if code, body := e.do(http.MethodPost, "/api/v1/builds/"+secondID+"/activate", ""); code != http.StatusAccepted {
		t.Fatalf("activate: %d %v", code, body)
	}
	waitFor(t, "the new build to run", func() bool {
		st := e.get("/api/v1/status")
		pid, _ := st["pid"].(float64)
		active, _ := st["active_build"].(map[string]any)
		return st["state"] == "running" && pid != firstPID && active["id"] == secondID
	})
	waitFor(t, "the model to come back", func() bool { return e.modelStatus("smol") == "loaded" })
	if code, body := e.do(http.MethodDelete, "/api/v1/builds/"+firstID, ""); code != http.StatusNoContent {
		t.Errorf("remove the old build: %d %v", code, body)
	}
	if code, _ := e.do(http.MethodDelete, "/api/v1/builds/"+secondID, ""); code != http.StatusConflict {
		t.Errorf("remove the active build: %d", code)
	}

	lastPID := int(e.get("/api/v1/status")["pid"].(float64))
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run: %v", err)
		}
	case <-time.After(shutdownTimeout + stopTimeout + 5*time.Second):
		t.Fatal("run did not stop")
	}
	if err := syscall.Kill(lastPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("router %d left behind: %v", lastPID, err)
	}

	// The log pipeline's final write includes the last supervisor event.
	st, err = store.Open(filepath.Join(cfg.Data, DBFile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	events, err := st.ListEvents(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].Kind != eventRouterStopped {
		t.Errorf("newest event: %+v", events[:min(len(events), 1)])
	}
}
