package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testHandler() http.Handler {
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return Handler(Deps{
		Version: "v1",
		Capture: "bodies",
		Started: started,
		Now:     func() time.Time { return started.Add(90*time.Second + 500*time.Millisecond) },
		State:   func() string { return "stopped" },
	})
}

func do(t *testing.T, method, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler().ServeHTTP(w, httptest.NewRequest(method, path, nil))
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s %s: Content-Type = %q", method, path, ct)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s %s: body %q: %v", method, path, w.Body, err)
	}
	return w, body
}

func TestHealthz(t *testing.T) {
	w, body := do(t, http.MethodGet, "/api/v1/healthz")
	if w.Code != http.StatusOK || body["status"] != "ok" {
		t.Errorf("%d %v", w.Code, body)
	}
}

func TestStatus(t *testing.T) {
	w, body := do(t, http.MethodGet, "/api/v1/status")
	if w.Code != http.StatusOK {
		t.Fatalf("code %d", w.Code)
	}
	if body["state"] != "stopped" || body["version"] != "v1" || body["capture"] != "bodies" || body["uptime_seconds"] != 90.0 {
		t.Errorf("body %v", body)
	}
}

func TestUnknownRouteIsJSON404(t *testing.T) {
	w, body := do(t, http.MethodGet, "/api/v1/nope")
	if w.Code != http.StatusNotFound || body["error"] != "Not Found" {
		t.Errorf("%d %v", w.Code, body)
	}
}

func TestWrongMethodIsJSON405(t *testing.T) {
	w, body := do(t, http.MethodPost, "/api/v1/healthz")
	if w.Code != http.StatusMethodNotAllowed || body["error"] != "Method Not Allowed" {
		t.Errorf("%d %v", w.Code, body)
	}
	if allow := w.Header().Get("Allow"); allow == "" {
		t.Error("Allow header lost")
	}
}

func TestWriteJSONFailsAsJSON(t *testing.T) {
	w := httptest.NewRecorder()
	WriteJSON(w, http.StatusOK, make(chan int))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("code %d", w.Code)
	}
}
