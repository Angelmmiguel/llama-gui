package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/store"
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

func TestTelemetryHistory(t *testing.T) {
	h := newHarness(t)
	finished := time.Now().UTC().Truncate(telemetry.RollupPeriod).Add(-telemetry.RollupPeriod)
	for _, s := range []store.TelemetrySample{
		{At: finished, Decode: 12.5, DecodePeak: 40, Prefill: 3, TTFT: 90, RPM: 6, KV: 0.25},
		{At: finished.Add(-time.Hour), Decode: 99, DecodePeak: 99},
	} {
		if err := h.store.InsertTelemetrySample(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}

	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/telemetry/history?range=5m", "")
	points := asList(t, body, "points")
	if code != http.StatusOK || body["range"] != "5m" || body["step_seconds"] != float64(10) || len(points) != 30 {
		t.Fatalf("%d %v", code, body)
	}
	// The newest point may be the one before it, if the period turned over during the test.
	var found map[string]any
	for _, p := range points {
		if p["decode"] == 99.0 {
			t.Error("a sample outside the range came back")
		}
		if p["decode"] == 12.5 {
			found = p
		}
	}
	if found == nil || found["decode_peak"] != 40.0 || found["ttft"] != 90.0 || found["kv"] != 0.25 {
		t.Errorf("points = %v", points)
	}

	for rangeName, want := range map[string]int{"15m": 90, "1h": 120, "6h": 120, "24h": 120} {
		_, body = h.sendJSON(t, http.MethodGet, "/api/v1/telemetry/history?range="+rangeName, "")
		if got := len(asList(t, body, "points")); got != want {
			t.Errorf("%s: %d points, want %d", rangeName, got, want)
		}
	}
	if code, _ = h.sendJSON(t, http.MethodGet, "/api/v1/telemetry/history?range=9y", ""); code != http.StatusBadRequest {
		t.Errorf("unknown range: %d", code)
	}
}
