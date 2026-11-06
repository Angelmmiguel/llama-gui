package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenSetsPragmas(t *testing.T) {
	s := open(t)
	for _, tc := range []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"busy_timeout", "5000"},
		{"foreign_keys", "1"},
	} {
		var got string
		if err := s.write.QueryRow("PRAGMA " + tc.pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("%s = %s, want %s", tc.pragma, got, tc.want)
		}
	}
	if got := s.write.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("writer connections = %d", got)
	}
}

func TestOpenPathWithSpecialCharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a b?c#d%e")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "x.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.SetKV(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("database is not at the requested path: %v", err)
	}
}

func TestOpenMissingDirectoryFails(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing", "x.db")); err == nil {
		t.Fatal("want an error")
	}
}

func TestMigrationsAreIdempotentAndKeepData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetKV(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := s.read.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Errorf("user_version = %d", version)
	}
	_ = s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if v, err := s.GetKV(ctx, "k"); err != nil || v != "v" {
		t.Errorf("v = %q, err = %v", v, err)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.write.Exec(`PRAGMA user_version = 999`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("err = %v", err)
	}
}

func TestReadHandleCannotWrite(t *testing.T) {
	s := open(t)
	if _, err := s.read.Exec(`INSERT INTO kv (key, value) VALUES ('a', 'b')`); err == nil {
		t.Error("want an error")
	}
}

func TestConcurrentWritesAndReads(t *testing.T) {
	s := open(t)
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := range 32 {
		wg.Go(func() {
			errs <- s.SetKV(ctx, fmt.Sprint("k", i), "v")
		})
		wg.Go(func() {
			if _, err := s.GetKV(ctx, "k0"); err != nil && !errors.Is(err, ErrNotFound) {
				errs <- err
				return
			}
			errs <- nil
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}

func TestKV(t *testing.T) {
	s := open(t)
	if _, err := s.GetKV(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	for _, v := range []string{"one", "two"} {
		if err := s.SetKV(ctx, "k", v); err != nil {
			t.Fatal(err)
		}
		if got, err := s.GetKV(ctx, "k"); err != nil || got != v {
			t.Errorf("got %q, %v; want %q", got, err, v)
		}
	}
}

func TestProfiles(t *testing.T) {
	s := open(t)
	if err := s.CreateProfile(ctx, Profile{ID: "b", Doc: json.RawMessage(`{"n":1}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProfile(ctx, Profile{ID: "a", Doc: json.RawMessage(`{"n":2}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProfile(ctx, Profile{ID: "a", Doc: json.RawMessage(`{}`)}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate: %v", err)
	}
	if err := s.CreateProfile(ctx, Profile{ID: "c", Doc: json.RawMessage(`{nope`)}); err == nil {
		t.Error("invalid JSON accepted")
	}

	p, err := s.GetProfile(ctx, "a")
	if err != nil || string(p.Doc) != `{"n":2}` {
		t.Errorf("get: %+v, %v", p, err)
	}
	if _, err := s.GetProfile(ctx, "zz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing: %v", err)
	}

	list, err := s.ListProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "a" || list[1].ID != "b" {
		t.Errorf("list: %+v", list)
	}

	if err := s.UpdateProfile(ctx, Profile{ID: "a", Doc: json.RawMessage(`{"n":3}`)}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.GetProfile(ctx, "a"); string(p.Doc) != `{"n":3}` {
		t.Errorf("after update: %s", p.Doc)
	}
	if err := s.UpdateProfile(ctx, Profile{ID: "zz", Doc: json.RawMessage(`{}`)}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing: %v", err)
	}

	if err := s.DeleteProfile(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProfile(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing: %v", err)
	}
}

func TestRequestRoundTrip(t *testing.T) {
	s := open(t)
	slot, task := 0, int64(42)
	in := Request{
		StartedAt: t0, Model: "m", Endpoint: "/v1/chat/completions", Method: "POST", Status: 503,
		Slot: &slot, TaskID: &task, TTFTMs: 1.5, TotalMs: 2.5, PromptN: 3, CacheN: 4, PromptMs: 5.5,
		PredictedN: 6, PredictedMs: 7.5, PredictedPerSecond: 8.5, Stream: true, Error: "boom",
		Gaps: json.RawMessage(`[1,2]`),
	}
	id, err := s.InsertRequest(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRequest(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || !got.StartedAt.Equal(t0) || got.Model != "m" || got.Endpoint != in.Endpoint ||
		got.Method != "POST" || got.Status != 503 || got.StatusClass() != 5 || *got.Slot != 0 || *got.TaskID != 42 ||
		got.TTFTMs != 1.5 || got.TotalMs != 2.5 || got.PromptN != 3 || got.CacheN != 4 || got.PromptMs != 5.5 ||
		got.PredictedN != 6 || got.PredictedMs != 7.5 || got.PredictedPerSecond != 8.5 || !got.Stream ||
		got.Error != "boom" || string(got.Gaps) != "[1,2]" {
		t.Errorf("got %+v", got)
	}
	var class int
	if err := s.read.QueryRow(`SELECT status_class FROM requests WHERE id = ?`, id).Scan(&class); err != nil {
		t.Fatal(err)
	}
	if class != 5 {
		t.Errorf("status_class = %d", class)
	}
	if _, err := s.GetRequest(ctx, id+1); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
}

func TestRequestNullableFields(t *testing.T) {
	s := open(t)
	id, err := s.InsertRequest(ctx, Request{StartedAt: t0, Status: 200})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRequest(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Slot != nil || got.TaskID != nil || got.Gaps != nil || got.Stream {
		t.Errorf("got %+v", got)
	}
	if _, err := s.InsertRequest(ctx, Request{StartedAt: t0, Gaps: json.RawMessage(`nope`)}); err == nil {
		t.Error("invalid gaps accepted")
	}
}

func TestRequestBody(t *testing.T) {
	s := open(t)
	id, err := s.InsertRequest(ctx, Request{StartedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetRequestBody(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if err := s.InsertRequestBody(ctx, id, []byte("in"), []byte("out")); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertRequestBody(ctx, id, []byte("in2"), []byte("out2")); err != nil {
		t.Fatal(err)
	}
	in, out, err := s.GetRequestBody(ctx, id)
	if err != nil || string(in) != "in2" || string(out) != "out2" {
		t.Errorf("%q %q %v", in, out, err)
	}
	if err := s.InsertRequestBody(ctx, id+100, nil, nil); err == nil {
		t.Error("body for a missing request accepted")
	}
}

func TestListRequestsFilters(t *testing.T) {
	s := open(t)
	for i, r := range []Request{
		{Model: "a", Endpoint: "chat"},
		{Model: "b", Endpoint: "chat"},
		{Model: "a", Endpoint: "embed"},
		{Model: "b", Endpoint: "embed"},
	} {
		r.StartedAt = t0.Add(time.Duration(i) * time.Minute)
		if _, err := s.InsertRequest(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for name, tc := range map[string]struct {
		f    RequestFilter
		want []int // minute offsets, newest first
	}{
		"all":      {RequestFilter{}, []int{3, 2, 1, 0}},
		"since":    {RequestFilter{Since: t0.Add(2 * time.Minute)}, []int{3, 2}},
		"until":    {RequestFilter{Until: t0.Add(2 * time.Minute)}, []int{1, 0}},
		"model":    {RequestFilter{Model: "a"}, []int{2, 0}},
		"endpoint": {RequestFilter{Endpoint: "embed"}, []int{3, 2}},
		"both":     {RequestFilter{Model: "b", Endpoint: "chat"}, []int{1}},
		"none":     {RequestFilter{Model: "' OR 1=1 --"}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			got, next, err := s.ListRequests(ctx, tc.f)
			if err != nil {
				t.Fatal(err)
			}
			if next != "" {
				t.Errorf("next = %q", next)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d rows, want %d", len(got), len(tc.want))
			}
			for i, m := range tc.want {
				if want := t0.Add(time.Duration(m) * time.Minute); !got[i].StartedAt.Equal(want) {
					t.Errorf("row %d at %v, want %v", i, got[i].StartedAt, want)
				}
			}
		})
	}
}

func TestListRequestsPagination(t *testing.T) {
	s := open(t)
	// Inserted out of order and with ties: the sort key is (started_at, id).
	for _, m := range []int{1, 3, 2, 2, 0} {
		if _, err := s.InsertRequest(ctx, Request{StartedAt: t0.Add(time.Duration(m) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	var seen []int64
	cursor := ""
	for page := 0; ; page++ {
		got, next, err := s.ListRequests(ctx, RequestFilter{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range got {
			seen = append(seen, r.ID)
		}
		if next == "" {
			break
		}
		if page > 5 {
			t.Fatal("cursor never ended")
		}
		cursor = next
	}
	if fmt.Sprint(seen) != "[2 4 3 1 5]" {
		t.Errorf("ids = %v", seen)
	}
	if _, _, err := s.ListRequests(ctx, RequestFilter{Cursor: "!!"}); !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("bad cursor: %v", err)
	}
}

func TestLimitDefaultsAndCap(t *testing.T) {
	if pageSize(0) != defaultLimit || pageSize(-1) != defaultLimit || pageSize(5) != 5 || pageSize(1<<30) != maxLimit {
		t.Error("pageSize")
	}
}

func TestLogLines(t *testing.T) {
	s := open(t)
	if err := s.InsertLogLines(ctx, nil); err != nil {
		t.Fatal(err)
	}
	lines := []LogLine{
		{TS: t0, Level: "info", Source: "srv", Model: "m", Message: "loading 100%", Raw: "raw one"},
		{TS: t0.Add(time.Second), Level: "error", Source: "srv", Message: "Failed to bind", Raw: "raw two"},
		{TS: t0.Add(2 * time.Second), Level: "info", Source: "slot", Message: "a_b", Raw: "raw three"},
		{TS: t0.Add(3 * time.Second), Level: "", Source: "", Message: "", Raw: "unparseable axb"},
	}
	if err := s.InsertLogLines(ctx, lines); err != nil {
		t.Fatal(err)
	}

	all, next, err := s.ListLogLines(ctx, LogFilter{})
	if err != nil || next != "" || len(all) != 4 {
		t.Fatalf("all: %d rows, next %q, %v", len(all), next, err)
	}
	first := all[3]
	if first.ID == 0 || !first.TS.Equal(t0) || first.Level != "info" || first.Source != "srv" || first.Model != "m" ||
		first.Message != "loading 100%" || first.Raw != "raw one" {
		t.Errorf("round trip: %+v", first)
	}

	for name, tc := range map[string]struct {
		f    LogFilter
		want []string
	}{
		"since":                 {LogFilter{Since: t0.Add(2 * time.Second)}, []string{"raw three", "unparseable axb"}},
		"level":                 {LogFilter{Level: "error"}, []string{"raw two"}},
		"source":                {LogFilter{Source: "slot"}, []string{"raw three"}},
		"case insensitive":      {LogFilter{Query: "failed"}, []string{"raw two"}},
		"raw":                   {LogFilter{Query: "unparseable"}, []string{"unparseable axb"}},
		"percent is literal":    {LogFilter{Query: "100%"}, []string{"raw one"}},
		"underscore is literal": {LogFilter{Query: "a_b"}, []string{"raw three"}},
		"combined":              {LogFilter{Level: "info", Source: "srv", Query: "load"}, []string{"raw one"}},
		"injection":             {LogFilter{Source: "' OR 1=1 --"}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			got, _, err := s.ListLogLines(ctx, tc.f)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v", got)
			}
			have := map[string]bool{}
			for _, l := range got {
				have[l.Raw] = true
			}
			for _, w := range tc.want {
				if !have[w] {
					t.Errorf("missing %q", w)
				}
			}
		})
	}
}

func TestLogLinesPagination(t *testing.T) {
	s := open(t)
	lines := make([]LogLine, 5)
	for i := range lines {
		lines[i] = LogLine{TS: t0, Raw: fmt.Sprint(i)}
	}
	if err := s.InsertLogLines(ctx, lines); err != nil {
		t.Fatal(err)
	}
	var seen []string
	cursor := ""
	for {
		got, next, err := s.ListLogLines(ctx, LogFilter{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range got {
			seen = append(seen, l.Raw)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if fmt.Sprint(seen) != "[4 3 2 1 0]" {
		t.Errorf("seen = %v", seen)
	}
	if _, _, err := s.ListLogLines(ctx, LogFilter{Cursor: "bm9wZQ"}); !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("bad cursor: %v", err)
	}
}

func TestEvents(t *testing.T) {
	s := open(t)
	if _, err := s.InsertEvent(ctx, Event{TS: t0, Kind: "load", Severity: "info", Title: "one"}); err != nil {
		t.Fatal(err)
	}
	id, err := s.InsertEvent(ctx, Event{
		TS: t0.Add(time.Minute), Kind: "crash", Severity: "error", Title: "two", Meta: json.RawMessage(`{"code":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertEvent(ctx, Event{TS: t0, Meta: json.RawMessage(`nope`)}); err == nil {
		t.Error("invalid meta accepted")
	}

	all, err := s.ListEvents(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Title != "two" || all[1].Meta != nil {
		t.Errorf("all: %+v", all)
	}
	got, err := s.ListEvents(ctx, t0.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("since: %+v", got)
	}
	e := got[0]
	if e.ID != id || !e.TS.Equal(t0.Add(time.Minute)) || e.Kind != "crash" || e.Severity != "error" ||
		e.Title != "two" || string(e.Meta) != `{"code":1}` {
		t.Errorf("round trip: %+v", e)
	}
}

func TestPrune(t *testing.T) {
	s := open(t)
	cutoff := t0.Add(time.Hour)
	oldID, err := s.InsertRequest(ctx, Request{StartedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	newID, err := s.InsertRequest(ctx, Request{StartedAt: cutoff})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{oldID, newID} {
		if err := s.InsertRequestBody(ctx, id, []byte("in"), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InsertLogLines(ctx, []LogLine{{TS: t0, Raw: "old"}, {TS: cutoff, Raw: "new"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertEvent(ctx, Event{TS: t0, Title: "old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertEvent(ctx, Event{TS: cutoff, Title: "new"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKV(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProfile(ctx, Profile{ID: "p", Doc: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}

	n, err := s.Prune(ctx, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("pruned %d rows, want 3", n)
	}
	if _, err := s.GetRequest(ctx, oldID); !errors.Is(err, ErrNotFound) {
		t.Errorf("old request: %v", err)
	}
	if _, _, err := s.GetRequestBody(ctx, oldID); !errors.Is(err, ErrNotFound) {
		t.Errorf("old body: %v", err)
	}
	if _, err := s.GetRequest(ctx, newID); err != nil {
		t.Errorf("new request: %v", err)
	}
	if _, _, err := s.GetRequestBody(ctx, newID); err != nil {
		t.Errorf("new body: %v", err)
	}
	if lines, _, _ := s.ListLogLines(ctx, LogFilter{}); len(lines) != 1 || lines[0].Raw != "new" {
		t.Errorf("lines: %+v", lines)
	}
	if events, _ := s.ListEvents(ctx, time.Time{}); len(events) != 1 || events[0].Title != "new" {
		t.Errorf("events: %+v", events)
	}
	if _, err := s.GetKV(ctx, "k"); err != nil {
		t.Errorf("kv pruned: %v", err)
	}
	if _, err := s.GetProfile(ctx, "p"); err != nil {
		t.Errorf("profile pruned: %v", err)
	}
}

func TestPruneCrossesBatches(t *testing.T) {
	s := open(t)
	lines := make([]LogLine, pruneBatch+3)
	for i := range lines {
		lines[i] = LogLine{TS: t0}
	}
	if err := s.InsertLogLines(ctx, lines); err != nil {
		t.Fatal(err)
	}
	n, err := s.Prune(ctx, t0.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(lines)) {
		t.Errorf("pruned %d", n)
	}
}

func TestInsertLogLinesSetsIDs(t *testing.T) {
	s := open(t)
	lines := []LogLine{{TS: t0, Raw: "a"}, {TS: t0, Raw: "b"}}
	if err := s.InsertLogLines(ctx, lines); err != nil {
		t.Fatal(err)
	}
	if lines[0].ID == 0 || lines[1].ID != lines[0].ID+1 {
		t.Errorf("%+v", lines)
	}
}

func TestFiltersTakeSeveralValues(t *testing.T) {
	s := open(t)
	if err := s.InsertLogLines(ctx, []LogLine{
		{TS: t0, Level: "info", Source: "slot", Raw: "1"},
		{TS: t0, Level: "warn", Source: "srv", Raw: "2"},
		{TS: t0, Level: "error", Source: "router", Raw: "3"},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _, err := s.ListLogLines(ctx, LogFilter{Level: "warn,error"}); err != nil || len(got) != 2 {
		t.Errorf("levels: %+v %v", got, err)
	}
	if got, _, err := s.ListLogLines(ctx, LogFilter{Level: "warn,error", Source: "slot,router"}); err != nil || len(got) != 1 || got[0].Raw != "3" {
		t.Errorf("levels and sources: %+v %v", got, err)
	}
}

func TestLogLinesModelUntilAndAfter(t *testing.T) {
	s := open(t)
	if err := s.InsertLogLines(ctx, []LogLine{
		{TS: t0, Model: "a", Raw: "1"},
		{TS: t0.Add(time.Second), Model: "b", Raw: "2"},
		{TS: t0.Add(2 * time.Second), Model: "a", Raw: "3"},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _, err := s.ListLogLines(ctx, LogFilter{Model: "b"}); err != nil || len(got) != 1 || got[0].Raw != "2" {
		t.Errorf("model: %+v %v", got, err)
	}
	if got, _, err := s.ListLogLines(ctx, LogFilter{Until: t0.Add(time.Second)}); err != nil || len(got) != 1 || got[0].Raw != "1" {
		t.Errorf("until: %+v %v", got, err)
	}
	got, err := s.LogLinesAfter(ctx, LogFilter{Limit: 2}, 0)
	if err != nil || len(got) != 2 || got[0].Raw != "1" || got[1].Raw != "2" {
		t.Fatalf("first page: %+v %v", got, err)
	}
	got, err = s.LogLinesAfter(ctx, LogFilter{Limit: 2, Model: "a"}, got[1].ID)
	if err != nil || len(got) != 1 || got[0].Raw != "3" {
		t.Errorf("second page: %+v %v", got, err)
	}
}

func TestLogVolumeAndCounts(t *testing.T) {
	s := open(t)
	base := t0.Truncate(time.Minute)
	if err := s.InsertLogLines(ctx, []LogLine{
		{TS: base.Add(-time.Hour), Level: "error", Raw: "old"},
		{TS: base, Level: "info", Raw: "1"},
		{TS: base.Add(10 * time.Second), Level: "info", Raw: "2"},
		{TS: base.Add(20 * time.Second), Level: "error", Raw: "3"},
		{TS: base.Add(time.Minute), Level: "warn", Raw: "4"},
	}); err != nil {
		t.Fatal(err)
	}
	vol, err := s.LogVolume(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(vol) != 3 || !vol[0].Minute.Equal(base) || vol[0].Level != "error" || vol[0].Count != 1 ||
		vol[1].Level != "info" || vol[1].Count != 2 || !vol[2].Minute.Equal(base.Add(time.Minute)) {
		t.Errorf("%+v", vol)
	}
	counts, err := s.CountLogLevels(ctx, base)
	if err != nil || counts["info"] != 2 || counts["error"] != 1 || counts["warn"] != 1 {
		t.Errorf("%v %v", counts, err)
	}
}

func TestRequestStatusClassFilterAndSummary(t *testing.T) {
	s := open(t)
	for i, status := range []int{200, 503, 201} {
		if _, err := s.InsertRequest(ctx, Request{StartedAt: t0.Add(time.Duration(i) * time.Second), Model: "m", Status: status, Gaps: json.RawMessage(`[1]`)}); err != nil {
			t.Fatal(err)
		}
	}
	got, _, err := s.ListRequests(ctx, RequestFilter{StatusClass: 5})
	if err != nil || len(got) != 1 || got[0].Status != 503 {
		t.Errorf("%+v %v", got, err)
	}
	sum, err := s.RequestsForSummary(ctx, RequestFilter{StatusClass: 2}, 10)
	if err != nil || len(sum) != 2 || sum[0].Status != 201 || sum[0].Gaps != nil {
		t.Errorf("%+v %v", sum, err)
	}
	if sum, _ := s.RequestsForSummary(ctx, RequestFilter{}, 1); len(sum) != 1 {
		t.Errorf("bound: %d", len(sum))
	}
}
