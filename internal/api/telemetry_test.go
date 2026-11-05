package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/telemetry"
)

func TestTelemetry(t *testing.T) {
	h := newHarness(t)
	h.telemetry.snap.Server.State = "running"
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/telemetry", "")
	if code != http.StatusOK || body["server"].(map[string]any)["state"] != "running" {
		t.Errorf("%d %v", code, body)
	}
}

func TestTelemetryStream(t *testing.T) {
	h := newHarness(t)
	h.telemetry.snap.Server.State = "first"
	srv := httptest.NewServer(h.handler)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/telemetry/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("%v", resp.Header)
	}
	h.telemetry.ch <- &telemetry.Snapshot{Server: telemetry.Server{State: "second"}}
	got := readSSE(t, resp, 2)
	if !strings.Contains(got[0].data, `"first"`) || !strings.Contains(got[1].data, `"second"`) {
		t.Errorf("%+v", got)
	}
}

func TestTelemetryStreamStops(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(h *harness, cancel func())
	}{
		{"client gone", func(_ *harness, cancel func()) { cancel() }},
		{"shutdown", func(h *harness, _ func()) { close(h.done) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			finished := make(chan struct{})
			handler := h.handler
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handler.ServeHTTP(w, r)
				close(finished)
			}))
			defer srv.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/telemetry/stream", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			readSSE(t, resp, 1)
			tc.stop(h, cancel)
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("handler still running")
			}
		})
	}
}
