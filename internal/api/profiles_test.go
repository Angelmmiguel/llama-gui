package api

import (
	"net/http"
	"testing"

	"github.com/Angelmmiguel/llama-gui/internal/profiles"
)

const validProfile = `{"name":"Small","alias":"small","model_file":"a.gguf","ctx_size":4096}`

func TestProfilesCRUD(t *testing.T) {
	h := newHarness(t)
	h.profiles.skipped = []profiles.Skipped{{Alias: "x", Reason: "y"}}

	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/profiles", "")
	if list, _ := body["profiles"].([]any); code != http.StatusOK || list == nil || len(list) != 0 || body["skipped"] == nil {
		t.Errorf("empty: %d %v", code, body)
	}

	code, body = h.sendJSON(t, http.MethodPost, "/api/v1/profiles", validProfile)
	if code != http.StatusCreated || body["id"] != "new" || body["alias"] != "small" {
		t.Fatalf("create: %d %v", code, body)
	}
	_, body = h.sendJSON(t, http.MethodGet, "/api/v1/profiles/new", "")
	if body["alias"] != "small" {
		t.Errorf("get: %v", body)
	}
	_, body = h.sendJSON(t, http.MethodGet, "/api/v1/profiles/new/command", "")
	if body["command"] != "llama-server -m a.gguf" {
		t.Errorf("command: %v", body)
	}
	code, body = h.sendJSON(t, http.MethodPut, "/api/v1/profiles/new", `{"name":"Small","alias":"renamed","model_file":"a.gguf","ctx_size":8192}`)
	if code != http.StatusOK || body["alias"] != "renamed" {
		t.Errorf("update: %d %v", code, body)
	}

	h.table(t, []route{
		{"invalid create", http.MethodPost, "/api/v1/profiles", `{"name":"x","alias":"bad alias","model_file":"a.gguf","ctx_size":1}`, http.StatusBadRequest, "alias"},
		{"invalid update", http.MethodPut, "/api/v1/profiles/new", `{"alias":"ok","model_file":"../up.gguf","ctx_size":1}`, http.StatusBadRequest, "invalid profile"},
		{"not json", http.MethodPost, "/api/v1/profiles", `nope`, http.StatusBadRequest, "JSON"},
		{"unknown field", http.MethodPost, "/api/v1/profiles", `{"bogus":1}`, http.StatusBadRequest, "bogus"},
		{"duplicate alias", http.MethodPost, "/api/v1/profiles", `{"name":"x","alias":"renamed","model_file":"b.gguf","ctx_size":4096}`, http.StatusConflict, ""},
		{"missing get", http.MethodGet, "/api/v1/profiles/none", "", http.StatusNotFound, ""},
		{"missing update", http.MethodPut, "/api/v1/profiles/none", validProfile, http.StatusNotFound, ""},
		{"missing command", http.MethodGet, "/api/v1/profiles/none/command", "", http.StatusNotFound, ""},
		{"missing delete", http.MethodDelete, "/api/v1/profiles/none", "", http.StatusNotFound, ""},
		{"delete", http.MethodDelete, "/api/v1/profiles/new", "", http.StatusNoContent, ""},
	})
}
