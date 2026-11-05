package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/logs"
	"github.com/Angelmmiguel/llama-gui/internal/store"
)

const (
	defaultBacklog = 200
	volumeMinutes  = 60
	keepalive      = 15 * time.Second
	downloadPage   = 1000
)

type logLineJSON struct {
	ID      int64     `json:"id"`
	TS      time.Time `json:"ts"`
	Level   string    `json:"level"`
	Source  string    `json:"source"`
	Model   string    `json:"model"`
	Message string    `json:"message"`
	Raw     string    `json:"raw"`
}

func toLogLineJSON(l store.LogLine) logLineJSON {
	return logLineJSON{l.ID, l.TS, l.Level, l.Source, l.Model, l.Message, l.Raw}
}

type eventJSON struct {
	ID       int64           `json:"id"`
	TS       time.Time       `json:"ts"`
	Kind     string          `json:"kind"`
	Severity string          `json:"severity"`
	Title    string          `json:"title"`
	Meta     json.RawMessage `json:"meta,omitempty"`
}

func toEventJSON(e store.Event) eventJSON {
	return eventJSON{e.ID, e.TS, e.Kind, e.Severity, e.Title, e.Meta}
}

func logFilter(q *query) store.LogFilter {
	return store.LogFilter{
		Since: q.time("since"), Until: q.time("until"),
		Level: q.str("level"), Source: q.str("source"), Model: q.str("model"), Query: q.str("q"),
	}
}

func (a *api) listLogs(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	f := logFilter(q)
	f.Limit = q.integer("limit", 100, 1, 1000)
	f.Cursor = q.str("cursor")
	if q.err != nil {
		badRequest(w, "%v", q.err)
		return
	}
	lines, next, err := a.Store.ListLogLines(r.Context(), f)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]logLineJSON, len(lines))
	for i, l := range lines {
		out[i] = toLogLineJSON(l)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"lines": out, "next_cursor": next})
}

// anyOf says whether a comma-separated filter admits v; empty admits everything.
func anyOf(list, v string) bool {
	return list == "" || slices.Contains(strings.Split(list, ","), v)
}

// matches applies a log filter to a live line the way the store does.
func matches(f store.LogFilter, l *store.LogLine) bool {
	if !anyOf(f.Level, l.Level) || !anyOf(f.Source, l.Source) || (f.Model != "" && l.Model != f.Model) {
		return false
	}
	if f.Query == "" {
		return true
	}
	q := strings.ToLower(f.Query)
	return strings.Contains(strings.ToLower(l.Message), q) || strings.Contains(strings.ToLower(l.Raw), q)
}

func (a *api) streamLogs(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	f := logFilter(q)
	f.Limit = q.integer("backlog", defaultBacklog, 1, 1000)
	if q.err != nil {
		badRequest(w, "%v", q.err)
		return
	}
	backlog, ch, stop, err := a.Tail.Tail(r.Context(), f)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer stop()

	stream := startSSE(w)
	for _, l := range backlog {
		if stream.send("log", l.ID, toLogLineJSON(l)) != nil {
			return
		}
	}
	ticker := time.NewTicker(keepalive)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-a.Done:
			return
		case <-ticker.C:
			if stream.ping() != nil {
				return
			}
		case it, ok := <-ch:
			if !ok {
				return // too far behind: reconnecting brings the backlog
			}
			if a.sendItem(stream, f, it) != nil {
				return
			}
		}
	}
}

func (a *api) sendItem(stream *sse, f store.LogFilter, it logs.Item) error {
	switch {
	case it.Line != nil && matches(f, it.Line):
		return stream.send("log", it.Line.ID, toLogLineJSON(*it.Line))
	case it.Event != nil:
		return stream.send("event", 0, toEventJSON(*it.Event))
	}
	return nil
}

type volumeBucket struct {
	Minute time.Time `json:"minute"`
	Debug  int64     `json:"debug"`
	Info   int64     `json:"info"`
	Warn   int64     `json:"warn"`
	Error  int64     `json:"error"`
	Other  int64     `json:"other"`
}

func (a *api) logVolume(w http.ResponseWriter, r *http.Request) {
	now := a.Now()
	first := now.Truncate(time.Minute).Add(-(volumeMinutes - 1) * time.Minute)
	buckets, err := a.Store.LogVolume(r.Context(), first)
	if err != nil {
		a.fail(w, err)
		return
	}
	y, m, d := now.Date()
	today, err := a.Store.CountLogLevels(r.Context(), time.Date(y, m, d, 0, 0, 0, 0, now.Location()))
	if err != nil {
		a.fail(w, err)
		return
	}

	out := make([]volumeBucket, volumeMinutes)
	for i := range out {
		out[i].Minute = first.Add(time.Duration(i) * time.Minute).UTC()
	}
	for _, b := range buckets {
		i := int(b.Minute.Sub(first) / time.Minute)
		if i < 0 || i >= len(out) {
			continue
		}
		switch b.Level {
		case logs.LevelDebug:
			out[i].Debug += b.Count
		case logs.LevelInfo:
			out[i].Info += b.Count
		case logs.LevelWarn:
			out[i].Warn += b.Count
		case logs.LevelError:
			out[i].Error += b.Count
		default:
			out[i].Other += b.Count
		}
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"minutes": out,
		"today":   map[string]int64{"warn": today[logs.LevelWarn], "error": today[logs.LevelError]},
	})
}

func (a *api) downloadLogs(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	f := logFilter(q)
	f.Limit = downloadPage
	if q.err != nil {
		badRequest(w, "%v", q.err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="llama-gui.log"`)
	rc := http.NewResponseController(w)
	var after int64
	for {
		lines, err := a.Store.LogLinesAfter(r.Context(), f, after)
		if err != nil {
			if after == 0 {
				a.fail(w, err)
			} else {
				a.log.Warn("downloading logs", "error", err)
			}
			return
		}
		for _, l := range lines {
			if _, err := fmt.Fprintln(w, formatLogLine(l)); err != nil { //nolint:gosec // G705: served as text/plain, as an attachment.
				return
			}
			after = l.ID
		}
		if len(lines) < downloadPage {
			return
		}
		_ = rc.Flush()
	}
}

func formatLogLine(l store.LogLine) string {
	text := l.Message
	if text == "" {
		text = l.Raw
	}
	model := l.Model
	if model == "" {
		model = "-"
	}
	return l.TS.UTC().Format(time.RFC3339Nano) + " " + l.Level + " " + l.Source + " " + model + " " + text
}

func (a *api) listEvents(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	since := q.time("since")
	if q.err != nil {
		badRequest(w, "%v", q.err)
		return
	}
	events, err := a.Store.ListEvents(r.Context(), since)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]eventJSON, len(events))
	for i, e := range events {
		out[i] = toEventJSON(e)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"events": out})
}
