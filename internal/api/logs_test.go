package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/logs"
	"github.com/Angelmmiguel/llama-gui/internal/store"
)

func seedLogs(t *testing.T, h *harness) {
	t.Helper()
	if err := h.store.InsertLogLines(context.Background(), []store.LogLine{
		{TS: testNow.Add(-25 * time.Hour), Level: "error", Source: "srv", Message: "yesterday", Raw: "yesterday"},
		{TS: testNow.Add(-2 * time.Minute), Level: "info", Source: "srv", Model: "a", Message: "alpha", Raw: "raw alpha"},
		{TS: testNow.Add(-time.Minute), Level: "warn", Source: "slot", Model: "b", Message: "beta", Raw: "raw beta"},
		{TS: testNow.Add(-time.Minute), Level: "error", Source: "srv", Raw: "only raw"},
		{TS: testNow.Add(-time.Minute), Level: "weird", Source: "srv", Message: "gamma", Raw: "raw gamma"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestListLogs(t *testing.T) {
	h := newHarness(t)
	seedLogs(t, h)
	for name, tc := range map[string]struct {
		query string
		want  int
	}{
		"all":     {"", 5},
		"level":   {"?level=warn", 1},
		"levels":  {"?level=warn,error", 3},
		"sources": {"?source=slot,srv", 5},
		"source":  {"?source=slot", 1},
		"model":   {"?model=a", 1},
		"query":   {"?q=ALPHA", 1},
		"since":   {"?since=" + testNow.Add(-90*time.Second).Format(time.RFC3339), 3},
		"limit":   {"?limit=2", 2},
	} {
		t.Run(name, func(t *testing.T) {
			code, body := h.sendJSON(t, http.MethodGet, "/api/v1/logs"+tc.query, "")
			if list, _ := body["lines"].([]any); code != http.StatusOK || len(list) != tc.want {
				t.Errorf("%d %v", code, body)
			}
		})
	}
	_, body := h.sendJSON(t, http.MethodGet, "/api/v1/logs?limit=4", "")
	next, _ := body["next_cursor"].(string)
	if next == "" {
		t.Fatal("no cursor")
	}
	_, body = h.sendJSON(t, http.MethodGet, "/api/v1/logs?limit=4&cursor="+next, "")
	list, _ := body["lines"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["message"] != "yesterday" {
		t.Errorf("%v", body)
	}
	h.table(t, []route{
		{"bad limit", http.MethodGet, "/api/v1/logs?limit=-1", "", http.StatusBadRequest, "limit"},
		{"bad cursor", http.MethodGet, "/api/v1/logs?cursor=!", "", http.StatusBadRequest, "cursor"},
		{"bad since", http.MethodGet, "/api/v1/logs?since=x", "", http.StatusBadRequest, "since"},
	})
}

func TestLogVolume(t *testing.T) {
	h := newHarness(t)
	seedLogs(t, h)
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/logs/volume", "")
	minutes, _ := body["minutes"].([]any)
	if code != http.StatusOK || len(minutes) != 60 {
		t.Fatalf("%d %v", code, body)
	}
	last := minutes[59].(map[string]any)
	if last["minute"] != "2026-01-01T12:00:00Z" {
		t.Errorf("last bucket: %v", last)
	}
	prev := minutes[58].(map[string]any)
	if prev["warn"] != 1.0 || prev["error"] != 1.0 || prev["other"] != 1.0 || prev["info"] != 0.0 {
		t.Errorf("bucket: %v", prev)
	}
	today := body["today"].(map[string]any)
	if today["warn"] != 1.0 || today["error"] != 1.0 {
		t.Errorf("today: %v", today)
	}
}

func TestDownloadLogs(t *testing.T) {
	h := newHarness(t)
	seedLogs(t, h)
	w := h.send(t, http.MethodGet, "/api/v1/logs/download?level=error", "")
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") ||
		!strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "yesterday") || !strings.HasSuffix(lines[1], "error srv - only raw") {
		t.Errorf("oldest first, falling back to raw: %q", lines)
	}
	if w := h.send(t, http.MethodGet, "/api/v1/logs/download?since=x", ""); w.Code != http.StatusBadRequest {
		t.Errorf("code %d", w.Code)
	}
}

func TestDownloadCrossesPages(t *testing.T) {
	h := newHarness(t)
	batch := make([]store.LogLine, downloadPage+5)
	for i := range batch {
		batch[i] = store.LogLine{TS: testNow, Level: "info", Message: "x", Raw: "x"}
	}
	if err := h.store.InsertLogLines(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	w := h.send(t, http.MethodGet, "/api/v1/logs/download", "")
	if n := strings.Count(w.Body.String(), "\n"); n != len(batch) {
		t.Errorf("%d lines", n)
	}
}

func TestListEvents(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, e := range []store.Event{
		{TS: testNow.Add(-time.Hour), Kind: "old", Severity: "info", Title: "Old"},
		{TS: testNow, Kind: "new", Severity: "warn", Title: "New", Meta: []byte(`{"model":"a"}`)},
	} {
		if _, err := h.store.InsertEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/events", "")
	list, _ := body["events"].([]any)
	if code != http.StatusOK || len(list) != 2 {
		t.Fatalf("%d %v", code, body)
	}
	newest := list[0].(map[string]any)
	if newest["kind"] != "new" || newest["meta"].(map[string]any)["model"] != "a" {
		t.Errorf("%v", newest)
	}
	_, body = h.sendJSON(t, http.MethodGet, "/api/v1/events?since="+testNow.Add(-time.Minute).Format(time.RFC3339), "")
	if list, _ := body["events"].([]any); len(list) != 1 {
		t.Errorf("%v", body)
	}
	h.table(t, []route{{"bad since", http.MethodGet, "/api/v1/events?since=x", "", http.StatusBadRequest, "since"}})
}

func TestLogStreamBacklogLiveAndFilter(t *testing.T) {
	h := newHarness(t)
	h.tail.backlog = []store.LogLine{{ID: 3, TS: testNow, Level: "info", Message: "old"}}
	srv := httptest.NewServer(h.handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/logs/stream?level=info,error&backlog=5")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("%v", resp.Header)
	}
	h.tail.ch <- logs.Item{Line: &store.LogLine{ID: 4, Level: "warn", Message: "filtered out"}}
	h.tail.ch <- logs.Item{Line: &store.LogLine{ID: 5, Level: "info", Message: "live"}}
	h.tail.ch <- logs.Item{Event: &store.Event{ID: 9, Kind: "x", Title: "X"}}

	got := readSSE(t, resp, 3)
	if got[0].event != "log" || got[0].id != "3" || !strings.Contains(got[0].data, `"old"`) {
		t.Errorf("backlog: %+v", got[0])
	}
	if got[1].event != "log" || got[1].id != "5" {
		t.Errorf("live: %+v", got[1])
	}
	if got[2].event != "event" || !strings.Contains(got[2].data, `"kind":"x"`) {
		t.Errorf("event: %+v", got[2])
	}
	if h.tail.filter.Level != "info,error" || h.tail.filter.Limit != 5 {
		t.Errorf("filter %+v", h.tail.filter)
	}

	close(h.done)
	select {
	case <-h.tail.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not end the stream")
	}
}

func TestLogStreamEndsWithTheClientAndWhenBehind(t *testing.T) {
	h := newHarness(t)
	srv := httptest.NewServer(h.handler)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/logs/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	cancel()
	select {
	case <-h.tail.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("client going away did not end the stream")
	}

	h = newHarness(t)
	srv2 := httptest.NewServer(h.handler)
	defer srv2.Close()
	close(h.tail.ch)
	resp2, err := http.Get(srv2.URL + "/api/v1/logs/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	select {
	case <-h.tail.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("falling behind did not end the stream")
	}
}

func TestLogStreamFailures(t *testing.T) {
	h := newHarness(t)
	h.table(t, []route{{"bad backlog", http.MethodGet, "/api/v1/logs/stream?backlog=0", "", http.StatusBadRequest, "backlog"}})
	h.tail.err = store.ErrInvalidCursor
	h.table(t, []route{{"tail failure", http.MethodGet, "/api/v1/logs/stream", "", http.StatusBadRequest, ""}})
}
