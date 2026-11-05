package api

import (
	"net/http"
	"slices"
	"testing"

	"github.com/Angelmmiguel/llama-gui/internal/llama"
	"github.com/Angelmmiguel/llama-gui/internal/telemetry"
)

func TestListModels(t *testing.T) {
	h := newHarness(t)
	h.router.models = []llama.Model{{ID: "a", Status: "loaded", Port: 5, ContextLength: 8}}
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/models", "")
	list, _ := body["models"].([]any)
	if code != http.StatusOK || len(list) != 1 || list[0].(map[string]any)["id"] != "a" || list[0].(map[string]any)["memory"] == nil {
		t.Errorf("%d %v", code, body)
	}

	h.router.err = &llama.ErrUpstream{Status: 500, Message: "boom"}
	h.table(t, []route{{"upstream failure", http.MethodGet, "/api/v1/models", "", http.StatusBadGateway, "boom"}})

	h.up = false
	code, body = h.sendJSON(t, http.MethodGet, "/api/v1/models", "")
	if list, ok := body["models"].([]any); code != http.StatusOK || !ok || len(list) != 0 {
		t.Errorf("down: %d %v", code, body)
	}
}

func TestLoadAndUnload(t *testing.T) {
	h := newHarness(t)
	h.table(t, []route{
		{"load", http.MethodPost, "/api/v1/models/a/load", "", http.StatusAccepted, ""},
		{"unload", http.MethodPost, "/api/v1/models/a/unload", "", http.StatusAccepted, ""},
		{"escaped slash", http.MethodPost, "/api/v1/models/org%2Frepo:Q4/load", "", http.StatusAccepted, ""},
		{"bad id", http.MethodPost, "/api/v1/models/%20x/load", "", http.StatusBadRequest, "model id"},
	})
	if !slices.Equal(h.router.calls, []string{"load a", "unload a", "load org/repo:Q4"}) {
		t.Errorf("calls %v", h.router.calls)
	}
	h.router.err = &llama.ErrUpstream{Status: 400, Message: "model 'x' not found"}
	h.table(t, []route{{"unknown", http.MethodPost, "/api/v1/models/x/load", "", http.StatusBadRequest, "not found"}})
	h.up = false
	h.table(t, []route{{"down", http.MethodPost, "/api/v1/models/x/load", "", http.StatusServiceUnavailable, "not running"}})
}

func TestListSlots(t *testing.T) {
	h := newHarness(t)
	h.telemetry.snap.Models = []telemetry.Model{
		{ID: "a", Status: "loaded", Slots: []telemetry.Slot{{ID: 0, State: telemetry.StateDecode}}},
		{ID: "b", Status: "unloaded", Slots: []telemetry.Slot{}},
	}
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/slots", "")
	list, _ := body["models"].([]any)
	if code != http.StatusOK || len(list) != 1 || list[0].(map[string]any)["id"] != "a" {
		t.Errorf("%d %v", code, body)
	}
}

func TestSlotActions(t *testing.T) {
	h := newHarness(t)
	h.table(t, []route{
		{"save", http.MethodPost, "/api/v1/slots/a/1/save", `{"filename":"keep.bin"}`, http.StatusOK, ""},
		{"restore", http.MethodPost, "/api/v1/slots/a/1/restore", `{"filename":"keep.bin"}`, http.StatusOK, ""},
		{"erase", http.MethodPost, "/api/v1/slots/a/1/erase", "", http.StatusOK, ""},
		{"erase ignores a filename", http.MethodPost, "/api/v1/slots/a/0/erase", `{"filename":"../x"}`, http.StatusOK, ""},
		{"cancel", http.MethodPost, "/api/v1/slots/a/1/cancel", "", http.StatusOK, ""},
		{"cancel with nothing to cancel", http.MethodPost, "/api/v1/slots/idle/1/cancel", "", http.StatusNotFound, "no request"},
		{"unknown action", http.MethodPost, "/api/v1/slots/a/1/explode", "", http.StatusBadRequest, "action"},
		{"bad slot", http.MethodPost, "/api/v1/slots/a/x/save", `{"filename":"f"}`, http.StatusBadRequest, "slot"},
		{"negative slot", http.MethodPost, "/api/v1/slots/a/-1/erase", "", http.StatusBadRequest, "slot"},
		{"bad model", http.MethodPost, "/api/v1/slots/%20/1/erase", "", http.StatusBadRequest, "model"},
		{"save needs a name", http.MethodPost, "/api/v1/slots/a/1/save", "", http.StatusBadRequest, "filename"},
		{"traversal", http.MethodPost, "/api/v1/slots/a/1/save", `{"filename":"../../etc/passwd"}`, http.StatusBadRequest, "filename"},
		{"subdirectory", http.MethodPost, "/api/v1/slots/a/1/restore", `{"filename":"a/b"}`, http.StatusBadRequest, "filename"},
		{"dot file", http.MethodPost, "/api/v1/slots/a/1/save", `{"filename":".hidden"}`, http.StatusBadRequest, "filename"},
		{"parent", http.MethodPost, "/api/v1/slots/a/1/save", `{"filename":".."}`, http.StatusBadRequest, "filename"},
		{"not json", http.MethodPost, "/api/v1/slots/a/1/save", `nope`, http.StatusBadRequest, "JSON"},
	})
	want := []string{"slot a 1 save keep.bin", "slot a 1 restore keep.bin", "slot a 1 erase ", "slot a 0 erase "}
	if !slices.Equal(h.router.calls, want) {
		t.Errorf("calls %q", h.router.calls)
	}
	if !slices.Equal(h.cancelled, []string{"a", "idle"}) {
		t.Errorf("cancelled %v", h.cancelled)
	}
	h.up = false
	h.table(t, []route{
		{"down", http.MethodPost, "/api/v1/slots/a/1/erase", "", http.StatusServiceUnavailable, "not running"},
	})
}
