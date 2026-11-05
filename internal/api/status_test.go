package api

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Angelmmiguel/llama-gui/internal/builds"
	"github.com/Angelmmiguel/llama-gui/internal/supervisor"
	"github.com/Angelmmiguel/llama-gui/internal/telemetry"
)

func TestStatus(t *testing.T) {
	h := newHarness(t)
	h.builds.list = []builds.Manifest{{ID: "b1-cpu", Tag: "b1", Backend: "cpu"}}
	h.builds.active = "b1-cpu"
	h.telemetry.snap.Models = []telemetry.Model{{ID: "a", Status: "loaded"}, {ID: "b", Status: "unloaded"}}
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/status", "")
	if code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
	active, _ := body["active_build"].(map[string]any)
	if body["state"] != "running" || body["version"] != "v1" || body["capture"] != "bodies" || body["log_verbosity"] != 3.0 || body["uptime_seconds"] != 90.0 ||
		body["models_loaded"] != 1.0 || body["update_available"] != true || active["id"] != "b1-cpu" || active["tag"] != "b1" || active["backend"] != "cpu" {
		t.Errorf("body %v", body)
	}
}

func TestStatusWithoutABuild(t *testing.T) {
	h := newHarness(t)
	_, body := h.sendJSON(t, http.MethodGet, "/api/v1/status", "")
	if v, ok := body["active_build"]; !ok || v != nil {
		t.Errorf("active_build = %v", v)
	}
}

func TestHardware(t *testing.T) {
	h := newHarness(t)
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/hardware", "")
	cpu, _ := body["cpu"].(map[string]any)
	if code != http.StatusOK || cpu["model"] != "x" {
		t.Errorf("%d %v", code, body)
	}
}

func TestServerControl(t *testing.T) {
	h := newHarness(t)
	h.table(t, []route{
		{"start", http.MethodPost, "/api/v1/server/start", "", http.StatusAccepted, ""},
		{"stop", http.MethodPost, "/api/v1/server/stop", "", http.StatusOK, ""},
		{"restart", http.MethodPost, "/api/v1/server/restart", "", http.StatusAccepted, ""},
		{"get is not allowed", http.MethodGet, "/api/v1/server/start", "", http.StatusMethodNotAllowed, ""},
	})
	if len(h.server.calls) != 3 {
		t.Errorf("calls %v", h.server.calls)
	}
	h.server.err = supervisor.ErrNoBuild
	h.table(t, []route{{"no build", http.MethodPost, "/api/v1/server/start", "", http.StatusConflict, "no llama-server build"}})
	h.server.err = errors.New("boom")
	h.table(t, []route{{"other", http.MethodPost, "/api/v1/server/restart", "", http.StatusInternalServerError, "boom"}})
}
