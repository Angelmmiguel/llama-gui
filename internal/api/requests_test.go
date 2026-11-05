package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/store"
)

func seedRequests(t *testing.T, h *harness) []int64 {
	t.Helper()
	slot := 1
	task := int64(7)
	var ids []int64
	for _, r := range []store.Request{
		{StartedAt: testNow.Add(-3 * time.Minute), Model: "a", Endpoint: "/v1/chat/completions", Method: "POST", Status: 200, Slot: &slot, TaskID: &task,
			TTFTMs: 100, TotalMs: 1000, PromptMs: 40, PromptN: 10, CacheN: 5, PredictedMs: 900, PredictedN: 90, PredictedPerSecond: 100, Stream: true, Gaps: json.RawMessage(`[1.5,2.5]`)},
		{StartedAt: testNow.Add(-2 * time.Minute), Model: "a", Endpoint: "/v1/chat/completions", Method: "POST", Status: 200,
			TTFTMs: 300, TotalMs: 3000, PromptMs: 500, PromptN: 20, PredictedMs: 2500, PredictedN: 50, PredictedPerSecond: 20},
		{StartedAt: testNow.Add(-time.Minute), Model: "b", Endpoint: "/v1/completions", Method: "POST", Status: 400, Error: "400 ctx", TotalMs: 5},
		{StartedAt: testNow.Add(-2 * time.Hour), Model: "=evil()", Endpoint: "/v1/completions", Method: "POST", Status: 503, Error: "503 busy"},
	} {
		id, err := h.store.InsertRequest(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestListRequestsFiltersAndCursor(t *testing.T) {
	h := newHarness(t)
	seedRequests(t, h)
	since := testNow.Add(-5 * time.Minute).Format(time.RFC3339)
	for name, tc := range map[string]struct {
		query string
		want  int
	}{
		"all":      {"", 4},
		"model":    {"?model=b", 1},
		"endpoint": {"?endpoint=/v1/chat/completions", 2},
		"status":   {"?status=4xx", 1},
		"class":    {"?status=2", 2},
		"since":    {"?since=" + since, 3},
		"until":    {"?until=" + since, 1},
	} {
		t.Run(name, func(t *testing.T) {
			code, body := h.sendJSON(t, http.MethodGet, "/api/v1/requests"+tc.query, "")
			if list, _ := body["requests"].([]any); code != http.StatusOK || len(list) != tc.want {
				t.Errorf("%d %v", code, body)
			}
		})
	}

	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/requests?limit=3", "")
	list, _ := body["requests"].([]any)
	next, _ := body["next_cursor"].(string)
	if code != http.StatusOK || len(list) != 3 || next == "" {
		t.Fatalf("first page: %d %v", code, body)
	}
	first := list[0].(map[string]any)
	if first["model"] != "b" || first["error"] != "400 ctx" {
		t.Errorf("newest first: %v", first)
	}
	_, body = h.sendJSON(t, http.MethodGet, "/api/v1/requests?limit=3&cursor="+next, "")
	if list, _ := body["requests"].([]any); len(list) != 1 || body["next_cursor"] != "" {
		t.Errorf("second page: %v", body)
	}

	h.table(t, []route{
		{"bad time", http.MethodGet, "/api/v1/requests?since=yesterday", "", http.StatusBadRequest, "since"},
		{"bad limit", http.MethodGet, "/api/v1/requests?limit=99999", "", http.StatusBadRequest, "limit"},
		{"bad status", http.MethodGet, "/api/v1/requests?status=9", "", http.StatusBadRequest, "status"},
		{"bad cursor", http.MethodGet, "/api/v1/requests?cursor=nope", "", http.StatusBadRequest, "cursor"},
		{"bad format", http.MethodGet, "/api/v1/requests?format=xml", "", http.StatusBadRequest, "format"},
	})
}

func TestRequestsCSV(t *testing.T) {
	h := newHarness(t)
	seedRequests(t, h)
	w := h.send(t, http.MethodGet, "/api/v1/requests?format=csv", "")
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	rows, err := csv.NewReader(w.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 || rows[0][0] != "id" || rows[1][2] != "b" {
		t.Fatalf("%v", rows)
	}
	if rows[4][2] != "'=evil()" {
		t.Errorf("a formula must not reach a spreadsheet: %q", rows[4][2])
	}
	if rows[3][8] != "1" || rows[3][9] != "7" || rows[2][8] != "" {
		t.Errorf("slot and task: %v %v", rows[3], rows[2])
	}
	if w := h.send(t, http.MethodGet, "/api/v1/requests?format=csv&model=b", ""); strings.Count(w.Body.String(), "\n") != 2 {
		t.Errorf("filter ignored: %s", w.Body)
	}
}

func TestGetRequest(t *testing.T) {
	h := newHarness(t)
	ids := seedRequests(t, h)
	if err := h.store.InsertRequestBody(context.Background(), ids[0], []byte("prompt"), []byte("answer\xff")); err != nil {
		t.Fatal(err)
	}
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/requests/1", "")
	if code != http.StatusOK {
		t.Fatalf("%d %v", code, body)
	}
	gaps, _ := body["gaps"].([]any)
	captured, _ := body["body"].(map[string]any)
	if len(gaps) != 2 || gaps[0] != 1.5 || captured["request"] != "prompt" || captured["response"] != "answer�" {
		t.Errorf("%v", body)
	}
	if body["queue_ms"] != 60.0 || body["slot"] != 1.0 || body["task_id"] != 7.0 {
		t.Errorf("%v", body)
	}

	_, body = h.sendJSON(t, http.MethodGet, "/api/v1/requests/2", "")
	if v, ok := body["body"]; !ok || v != nil {
		t.Errorf("not captured: %v", body)
	}
	if body["queue_ms"] != 0.0 {
		t.Errorf("queue never goes negative: %v", body)
	}
	if list, ok := body["gaps"].([]any); !ok || len(list) != 0 {
		t.Errorf("gaps: %v", body)
	}
	h.table(t, []route{
		{"missing", http.MethodGet, "/api/v1/requests/999", "", http.StatusNotFound, ""},
		{"bad id", http.MethodGet, "/api/v1/requests/x", "", http.StatusBadRequest, "id"},
	})
}

func TestRequestSummary(t *testing.T) {
	h := newHarness(t)
	seedRequests(t, h)
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/requests/summary", "")
	if code != http.StatusOK {
		t.Fatalf("%d %v", code, body)
	}
	kpis := body["kpis"].(map[string]any)
	if kpis["requests"] != 3.0 || kpis["errors"] != 1.0 || kpis["predicted_tokens"] != 140.0 || kpis["prompt_tokens"] != 30.0 ||
		kpis["cached_tokens"] != 5.0 || kpis["per_minute"] != 3.0/60 || kpis["decode_speed"] != 60.0 {
		t.Errorf("kpis %v", kpis)
	}
	ttft := body["ttft"].(map[string]any)
	if ttft["p50"] != 200.0 || ttft["p95"] != 290.0 {
		t.Errorf("ttft %v", ttft)
	}
	statuses := body["statuses"].([]any)
	if len(statuses) != 2 || statuses[0].(map[string]any)["status"] != "200" || statuses[0].(map[string]any)["count"] != 2.0 ||
		statuses[1].(map[string]any)["status"] != "400 ctx" {
		t.Errorf("statuses %v", statuses)
	}
	models := body["models"].([]any)
	a := models[0].(map[string]any)
	if len(models) != 2 || a["model"] != "a" || a["count"] != 2.0 || a["queue_ms"] != 30.0 || a["prefill_ms"] != 270.0 || a["decode_ms"] != 1700.0 {
		t.Errorf("models %v", models)
	}
	if scatter := body["scatter"].([]any); len(scatter) != 3 || scatter[0].(map[string]any)["model"] != "b" {
		t.Errorf("scatter %v", scatter)
	}
	if body["truncated"] != false {
		t.Errorf("truncated %v", body["truncated"])
	}

	_, body = h.sendJSON(t, http.MethodGet, "/api/v1/requests/summary?model=b&since="+testNow.Add(-5*time.Minute).Format(time.RFC3339), "")
	if kpis := body["kpis"].(map[string]any); kpis["requests"] != 1.0 {
		t.Errorf("filtered %v", kpis)
	}
	h.table(t, []route{{"bad time", http.MethodGet, "/api/v1/requests/summary?until=x", "", http.StatusBadRequest, "until"}})
}

func TestSummaryIsEmptyNotNull(t *testing.T) {
	h := newHarness(t)
	w := h.send(t, http.MethodGet, "/api/v1/requests/summary", "")
	for _, want := range []string{`"statuses":[]`, `"models":[]`, `"scatter":[]`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("missing %s in %s", want, w.Body)
		}
	}
}
