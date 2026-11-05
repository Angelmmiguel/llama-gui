package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/fakellama"
	"github.com/Angelmmiguel/llama-gui/internal/llama"
	"github.com/Angelmmiguel/llama-gui/internal/store"
)

type memSink struct {
	mu      sync.Mutex
	records []RequestRecord
	bodies  []Body
}

func (s *memSink) Record(r RequestRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r)
}

func (s *memSink) RecordBody(b Body) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies = append(s.bodies, b)
}

func (s *memSink) wait(t *testing.T, n int) []RequestRecord {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d records", n), func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.records) >= n
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RequestRecord(nil), s.records...)
}

func (s *memSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

type allow map[string]bool

func (a allow) Allow(m string) bool { return a[m] }

// fixture is a proxy in front of a fake router with models a and b; a is loaded.
type fixture struct {
	t        *testing.T
	upstream *httptest.Server
	proxy    *Proxy
	front    *httptest.Server
	sink     *memSink
}

func newFixture(t *testing.T, tweak func(*fakellama.Options), opts Options) *fixture {
	t.Helper()
	dir := t.TempDir()
	presetPath := filepath.Join(dir, "preset.ini")
	if err := os.WriteFile(presetPath, []byte("version = 1\n\n[a]\nctx-size = 4096\n\n[b]\nctx-size = 4096\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fo := fakellama.Options{Host: "127.0.0.1", Preset: presetPath, Metrics: true}
	fo.ApplyEnv(func(string) string { return "" })
	fo.LoadDelay, fo.TokenDelay = time.Millisecond, time.Millisecond
	if tweak != nil {
		tweak(&fo)
	}
	fake, err := fakellama.New(fo, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(fake.Handler())
	t.Cleanup(up.Close)
	f := &fixture{t: t, upstream: up, sink: &memSink{}}
	f.start(opts)
	client := llama.New(up.URL, nil)
	if err := client.Load(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a loaded", func() bool {
		models, err := client.Models(context.Background())
		if err != nil {
			return false
		}
		for _, m := range models {
			if m.ID == "a" {
				return m.Status == "loaded"
			}
		}
		return false
	})
	return f
}

func (f *fixture) start(opts Options) {
	u, err := url.Parse(f.upstream.URL)
	if err != nil {
		f.t.Fatal(err)
	}
	if opts.Upstream == nil {
		opts.Upstream = func() (*url.URL, bool) { return u, true }
	}
	if opts.Sink == nil {
		opts.Sink = f.sink
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	f.proxy = New(opts)
	f.front = httptest.NewServer(f.proxy)
	f.t.Cleanup(f.front.Close)
}

func (f *fixture) post(path, body string) *http.Response {
	f.t.Helper()
	resp, err := http.Post(f.front.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	return resp
}

func drain(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const chat = `{"model":"a","messages":[{"role":"user","content":"secret prompt words"}]`

func TestStreamingIsNotBuffered(t *testing.T) {
	const tokens, delay = 10, 30 * time.Millisecond
	f := newFixture(t, func(o *fakellama.Options) { o.TokenDelay = delay }, Options{})
	resp := f.post("/v1/chat/completions", chat+fmt.Sprintf(`,"stream":true,"max_tokens":%d}`, tokens))
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type %q", ct)
	}
	br := bufio.NewReader(resp.Body)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, `"content":" `) {
			break
		}
	}
	// A token is in hand: if the proxy buffered, generation would be over by now.
	if n := f.proxy.InFlight("a"); n != 1 {
		t.Fatalf("InFlight = %d during generation", n)
	}
	list := f.proxy.List()
	if len(list) != 1 || list[0].Model != "a" || list[0].Endpoint != "/v1/chat/completions" || list[0].Phase != PhaseResponding {
		t.Fatalf("List = %+v", list)
	}
	if f.sink.count() != 0 {
		t.Fatal("recorded before the end")
	}
	rest, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rest), "[DONE]") {
		t.Fatal("no [DONE]")
	}

	r := f.sink.wait(t, 1)[0]
	if r.Method != "POST" || r.Endpoint != "/v1/chat/completions" || r.Model != "a" || r.Status != 200 || r.StatusClass != "200" || !r.Stream {
		t.Fatalf("record %+v", r)
	}
	if r.TTFTMs < float64(delay.Milliseconds())*0.5 {
		t.Fatalf("TTFT %v must wait for the first token, not the role chunk", r.TTFTMs)
	}
	if r.TotalMs < r.TTFTMs+float64(delay.Milliseconds()) {
		t.Fatalf("total %v, ttft %v", r.TotalMs, r.TTFTMs)
	}
	if len(r.Gaps) != tokens-1 {
		t.Fatalf("gaps %v", r.Gaps)
	}
	if r.Timings.PredictedN != tokens || r.Timings.PredictedMS <= 0 {
		t.Fatalf("timings %+v", r.Timings)
	}
	if f.proxy.InFlight("a") != 0 || len(f.proxy.List()) != 0 {
		t.Fatal("not removed from in-flight")
	}
}

func TestNonStreamTimings(t *testing.T) {
	f := newFixture(t, nil, Options{})
	body := drain(t, f.post("/v1/chat/completions", chat+`,"max_tokens":3}`))
	if !strings.Contains(body, `"timings"`) {
		t.Fatalf("body %s", body)
	}
	r := f.sink.wait(t, 1)[0]
	if r.Stream || r.Status != 200 || r.TTFTMs <= 0 || r.Timings.PredictedN != 3 || r.Gaps != nil {
		t.Fatalf("record %+v", r)
	}
}

func TestOtherEndpointsPassSilently(t *testing.T) {
	var touched atomic.Int64
	f := newFixture(t, nil, Options{Touch: func(string) { touched.Add(1) }})
	for _, path := range []string{"/slots?model=a&autoload=false", "/metrics?model=a&autoload=false", "/health", "/models"} {
		resp, err := http.Get(f.front.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		if body := drain(t, resp); resp.StatusCode != 200 {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
	drain(t, f.post("/models/load", `{"model":"a"}`))
	time.Sleep(20 * time.Millisecond)
	if f.sink.count() != 0 || touched.Load() != 0 {
		t.Fatalf("records %d, touches %d", f.sink.count(), touched.Load())
	}
}

func TestTouchOnStartAndEnd(t *testing.T) {
	var mu sync.Mutex
	var touches []string
	f := newFixture(t, nil, Options{Touch: func(m string) {
		mu.Lock()
		defer mu.Unlock()
		touches = append(touches, m)
	}})
	drain(t, f.post("/v1/chat/completions", chat+`}`))
	f.sink.wait(t, 1)
	mu.Lock()
	defer mu.Unlock()
	if len(touches) != 2 || touches[0] != "a" || touches[1] != "a" {
		t.Fatalf("touches %v", touches)
	}
}

func TestClientDisconnectCancelsUpstream(t *testing.T) {
	f := newFixture(t, func(o *fakellama.Options) { o.TokenDelay = 20 * time.Millisecond }, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.front.URL+"/v1/chat/completions", strings.NewReader(chat+`,"stream":true,"max_tokens":1000}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	cancel()

	r := f.sink.wait(t, 1)[0]
	if r.Status != 200 || r.StatusClass != "200 cancelled" {
		t.Fatalf("record %+v", r)
	}
	expectIdle(t, f)
}

func expectIdle(t *testing.T, f *fixture) {
	t.Helper()
	client := llama.New(f.upstream.URL, nil)
	waitFor(t, "upstream slots idle", func() bool {
		slots, err := client.Slots(context.Background(), "a")
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range slots {
			if s.IsProcessing {
				return false
			}
		}
		return true
	})
}

func TestCancelBySlot(t *testing.T) {
	f := newFixture(t, func(o *fakellama.Options) { o.TokenDelay = 20 * time.Millisecond }, Options{})
	resp := f.post("/v1/chat/completions", chat+`,"stream":true,"max_tokens":1000}`)
	defer func() { _ = resp.Body.Close() }()
	waitFor(t, "in flight", func() bool { return f.proxy.InFlight("a") == 1 })
	if f.proxy.Cancel("a", 0) {
		t.Fatal("nothing is on the slot yet")
	}
	if !f.proxy.AssignTask("a", 0, 7) {
		t.Fatal("assign")
	}
	if !f.proxy.Cancel("a", 0) {
		t.Fatal("cancel")
	}
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Log("stream ended cleanly") // either way the connection is over
	}
	r := f.sink.wait(t, 1)[0]
	if r.StatusClass != "200 cancelled" || r.Slot == nil || *r.Slot != 0 || r.Task == nil || *r.Task != 7 {
		t.Fatalf("record %+v", r)
	}
	expectIdle(t, f)
}

func TestContextOverflowClassified(t *testing.T) {
	f := newFixture(t, func(o *fakellama.Options) { o.Ctx = 2 }, Options{})
	resp := f.post("/v1/chat/completions", chat+`,"stream":true}`)
	body := drain(t, resp)
	if resp.StatusCode != 400 || !strings.Contains(body, "exceed_context_size_error") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	r := f.sink.wait(t, 1)[0]
	if r.Status != 400 || r.StatusClass != "400 ctx" {
		t.Fatalf("record %+v", r)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{400, `{"error":{"code":400,"message":"request (5 tokens) exceeds the available context size (2 tokens)","type":"exceed_context_size_error"}}`, "400 ctx"},
		{400, `{"error":{"message":"the request exceeds the available context size"}}`, "400 ctx"},
		{503, `{"error":{"code":503,"message":"Loading model","type":"unavailable_error"}}`, "503 loading"},
		{503, `{"error":{"code":503,"message":"no slot available","type":"unavailable_error"}}`, "503 busy"},
		{503, `Service Unavailable`, "503 busy"},
		{400, `{"error":{"code":400,"message":"model 'a' is not loaded","type":"invalid_request_error"}}`, "400 not loaded"},
		{400, `{"error":{"code":400,"message":"model 'nope' not found","type":"invalid_request_error"}}`, "400 unknown model"},
		{500, `{"error":"boom"}`, "500"},
		{404, `File Not Found`, "404"},
		{500, `not json`, "500"},
		{500, ``, "500"},
		{200, `{"error":{"code":400,"message":"context size exceeded"}}`, "400 ctx"},
		{200, `{"code":500,"message":"boom"}`, "500"},
	}
	for _, c := range cases {
		if got := classify(c.status, []byte(c.body)); got != c.want {
			t.Errorf("classify(%d, %s) = %q, want %q", c.status, c.body, got, c.want)
		}
	}
}

func TestUpstreamDown(t *testing.T) {
	sink := &memSink{}
	check := func(t *testing.T, h http.Handler) {
		t.Helper()
		front := httptest.NewServer(h)
		defer front.Close()
		resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json", strings.NewReader(chat+`}`))
		if err != nil {
			t.Fatal(err)
		}
		body := drain(t, resp)
		if resp.StatusCode != 503 || resp.Header.Get("Retry-After") == "" {
			t.Fatalf("%d, headers %v", resp.StatusCode, resp.Header)
		}
		var e struct {
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(body), &e); err != nil || e.Error.Code != 503 || e.Error.Type == "" || e.Error.Message == "" {
			t.Fatalf("shape %s: %v", body, err)
		}
	}
	quiet := slog.New(slog.DiscardHandler)
	t.Run("not running", func(t *testing.T) {
		check(t, New(Options{Upstream: func() (*url.URL, bool) { return nil, false }, Sink: sink, Logger: quiet}))
		if r := sink.wait(t, 1)[0]; r.Status != 503 || r.StatusClass != "503 down" {
			t.Fatalf("record %+v", r)
		}
	})
	t.Run("refused", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		u, err := url.Parse(dead.URL)
		if err != nil {
			t.Fatal(err)
		}
		dead.Close()
		check(t, New(Options{Upstream: func() (*url.URL, bool) { return u, true }, Sink: sink, Logger: quiet}))
		if r := sink.wait(t, 2)[1]; r.Status != 503 || r.StatusClass != "503 down" || r.Model != "a" {
			t.Fatalf("record %+v", r)
		}
	})
}

// recording answers 200 and keeps what it was sent.
type recording struct {
	mu    sync.Mutex
	query string
	body  []byte
	ct    string
	aenc  string
}

func (u *recording) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.query, u.body, u.ct, u.aenc = r.URL.RawQuery, b, r.Header.Get("Content-Type"), r.Header.Get("Accept-Encoding")
	_, _ = io.WriteString(w, `{}`)
}

func (u *recording) snapshot() (string, []byte, string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.query, u.body, u.ct
}

func recordingFront(t *testing.T, opts Options) (*recording, *httptest.Server) {
	t.Helper()
	rec := &recording{}
	up := httptest.NewServer(rec)
	t.Cleanup(up.Close)
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	opts.Upstream = func() (*url.URL, bool) { return u, true }
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	front := httptest.NewServer(New(opts))
	t.Cleanup(front.Close)
	return rec, front
}

func TestForwardsExactBytes(t *testing.T) {
	rec, front := recordingFront(t, Options{})
	sent := "  {\n \"zeta\": 1,   \"model\" : \"a\",\t\"alpha\":[1, 2]}\n\n"
	resp, err := http.Post(front.URL+"/v1/completions", "application/json; charset=utf-8", strings.NewReader(sent))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, resp)
	_, body, ct := rec.snapshot()
	if string(body) != sent || ct != "application/json; charset=utf-8" {
		t.Fatalf("body %q, type %q", body, ct)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.aenc != "" {
		t.Fatalf("Accept-Encoding %q reached upstream: a compressed body cannot be measured", rec.aenc)
	}
}

func TestChunkedRequestBody(t *testing.T) {
	sink := &memSink{}
	rec, front := recordingFront(t, Options{Sink: sink})
	req, err := http.NewRequest(http.MethodPost, front.URL+"/v1/completions", io.NopCloser(strings.NewReader(`{"model":"a"}`)))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, resp)
	if _, body, _ := rec.snapshot(); string(body) != `{"model":"a"}` {
		t.Fatalf("body %q", body)
	}
	if r := sink.wait(t, 1)[0]; r.Model != "a" {
		t.Fatalf("record %+v", r)
	}
}

func TestBodyTooLarge(t *testing.T) {
	sink := &memSink{}
	rec, front := recordingFront(t, Options{Sink: sink, MaxBody: 16})
	resp, err := http.Post(front.URL+"/v1/completions", "application/json", strings.NewReader(`{"model":"a","prompt":"far too long"}`))
	if err != nil {
		t.Fatal(err)
	}
	body := drain(t, resp)
	if resp.StatusCode != 413 || !strings.Contains(body, `"code":413`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if _, got, _ := rec.snapshot(); got != nil {
		t.Fatal("reached upstream")
	}
	if r := sink.wait(t, 1)[0]; r.Status != 413 || r.StatusClass != "413" {
		t.Fatalf("record %+v", r)
	}
}

func TestMultipartPassesThroughUnread(t *testing.T) {
	sink := &memSink{}
	rec, front := recordingFront(t, Options{Sink: sink, Policy: allow{"a": true}, MaxBody: 16})
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("model", "a"); err != nil {
		t.Fatal(err)
	}
	part, err := mw.CreateFormFile("file", "a.wav")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("x"), 1000)); err != nil { // past MaxBody: never applied
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	sent := buf.String()
	resp, err := http.Post(front.URL+"/v1/audio/transcriptions", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if _, body, _ := rec.snapshot(); string(body) != sent {
		t.Fatal("body changed")
	}
	if r := sink.wait(t, 1)[0]; r.Endpoint != "/v1/audio/transcriptions" || r.Status != 200 {
		t.Fatalf("record %+v", r)
	}
}

func TestAutoloadPolicy(t *testing.T) {
	cases := []struct {
		name, path, body string
		allowed          bool
		want             string
	}{
		{"allowed", "/v1/chat/completions", `{"model":"a"}`, true, "autoload=true"},
		{"denied", "/v1/chat/completions", `{"model":"a"}`, false, ""},
		{"client cannot override", "/v1/chat/completions?autoload=true", `{"model":"a"}`, false, ""},
		{"keeps other params", "/v1/chat/completions?x=1", `{"model":"a"}`, true, "x=1&autoload=true"},
		{"no model", "/v1/chat/completions", `{}`, true, ""},
		{"management", "/slots/0?action=erase", `{"model":"a"}`, true, "action=erase"},
		{"models load", "/models/load", `{"model":"a"}`, true, ""},
		{"unmeasured model route", "/tokenize", `{"model":"a"}`, true, "autoload=true"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, front := recordingFront(t, Options{Policy: allow{"a": c.allowed}})
			resp, err := http.Post(front.URL+c.path, "application/json", strings.NewReader(c.body))
			if err != nil {
				t.Fatal(err)
			}
			drain(t, resp)
			if q, _, _ := rec.snapshot(); q != c.want {
				t.Fatalf("query %q, want %q", q, c.want)
			}
		})
	}
}

func TestOnDemandAgainstFake(t *testing.T) {
	f := newFixture(t, nil, Options{Policy: allow{"b": false}})
	resp := f.post("/v1/chat/completions?autoload=true", `{"model":"b","prompt":"x"}`)
	if body := drain(t, resp); resp.StatusCode != 400 || !strings.Contains(body, "not loaded") {
		t.Fatalf("denied: %d %s", resp.StatusCode, body)
	}
	if r := f.sink.wait(t, 1)[0]; r.StatusClass != "400 not loaded" {
		t.Fatalf("record %+v", r)
	}

	g := newFixture(t, nil, Options{Policy: allow{"b": true}})
	resp = g.post("/v1/chat/completions", `{"model":"b","prompt":"x","max_tokens":1}`)
	if body := drain(t, resp); resp.StatusCode != 200 {
		t.Fatalf("allowed: %d %s", resp.StatusCode, body)
	}
}

func TestWebSocketPassthrough(t *testing.T) {
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "want upgrade", http.StatusBadRequest)
			return
		}
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = rw.Flush()
		buf := make([]byte, 64)
		for {
			n, err := rw.Read(buf)
			if err != nil {
				return
			}
			if _, err := conn.Write(buf[:n]); err != nil {
				return
			}
		}
	})
	up := httptest.NewServer(echo)
	defer up.Close()
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	sink := &memSink{}
	front := httptest.NewServer(New(Options{Upstream: func() (*url.URL, bool) { return u, true }, Sink: sink, Logger: slog.New(slog.DiscardHandler)}))
	defer front.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "GET /v1/realtime HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 101 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, msg := range []string{"ping", "pong"} {
		if _, err := io.WriteString(conn, msg); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(msg))
		if _, err := io.ReadFull(br, got); err != nil {
			t.Fatal(err)
		}
		if string(got) != msg {
			t.Fatalf("echo %q", got)
		}
	}
	if sink.count() != 0 {
		t.Fatal("websocket measured")
	}
}

func TestCapture(t *testing.T) {
	t.Run("off", func(t *testing.T) {
		f := newFixture(t, nil, Options{})
		drain(t, f.post("/v1/chat/completions", chat+`,"max_tokens":2}`))
		f.sink.wait(t, 1)
		if len(f.sink.bodies) != 0 {
			t.Fatalf("captured %d bodies", len(f.sink.bodies))
		}
	})
	t.Run("on", func(t *testing.T) {
		var logs bytes.Buffer
		f := newFixture(t, nil, Options{Capture: true, MaxBodyCapture: 20, Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
		resp := f.post("/v1/chat/completions", chat+`,"max_tokens":2}`)
		out := drain(t, resp)
		r := f.sink.wait(t, 1)[0]
		f.sink.mu.Lock()
		defer f.sink.mu.Unlock()
		if len(f.sink.bodies) != 1 {
			t.Fatalf("bodies %d", len(f.sink.bodies))
		}
		b := f.sink.bodies[0]
		if b.RequestID != r.ID || string(b.Request) != (chat + `,"max_tokens":2}`)[:20] || string(b.Response) != out[:20] {
			t.Fatalf("body %+v (record %d)", b, r.ID)
		}
		if strings.Contains(logs.String(), "secret prompt") {
			t.Fatal("prompt logged")
		}
	})
}

func TestConcurrency(t *testing.T) {
	const n = 24
	f := newFixture(t, func(o *fakellama.Options) { o.Tokens = 4 }, Options{})
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream := "true"
			if i%2 == 0 {
				stream = "false"
			}
			resp, err := http.Post(f.front.URL+"/v1/chat/completions", "application/json",
				strings.NewReader(fmt.Sprintf(`{"model":"a","prompt":"p%d","stream":%s}`, i, stream)))
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = resp.Body.Close() }()
			if _, err := io.Copy(io.Discard, resp.Body); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	records := f.sink.wait(t, n)
	seen := map[uint64]bool{}
	for _, r := range records {
		if r.Status != 200 || r.Timings.PredictedN != 4 || seen[r.ID] {
			t.Fatalf("record %+v", r)
		}
		seen[r.ID] = true
	}
	if f.proxy.InFlight("a") != 0 {
		t.Fatal("left in flight")
	}
}

func TestAssignTaskFIFO(t *testing.T) {
	var reg registry
	cancelled := map[string]bool{}
	add := func(name, model string) *entry {
		return reg.add(model, "/e", time.Now(), func() { cancelled[name] = true })
	}
	r1 := add("r1", "a")
	add("other", "b")
	r2 := add("r2", "a")

	if !reg.assign("a", 1, 100) {
		t.Fatal("first")
	}
	if *r1.Slot != 1 || *r1.Task != 100 {
		t.Fatalf("oldest not assigned: %+v", r1)
	}
	if reg.assign("a", 1, 100) {
		t.Fatal("same task twice")
	}
	if r2.Task != nil {
		t.Fatal("repeat reached r2")
	}
	if !reg.assign("a", 0, 101) || *r2.Slot != 0 || *r2.Task != 101 {
		t.Fatalf("second: %+v", r2)
	}
	if reg.assign("a", 2, 102) {
		t.Fatal("nobody left to assign to")
	}

	if reg.cancel("a", 2) || reg.cancel("b", 1) {
		t.Fatal("cancelled the wrong thing")
	}
	if !reg.cancel("a", 0) || !cancelled["r2"] || cancelled["r1"] {
		t.Fatalf("cancelled %v", cancelled)
	}

	// A new request lands on a slot whose old task's request is still finishing.
	r3 := add("r3", "a")
	if !reg.assign("a", 1, 103) || *r3.Slot != 1 {
		t.Fatalf("r3 %+v", r3)
	}
	if r1.Slot != nil {
		t.Fatal("slot still held by the finished request")
	}
	if !reg.cancel("a", 1) || !cancelled["r3"] || cancelled["r1"] {
		t.Fatalf("cancelled %v", cancelled)
	}

	list := reg.list()
	if len(list) != 4 || list[2].Slot == nil {
		t.Fatalf("list %+v", list)
	}
	*list[2].Slot = 99
	if *r2.Slot == 99 {
		t.Fatal("list leaked internals")
	}
	reg.remove(r1)
	if reg.count("a") != 2 || reg.count("b") != 1 {
		t.Fatalf("count a=%d b=%d", reg.count("a"), reg.count("b"))
	}
}

func TestMeterSSEShapes(t *testing.T) {
	cases := []struct {
		name   string
		lines  []string
		tokens int
		class  string
	}{
		{"chat with role chunk", []string{
			`data: {"choices":[{"delta":{"role":"assistant","content":null}}]}`,
			`data: {"choices":[{"delta":{"content":""}}]}`,
			`data: {"choices":[{"delta":{"content":"a"}}]}`,
			`data: {"choices":[{"delta":{"reasoning_content":"b"}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"id":"x"}]}}]}`,
			`data: [DONE]`,
		}, 3, "200"},
		{"completion", []string{`data: {"choices":[{"text":"a"}]}`, `data: {"choices":[{"text":""}]}`}, 1, "200"},
		{"native", []string{`data: {"content":"a","stop":false}`, `data: {"content":"","stop":true}`}, 1, "200"},
		{"anthropic", []string{
			`event: message_start`, `data: {"type":"message_start","message":{"content":[]}}`,
			`event: content_block_delta`, `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"a"}}`,
			`event: message_delta`, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		}, 1, "200"},
		{"responses", []string{
			`data: {"type":"response.created"}`,
			`data: {"type":"response.output_text.delta","delta":"a"}`,
			`data: {"type":"response.output_text.delta","delta":""}`,
		}, 1, "200"},
		{"content of another type", []string{`data: {"content":[1],"choices":[{"text":"a"}]}`}, 1, "200"},
		{"junk", []string{`data: {not json`, `: comment`, `data: 5`, `retry: 10`}, 0, "200"},
		{"mid-stream error object", []string{
			`data: {"choices":[{"text":"a"}]}`,
			`data: {"error":{"code":500,"message":"boom","type":"server_error"}}`,
		}, 1, "500"},
		{"mid-stream error event", []string{`error: {"code":400,"message":"context size exceeded"}`}, 0, "400 ctx"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			now := time.Unix(100, 0)
			clock := func() time.Time { return now }
			m := meter{started: now, now: clock}
			m.begin(200, true)
			// One byte at a time: chunk boundaries must not matter.
			raw := strings.Join(c.lines, "\n") + "\n\n"
			body := &meteredBody{ReadCloser: io.NopCloser(strings.NewReader(raw)), m: &m}
			buf := make([]byte, 1)
			for {
				now = now.Add(time.Millisecond)
				if _, err := body.Read(buf); err != nil {
					break
				}
			}
			m.settle(false)
			got := 0
			if m.sawToken {
				got = 1 + len(m.gaps)
			}
			if got != c.tokens || m.class != c.class {
				t.Fatalf("tokens %d, class %q", got, m.class)
			}
			if c.tokens > 0 && m.result(now).TTFTMs <= 0 {
				t.Fatal("no TTFT")
			}
		})
	}
}

func TestMeterGapsAndBounds(t *testing.T) {
	now := time.Unix(100, 0)
	m := meter{started: now, now: func() time.Time { return now }}
	m.begin(200, true)
	for range 200 {
		now = now.Add(2 * time.Millisecond)
		m.observe([]byte("data: {\"content\":\"x\"}\n\n"), now)
	}
	if len(m.gaps) != maxGaps || m.gaps[0] != 2 {
		t.Fatalf("gaps %d, first %v", len(m.gaps), m.gaps[0])
	}
	m.observe(bytes.Repeat([]byte("y"), maxSSELine+10), now)
	if len(m.line) > maxSSELine || !m.lineOver {
		t.Fatalf("line %d", len(m.line))
	}
	m.observe([]byte("\ndata: {\"timings\":{\"predicted_n\":7}}\n"), now)
	if m.timings.PredictedN != 7 {
		t.Fatal("did not recover after a long line")
	}

	j := meter{started: now, now: func() time.Time { return now }}
	j.begin(200, false)
	j.observe(bytes.Repeat([]byte("z"), maxJSONBody+1), now)
	j.observe([]byte(`{"timings":{"predicted_n":7}}`), now)
	if j.buf != nil || !j.bufOver {
		t.Fatal("JSON buffer unbounded")
	}
}

func TestSettleUpstreamAbort(t *testing.T) {
	m := meter{started: time.Unix(0, 0)}
	m.begin(200, true)
	m.readErr = errors.New("unexpected EOF")
	m.settle(false)
	if m.class != "200 aborted" {
		t.Fatalf("class %q", m.class)
	}
}

func TestStoreSink(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	sink := NewStoreSink(st, slog.New(slog.DiscardHandler), 16)

	f := newFixture(t, nil, Options{Sink: sink, Capture: true, MaxBodyCapture: 1024})
	out := drain(t, f.post("/v1/chat/completions", chat+`,"stream":true,"max_tokens":3}`))
	bad := f.post("/nothing", `{}`)
	drain(t, bad)
	f.front.Close() // waits for handlers, so the record has been queued
	if err := sink.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	rows, _, err := st.ListRequests(context.Background(), store.RequestFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows %d", len(rows))
	}
	r := rows[0]
	if r.Model != "a" || r.Endpoint != "/v1/chat/completions" || r.Status != 200 || !r.Stream || r.PredictedN != 3 || r.Error != "" || len(r.Gaps) == 0 || r.TTFTMs <= 0 {
		t.Fatalf("row %+v", r)
	}
	var gaps []float64
	if err := json.Unmarshal(r.Gaps, &gaps); err != nil || len(gaps) != 2 {
		t.Fatalf("gaps %s: %v", r.Gaps, err)
	}
	req, resp, err := st.GetRequestBody(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(req), "secret prompt") || string(resp) != out[:min(len(out), 1024)] {
		t.Fatalf("bodies %q %q", req, resp)
	}
}

func TestToStoreError(t *testing.T) {
	cases := []struct {
		status int
		class  string
		want   string
	}{{200, "200", ""}, {400, "400 ctx", "400 ctx"}, {500, "500", "500"}, {200, "200 cancelled", "200 cancelled"}}
	for _, c := range cases {
		if got := toStore(RequestRecord{Status: c.status, StatusClass: c.class}).Error; got != c.want {
			t.Errorf("%d %q: %q", c.status, c.class, got)
		}
	}
}

// blockedStore never finishes a write until released.
type blockedStore struct {
	release chan struct{}
	rows    atomic.Int64
}

func (b *blockedStore) InsertRequest(context.Context, store.Request) (int64, error) {
	<-b.release
	return b.rows.Add(1), nil
}

func (b *blockedStore) InsertRequestBody(context.Context, int64, []byte, []byte) error { return nil }

func TestStoreSinkDropsInsteadOfBlocking(t *testing.T) {
	st := &blockedStore{release: make(chan struct{})}
	var logs safeBuffer
	sink := NewStoreSink(st, slog.New(slog.NewTextHandler(&logs, nil)), 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			sink.RecordBody(Body{RequestID: uint64(i), Request: []byte("secret prompt")})
			sink.Record(RequestRecord{ID: uint64(i), Status: 200, StatusClass: "200"})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record blocked on a stalled store")
	}
	if !strings.Contains(logs.String(), "dropping") || strings.Contains(logs.String(), "secret") {
		t.Fatalf("logs %q", logs.String())
	}
	close(st.release)
	if err := sink.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := st.rows.Load(); n == 0 || n > 3 {
		t.Fatalf("rows %d", n)
	}
	if got := sink.queuedBytes.Load(); got != 0 {
		t.Fatalf("queued bytes leaked: %d", got)
	}
}

func TestStoreSinkBoundsBodyMemory(t *testing.T) {
	st := &blockedStore{release: make(chan struct{})}
	close(st.release)
	sink := NewStoreSink(st, slog.New(slog.DiscardHandler), 8)
	defer func() { _ = sink.Close(context.Background()) }()
	sink.RecordBody(Body{RequestID: 1, Request: make([]byte, maxQueuedBodyBytes+1)})
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.waiting) != 0 || sink.queuedBytes.Load() != 0 {
		t.Fatal("oversized body kept")
	}
}

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestWithAutoloadCannotBeBypassed(t *testing.T) {
	for _, tc := range []struct {
		query string
		allow bool
		want  string
	}{
		{"%61utoload=true", false, ""},
		{"autoload=true&x=a;b", false, ""},
		{"x=1&%61utoload=true", false, "x=1"},
		{"x=1", true, "x=1&autoload=true"},
		{"", true, "autoload=true"},
		{"", false, ""},
	} {
		if got := withAutoload(tc.query, tc.allow); got != tc.want {
			t.Errorf("withAutoload(%q, %v) = %q, want %q", tc.query, tc.allow, got, tc.want)
		}
	}
}

func TestPaused(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer up.Close()
	target, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	sink := &memSink{}
	p := New(Options{
		Upstream: func() (*url.URL, bool) { return target, true },
		Sink:     sink,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	send := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`)))
		return w
	}

	p.SetPaused("paused for a benchmark")
	w := send()
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != pausedRetryAfter {
		t.Errorf("%d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	if !strings.Contains(w.Body.String(), "paused for a benchmark") {
		t.Errorf("body %s", w.Body)
	}
	if hits.Load() != 0 {
		t.Errorf("reached upstream %d times", hits.Load())
	}
	if recs := sink.wait(t, 1); recs[0].Status != http.StatusServiceUnavailable {
		t.Errorf("record %+v", recs[0])
	}

	p.SetPaused("")
	if w := send(); w.Code != http.StatusNoContent || hits.Load() != 1 {
		t.Errorf("after resuming: %d, %d hits", w.Code, hits.Load())
	}
}
