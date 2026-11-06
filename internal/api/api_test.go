package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testDeps(h *harness) Deps { return h.deps() }

func TestHealthz(t *testing.T) {
	h := newHarness(t)
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/healthz", "")
	if code != http.StatusOK || body["status"] != "ok" {
		t.Errorf("%d %v", code, body)
	}
}

func TestUnknownRouteIsJSON404(t *testing.T) {
	h := newHarness(t)
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/nope", "")
	if code != http.StatusNotFound || body["error"] != "Not Found" {
		t.Errorf("%d %v", code, body)
	}
}

func TestWrongMethodIsJSON405(t *testing.T) {
	h := newHarness(t)
	w := h.send(t, http.MethodPost, "/api/v1/healthz", "")
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") == "" {
		t.Errorf("%d %v", w.Code, w.Header())
	}
}

func TestWriteJSONFailsAsJSON(t *testing.T) {
	w := httptest.NewRecorder()
	WriteJSON(w, http.StatusOK, make(chan int))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("code %d", w.Code)
	}
}

func TestCrossSiteWritesAreRejected(t *testing.T) {
	for _, tc := range []struct {
		name, method, site string
		status             int
	}{
		{"no header", http.MethodPost, "", http.StatusAccepted},
		{"same origin", http.MethodPost, "same-origin", http.StatusAccepted},
		{"typed in", http.MethodPost, "none", http.StatusAccepted},
		{"same site", http.MethodPost, "same-site", http.StatusForbidden},
		{"cross site", http.MethodPost, "cross-site", http.StatusForbidden},
		{"cross site read", http.MethodGet, "cross-site", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			path := "/api/v1/builds/jobs"
			if tc.method == http.MethodPost {
				path = "/api/v1/builds/b1-cpu/activate"
			}
			var headers []string
			if tc.site != "" {
				headers = []string{"Sec-Fetch-Site", tc.site}
			}
			w := h.send(t, tc.method, path, "", headers...)
			if w.Code != tc.status {
				t.Errorf("status %d, want %d", w.Code, tc.status)
			}
			if tc.status == http.StatusForbidden && len(h.builds.calls) != 0 {
				t.Errorf("reached the handler: %v", h.builds.calls)
			}
		})
	}
}

// Browsers leave out Sec-Fetch-Site on plain HTTP to a LAN host, but still send Origin.
func TestCrossSiteWritesWithoutFetchMetadata(t *testing.T) {
	const host = "tower:8080"
	for _, tc := range []struct {
		name, origin string
		status       int
	}{
		{"curl", "", http.StatusAccepted},
		{"same host", "http://tower:8080", http.StatusAccepted},
		{"same host, other case", "http://TOWER:8080", http.StatusAccepted},
		{"other host", "http://evil.example", http.StatusForbidden},
		{"same name, other port", "http://tower:9090", http.StatusForbidden},
		{"sandboxed page", "null", http.StatusForbidden},
		{"malformed", "::", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			r := httptest.NewRequest(http.MethodPost, "/api/v1/builds/b1-cpu/activate", nil)
			r.Host = host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			h.handler.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Errorf("status %d, want %d", w.Code, tc.status)
			}
			if tc.status == http.StatusForbidden && len(h.builds.calls) != 0 {
				t.Errorf("reached the handler: %v", h.builds.calls)
			}
		})
	}
}

func TestOversizedBodyIsRejected(t *testing.T) {
	h := newHarness(t)
	big := `{"version":"` + strings.Repeat("x", maxJSONBody) + `"}`
	code, _ := h.sendJSON(t, http.MethodPost, "/api/v1/builds/install", big)
	if code != http.StatusRequestEntityTooLarge {
		t.Errorf("code %d", code)
	}
}
