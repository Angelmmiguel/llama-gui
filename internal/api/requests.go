package api

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/store"
	"github.com/Angelmmiguel/llama-gui/internal/telemetry"
)

const (
	defaultSummaryWindow = time.Hour
	maxSummaryRows       = 50000
	maxScatterPoints     = 500
	maxCSVRows           = 100000
	csvPage              = 1000
)

type requestJSON struct {
	ID        int64     `json:"id"`
	StartedAt time.Time `json:"started_at"`
	Model     string    `json:"model"`
	Endpoint  string    `json:"endpoint"`
	Method    string    `json:"method"`
	Status    int       `json:"status"`
	Slot      *int      `json:"slot"`
	TaskID    *int64    `json:"task_id"`
	TTFTMs    float64   `json:"ttft_ms"`
	TotalMs   float64   `json:"total_ms"`
	// QueueMs is TTFT minus prefill; see Metric derivations in ARCHITECTURE.
	QueueMs            float64 `json:"queue_ms"`
	PromptN            int     `json:"prompt_n"`
	CacheN             int     `json:"cache_n"`
	PromptMs           float64 `json:"prompt_ms"`
	PredictedN         int     `json:"predicted_n"`
	PredictedMs        float64 `json:"predicted_ms"`
	PredictedPerSecond float64 `json:"predicted_per_second"`
	Stream             bool    `json:"stream"`
	Error              string  `json:"error"`
}

func queueMs(r store.Request) float64 { return max(r.TTFTMs-r.PromptMs, 0) }

func toRequestJSON(r store.Request) requestJSON {
	return requestJSON{
		ID: r.ID, StartedAt: r.StartedAt, Model: r.Model, Endpoint: r.Endpoint, Method: r.Method, Status: r.Status,
		Slot: r.Slot, TaskID: r.TaskID, TTFTMs: r.TTFTMs, TotalMs: r.TotalMs, QueueMs: queueMs(r),
		PromptN: r.PromptN, CacheN: r.CacheN, PromptMs: r.PromptMs, PredictedN: r.PredictedN, PredictedMs: r.PredictedMs,
		PredictedPerSecond: r.PredictedPerSecond, Stream: r.Stream, Error: r.Error,
	}
}

var statusFilterRe = regexp.MustCompile(`^[1-5](xx)?$`)

// requestFilter reads the filters shared by the list, summary and export.
func requestFilter(q *query) store.RequestFilter {
	f := store.RequestFilter{
		Since: q.time("since"), Until: q.time("until"),
		Model: q.str("model"), Endpoint: q.str("endpoint"),
	}
	if s := q.str("status"); s != "" {
		if !statusFilterRe.MatchString(s) {
			if q.err == nil {
				q.err = fmt.Errorf("status: %q is not a status class such as 2 or 5xx", s)
			}
			return f
		}
		f.StatusClass = int(s[0] - '0')
	}
	return f
}

func (a *api) listRequests(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	f := requestFilter(q)
	f.Limit = q.integer("limit", 100, 1, 1000)
	f.Cursor = q.str("cursor")
	format := q.str("format")
	if format != "" && format != "csv" && q.err == nil {
		q.err = fmt.Errorf("format: %q is not csv", format)
	}
	if q.err != nil {
		badRequest(w, "%v", q.err)
		return
	}
	if format == "csv" {
		a.requestsCSV(w, r, f)
		return
	}
	list, next, err := a.Store.ListRequests(r.Context(), f)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]requestJSON, len(list))
	for i, req := range list {
		out[i] = toRequestJSON(req)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"requests": out, "next_cursor": next})
}

var csvHeader = []string{
	"id", "started_at", "model", "endpoint", "method", "status", "error", "stream", "slot", "task_id", "ttft_ms", "total_ms",
	"queue_ms", "prompt_n", "cache_n", "prompt_ms", "predicted_n", "predicted_ms", "predicted_per_second",
}

func (a *api) requestsCSV(w http.ResponseWriter, r *http.Request, f store.RequestFilter) {
	f.Cursor, f.Limit = "", csvPage
	first, next, err := a.Store.ListRequests(r.Context(), f)
	if err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="requests.csv"`)
	out := csv.NewWriter(w)
	_ = out.Write(csvHeader)
	page, rows := first, 0
	for {
		for _, req := range page {
			_ = out.Write(csvRecord(req))
		}
		rows += len(page)
		out.Flush()
		if next == "" || rows >= maxCSVRows || out.Error() != nil {
			return
		}
		f.Cursor = next
		if page, next, err = a.Store.ListRequests(r.Context(), f); err != nil {
			a.log.Warn("exporting requests", "error", err)
			return
		}
	}
}

func csvRecord(r store.Request) []string {
	floats := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	optional := func(v *int64) string {
		if v == nil {
			return ""
		}
		return strconv.FormatInt(*v, 10)
	}
	var slot *int64
	if r.Slot != nil {
		s := int64(*r.Slot)
		slot = &s
	}
	return []string{
		strconv.FormatInt(r.ID, 10), r.StartedAt.UTC().Format(time.RFC3339Nano), csvCell(r.Model), csvCell(r.Endpoint), csvCell(r.Method),
		strconv.Itoa(r.Status), csvCell(r.Error), strconv.FormatBool(r.Stream), optional(slot), optional(r.TaskID),
		floats(r.TTFTMs), floats(r.TotalMs), floats(queueMs(r)), strconv.Itoa(r.PromptN), strconv.Itoa(r.CacheN), floats(r.PromptMs),
		strconv.Itoa(r.PredictedN), floats(r.PredictedMs), floats(r.PredictedPerSecond),
	}
}

// csvCell defuses formulas: model and endpoint come from clients, and a
// spreadsheet would run a cell starting with one of these.
func csvCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

type requestDetail struct {
	requestJSON
	Gaps []float64 `json:"gaps"`
	// Body is null unless capture kept it.
	Body *requestBody `json:"body"`
}

type requestBody struct {
	Request  string `json:"request"`
	Response string `json:"response"`
}

func (a *api) getRequest(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid request id %q", r.PathValue("id"))
		return
	}
	req, err := a.Store.GetRequest(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := requestDetail{requestJSON: toRequestJSON(req), Gaps: []float64{}}
	if len(req.Gaps) > 0 {
		if err := json.Unmarshal(req.Gaps, &out.Gaps); err != nil {
			a.log.Warn("stored gaps are not a list of numbers", "request", id)
		}
	}
	reqBody, respBody, err := a.Store.GetRequestBody(r.Context(), id)
	switch {
	case err == nil:
		out.Body = &requestBody{Request: strings.ToValidUTF8(string(reqBody), "�"), Response: strings.ToValidUTF8(string(respBody), "�")}
	case !errors.Is(err, store.ErrNotFound):
		a.fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

type percentiles struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
}

func percentilesOf(values []float64) percentiles {
	slices.Sort(values)
	return percentiles{telemetry.Percentile(values, .5), telemetry.Percentile(values, .95), telemetry.Percentile(values, .99)}
}

func median(values []float64) float64 {
	slices.Sort(values)
	return telemetry.Percentile(values, .5)
}

type summaryKPIs struct {
	Requests        int     `json:"requests"`
	Errors          int     `json:"errors"`
	PerMinute       float64 `json:"per_minute"`
	PromptTokens    int64   `json:"prompt_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	PredictedTokens int64   `json:"predicted_tokens"`
	// DecodeSpeed is the median of the requests' tokens per second.
	DecodeSpeed float64 `json:"decode_speed"`
}

type statusCount struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

type modelTimes struct {
	Model string `json:"model"`
	Count int    `json:"count"`
	// Medians, in ms, over the requests that reported each.
	QueueMs   float64 `json:"queue_ms"`
	PrefillMs float64 `json:"prefill_ms"`
	DecodeMs  float64 `json:"decode_ms"`
}

type scatterPoint struct {
	ID         int64     `json:"id"`
	StartedAt  time.Time `json:"started_at"`
	Model      string    `json:"model"`
	Status     int       `json:"status"`
	PromptN    int       `json:"prompt_n"`
	PredictedN int       `json:"predicted_n"`
	TTFTMs     float64   `json:"ttft_ms"`
	TotalMs    float64   `json:"total_ms"`
}

type requestSummaryJSON struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
	// Truncated says only the newest rows of the window were looked at.
	Truncated bool           `json:"truncated"`
	KPIs      summaryKPIs    `json:"kpis"`
	TTFT      percentiles    `json:"ttft"`
	Total     percentiles    `json:"total"`
	Statuses  []statusCount  `json:"statuses"`
	Models    []modelTimes   `json:"models"`
	Scatter   []scatterPoint `json:"scatter"`
}

func (a *api) requestSummary(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	f := requestFilter(q)
	if q.err != nil {
		badRequest(w, "%v", q.err)
		return
	}
	if f.Until.IsZero() {
		f.Until = a.Now()
	}
	if f.Since.IsZero() {
		f.Since = f.Until.Add(-defaultSummaryWindow)
	}
	rows, err := a.Store.RequestsForSummary(r.Context(), f, maxSummaryRows)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := summarise(rows, f.Since, f.Until)
	out.Truncated = len(rows) == maxSummaryRows
	WriteJSON(w, http.StatusOK, out)
}

// statusLabel is the classified error when there is one ("400 ctx"), else the status.
func statusLabel(r store.Request) string {
	if r.Error != "" {
		return r.Error
	}
	return strconv.Itoa(r.Status)
}

// summarise aggregates rows, which are newest first.
func summarise(rows []store.Request, since, until time.Time) requestSummaryJSON {
	type series struct{ queue, prefill, decode []float64 }
	out := requestSummaryJSON{Since: since, Until: until, Statuses: []statusCount{}, Models: []modelTimes{}, Scatter: []scatterPoint{}}
	var ttft, total, speed []float64
	statuses := map[string]int{}
	byModel := map[string]*series{}
	counts := map[string]int{}
	for _, r := range rows {
		out.KPIs.Requests++
		if r.Status >= 400 || r.Error != "" {
			out.KPIs.Errors++
		}
		out.KPIs.PromptTokens += int64(r.PromptN)
		out.KPIs.CachedTokens += int64(r.CacheN)
		out.KPIs.PredictedTokens += int64(r.PredictedN)
		statuses[statusLabel(r)]++
		if r.TotalMs > 0 {
			total = append(total, r.TotalMs)
		}
		if r.PredictedPerSecond > 0 {
			speed = append(speed, r.PredictedPerSecond)
		}
		s := byModel[r.Model]
		if s == nil {
			s = &series{}
			byModel[r.Model] = s
		}
		counts[r.Model]++
		if r.TTFTMs > 0 {
			ttft = append(ttft, r.TTFTMs)
			s.queue = append(s.queue, queueMs(r))
		}
		if r.PromptMs > 0 {
			s.prefill = append(s.prefill, r.PromptMs)
		}
		if r.PredictedMs > 0 {
			s.decode = append(s.decode, r.PredictedMs)
		}
		if len(out.Scatter) < maxScatterPoints {
			out.Scatter = append(out.Scatter, scatterPoint{
				ID: r.ID, StartedAt: r.StartedAt, Model: r.Model, Status: r.Status,
				PromptN: r.PromptN, PredictedN: r.PredictedN, TTFTMs: r.TTFTMs, TotalMs: r.TotalMs,
			})
		}
	}
	if minutes := until.Sub(since).Minutes(); minutes > 0 {
		out.KPIs.PerMinute = float64(out.KPIs.Requests) / minutes
	}
	out.KPIs.DecodeSpeed = median(speed)
	out.TTFT, out.Total = percentilesOf(ttft), percentilesOf(total)
	for label, n := range statuses {
		out.Statuses = append(out.Statuses, statusCount{label, n})
	}
	slices.SortFunc(out.Statuses, func(a, b statusCount) int { return strings.Compare(a.Status, b.Status) })
	for model, s := range byModel {
		out.Models = append(out.Models, modelTimes{
			Model: model, Count: counts[model], QueueMs: median(s.queue), PrefillMs: median(s.prefill), DecodeMs: median(s.decode),
		})
	}
	slices.SortFunc(out.Models, func(a, b modelTimes) int { return strings.Compare(a.Model, b.Model) })
	return out
}
