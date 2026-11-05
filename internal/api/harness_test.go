package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/builds"
	"github.com/Angelmmiguel/llama-gui/internal/hardware"
	"github.com/Angelmmiguel/llama-gui/internal/llama"
	"github.com/Angelmmiguel/llama-gui/internal/logs"
	"github.com/Angelmmiguel/llama-gui/internal/profiles"
	"github.com/Angelmmiguel/llama-gui/internal/releases"
	"github.com/Angelmmiguel/llama-gui/internal/store"
	"github.com/Angelmmiguel/llama-gui/internal/supervisor"
	"github.com/Angelmmiguel/llama-gui/internal/telemetry"
)

var testNow = time.Date(2026, 1, 1, 12, 0, 30, 0, time.UTC)

type fakeServer struct {
	status supervisor.Status
	err    error
	calls  []string
}

func (f *fakeServer) Status() supervisor.Status { return f.status }
func (f *fakeServer) Start(context.Context) error {
	f.calls = append(f.calls, "start")
	return f.err
}
func (f *fakeServer) Stop(ctx context.Context) error {
	f.calls = append(f.calls, "stop")
	return errors.Join(f.err, ctx.Err())
}
func (f *fakeServer) Restart(context.Context) error {
	f.calls = append(f.calls, "restart")
	return f.err
}

type fakeBuilds struct {
	list       []builds.Manifest
	active     string
	jobs       []builds.Job
	installErr error
	job        builds.Job
	err        error
	calls      []string
}

func (f *fakeBuilds) List() ([]builds.Manifest, error) { return f.list, f.err }
func (f *fakeBuilds) Active(context.Context) (string, error) {
	return f.active, nil
}
func (f *fakeBuilds) Get(id string) (builds.Manifest, error) {
	for _, m := range f.list {
		if m.ID == id {
			return m, nil
		}
	}
	return builds.Manifest{}, builds.ErrNotFound
}
func (f *fakeBuilds) Jobs() []builds.Job { return f.jobs }
func (f *fakeBuilds) Install(_ context.Context, version, backend string) (builds.Job, error) {
	f.calls = append(f.calls, "install "+version+" "+backend)
	return f.job, f.installErr
}
func (f *fakeBuilds) Cancel(id string) error {
	f.calls = append(f.calls, "cancel "+id)
	return f.err
}
func (f *fakeBuilds) Activate(_ context.Context, id string) error {
	f.calls = append(f.calls, "activate "+id)
	return f.err
}
func (f *fakeBuilds) Remove(_ context.Context, id string) error {
	f.calls = append(f.calls, "remove "+id)
	return f.err
}
func (f *fakeBuilds) Available(_ context.Context, rel releases.Release, _ string) ([]builds.Option, error) {
	return []builds.Option{{Backend: "cpu", Asset: rel.Tag + ".tar.gz"}}, nil
}

type fakeReleases struct {
	list    map[releases.Channel][]releases.Release
	err     error
	cmp     releases.Comparison
	resolve error
}

func (f *fakeReleases) List(_ context.Context, ch releases.Channel, _ int) ([]releases.Release, error) {
	return f.list[ch], f.err
}
func (f *fakeReleases) ResolveVersion(_ context.Context, version string) (releases.Release, error) {
	if f.resolve != nil {
		return releases.Release{}, f.resolve
	}
	return releases.Release{Tag: version, BinaryTag: "b1"}, nil
}
func (f *fakeReleases) Compare(context.Context, string, string) (releases.Comparison, error) {
	return f.cmp, f.err
}

type fakeProfiles struct {
	items   map[string]profiles.Profile
	skipped []profiles.Skipped
}

func (f *fakeProfiles) List(context.Context) ([]profiles.Profile, error) {
	out := []profiles.Profile{}
	for _, p := range f.items {
		out = append(out, p)
	}
	return out, nil
}
func (f *fakeProfiles) Get(_ context.Context, id string) (profiles.Profile, error) {
	p, ok := f.items[id]
	if !ok {
		return p, store.ErrNotFound
	}
	return p, nil
}
func (f *fakeProfiles) Create(_ context.Context, p profiles.Profile) (profiles.Profile, error) {
	if err := p.Validate(); err != nil {
		return p, err
	}
	for _, o := range f.items {
		if o.Alias == p.Alias {
			return p, store.ErrConflict
		}
	}
	p.ID = "new"
	f.items[p.ID] = p
	return p, nil
}
func (f *fakeProfiles) Update(_ context.Context, id string, p profiles.Profile) (profiles.Profile, error) {
	if _, ok := f.items[id]; !ok {
		return p, store.ErrNotFound
	}
	if err := p.Validate(); err != nil {
		return p, err
	}
	p.ID = id
	f.items[id] = p
	return p, nil
}
func (f *fakeProfiles) Delete(_ context.Context, id string) error {
	if _, ok := f.items[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.items, id)
	return nil
}
func (f *fakeProfiles) Command(p profiles.Profile) string { return "llama-server -m " + p.ModelFile }
func (f *fakeProfiles) Skipped() []profiles.Skipped       { return f.skipped }

type fakeRouter struct {
	models []llama.Model
	err    error
	calls  []string
}

func (f *fakeRouter) Models(context.Context) ([]llama.Model, error) { return f.models, f.err }
func (f *fakeRouter) Load(_ context.Context, id string) error {
	f.calls = append(f.calls, "load "+id)
	return f.err
}
func (f *fakeRouter) Unload(_ context.Context, id string) error {
	f.calls = append(f.calls, "unload "+id)
	return f.err
}
func (f *fakeRouter) SlotAction(_ context.Context, model string, slot int, action, filename string) (map[string]any, error) {
	f.calls = append(f.calls, strings.Join([]string{"slot", model, string(rune('0' + slot)), action, filename}, " "))
	return map[string]any{"id_slot": slot}, f.err
}

type fakeTelemetry struct {
	snap *telemetry.Snapshot
	ch   chan *telemetry.Snapshot
}

func (f *fakeTelemetry) Snapshot() *telemetry.Snapshot { return f.snap }
func (f *fakeTelemetry) Subscribe() (<-chan *telemetry.Snapshot, func()) {
	return f.ch, func() {}
}

type fakeTail struct {
	backlog []store.LogLine
	ch      chan logs.Item
	err     error
	filter  store.LogFilter
	stopped chan struct{}
}

func (f *fakeTail) Tail(_ context.Context, sf store.LogFilter) ([]store.LogLine, <-chan logs.Item, func(), error) {
	f.filter = sf
	return f.backlog, f.ch, func() { close(f.stopped) }, f.err
}

// harness is a Handler with every dependency replaced, over a real store.
type harness struct {
	server    *fakeServer
	builds    *fakeBuilds
	releases  *fakeReleases
	profiles  *fakeProfiles
	router    *fakeRouter
	up        bool
	telemetry *fakeTelemetry
	tail      *fakeTail
	store     *store.Store
	cancelled []string
	done      chan struct{}
	handler   http.Handler
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h := &harness{
		server:    &fakeServer{status: supervisor.Status{State: supervisor.Running, Since: testNow}},
		builds:    &fakeBuilds{},
		releases:  &fakeReleases{list: map[releases.Channel][]releases.Release{}},
		profiles:  &fakeProfiles{items: map[string]profiles.Profile{}},
		router:    &fakeRouter{},
		up:        true,
		telemetry: &fakeTelemetry{snap: &telemetry.Snapshot{Models: []telemetry.Model{}}, ch: make(chan *telemetry.Snapshot, 2)},
		tail:      &fakeTail{ch: make(chan logs.Item, 8), stopped: make(chan struct{})},
		store:     st,
		done:      make(chan struct{}),
	}
	h.handler = Handler(testDeps(h))
	return h
}

func (h *harness) deps() Deps {
	return Deps{
		Version: "v1", Capture: "bodies",
		Started:   testNow.Add(-90 * time.Second),
		Now:       func() time.Time { return testNow },
		Logger:    discardLogger(),
		Server:    h.server,
		Builds:    h.builds,
		Releases:  h.releases,
		Profiles:  h.profiles,
		Store:     h.store,
		Telemetry: h.telemetry,
		Tail:      h.tail,
		Router:    func() (Router, bool) { return h.router, h.up },
		CancelSlot: func(model string, _ int) bool {
			h.cancelled = append(h.cancelled, model)
			return model != "idle"
		},
		Hardware: func(context.Context) hardware.Snapshot { return hardware.Snapshot{CPU: hardware.CPU{Model: "x"}} },
		Updates:  func(context.Context) builds.UpdateInfo { return builds.UpdateInfo{Available: true, Latest: "v2"} },
		Done:     h.done,
	}
}

// send makes one request and returns the status and raw body.
func (h *harness) send(t *testing.T, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rd)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w
}

// sendJSON is send for a JSON answer.
func (h *harness) sendJSON(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	w := h.send(t, method, path, body)
	var out map[string]any
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: body %q: %v", method, path, w.Body, err)
		}
	}
	return w.Code, out
}

// route is one row of a status table.
type route struct {
	name, method, path, body string
	status                   int
	errContains              string
}

func (h *harness) table(t *testing.T, routes []route) {
	t.Helper()
	for _, tc := range routes {
		t.Run(tc.name, func(t *testing.T) {
			code, body := h.sendJSON(t, tc.method, tc.path, tc.body)
			if code != tc.status {
				t.Errorf("status %d, want %d: %v", code, tc.status, body)
			}
			if tc.errContains != "" {
				if msg, _ := body["error"].(string); !strings.Contains(msg, tc.errContains) {
					t.Errorf("error %q, want it to mention %q", msg, tc.errContains)
				}
			}
		})
	}
}

type sseMessage struct{ event, id, data string }

// readSSE reads n messages, or fails the test after a while.
func readSSE(t *testing.T, resp *http.Response, n int) []sseMessage {
	t.Helper()
	got := make(chan []sseMessage, 1)
	go func() {
		var out []sseMessage
		var cur sseMessage
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				out = append(out, cur)
				cur = sseMessage{}
				if len(out) == n {
					got <- out
					return
				}
			case strings.HasPrefix(line, "event: "):
				cur.event = line[len("event: "):]
			case strings.HasPrefix(line, "id: "):
				cur.id = line[len("id: "):]
			case strings.HasPrefix(line, "data: "):
				cur.data = line[len("data: "):]
			}
		}
		got <- out
	}()
	select {
	case out := <-got:
		if len(out) != n {
			t.Fatalf("got %d messages: %+v", len(out), out)
		}
		return out
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %d messages", n)
		return nil
	}
}
