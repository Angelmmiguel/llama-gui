// Package proxy is the reverse proxy in front of the router. It measures every
// completion-type request as the response flows past, without buffering it.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultMaxBody    = 64 << 20
	defaultMaxCapture = 1 << 20
	retryAfterSeconds = "5"
	pausedRetryAfter  = "30" // a pause outlasts a restart by minutes
	statusCancelled   = 499  // no status exists when the request ends before upstream answers
)

// Policy decides whether a request may load a model on demand.
type Policy interface {
	Allow(model string) bool
}

// Options configures a Proxy. Upstream is required.
type Options struct {
	Upstream       func() (*url.URL, bool) // false while the router is not running
	Sink           Sink                    // nil drops the measurements
	Policy         Policy                  // nil never loads on demand
	Touch          func(model string)      // called as measured requests start and end
	Capture        bool                    // keep request and response bodies
	MaxBodyCapture int                     // per body; 0 means 1 MiB
	MaxBody        int64                   // largest request body read; 0 means 512 MiB
	// AllowedOrigins may call inference routes from a browser on another site. "*" means any.
	AllowedOrigins []string
	Clock          func() time.Time
	Logger         *slog.Logger
}

// Proxy is an http.Handler forwarding to the router.
type Proxy struct {
	opts Options
	rp   *httputil.ReverseProxy
	log  *slog.Logger
	reg  registry
	// paused holds why requests are being refused, nil while they are not.
	paused atomic.Pointer[string]
}

type ctxKey struct{}

// exchange is one request's state. Only the handler goroutine touches it:
// ReverseProxy calls Rewrite and ModifyResponse and reads the body from there.
type exchange struct {
	upstream *url.URL
	autoload bool
	measured bool
	meter
}

// New returns a Proxy.
func New(opts Options) *Proxy {
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.MaxBodyCapture <= 0 {
		opts.MaxBodyCapture = defaultMaxCapture
	}
	if opts.MaxBody <= 0 {
		opts.MaxBody = defaultMaxBody
	}
	if opts.Touch == nil {
		opts.Touch = func(string) {}
	}
	p := &Proxy{opts: opts, log: opts.Logger}
	transport, _ := http.DefaultTransport.(*http.Transport)
	transport = transport.Clone()
	transport.Proxy = nil
	transport.MaxIdleConnsPerHost = 64
	transport.DisableCompression = true
	p.rp = &httputil.ReverseProxy{
		Rewrite:        p.rewrite,
		ModifyResponse: p.modifyResponse,
		ErrorHandler:   p.errorHandler,
		Transport:      transport,
		FlushInterval:  -1,
		ErrorLog:       slog.NewLogLogger(opts.Logger.Handler(), slog.LevelWarn),
	}
	return p
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.refuseCrossSite(w, r) {
		return
	}
	ex := &exchange{measured: isMeasured(r)}
	ex.meter = meter{started: p.opts.Clock(), now: p.opts.Clock, capture: p.opts.Capture, maxCapture: p.opts.MaxBodyCapture}

	if reason := p.paused.Load(); reason != nil {
		ex.answered(http.StatusServiceUnavailable, "503 paused")
		w.Header().Set("Retry-After", pausedRetryAfter)
		writeError(w, http.StatusServiceUnavailable, "unavailable_error", *reason)
		p.record(r, ex, nil)
		return
	}

	upstream, ok := p.opts.Upstream()
	if !ok {
		ex.answered(http.StatusServiceUnavailable, "503 down")
		writeDown(w)
		p.record(r, ex, nil)
		return
	}
	ex.upstream = upstream

	if readsBody(r) {
		body, err := p.readBody(w, r)
		if err != nil {
			var tooBig *http.MaxBytesError
			status, msg := http.StatusBadRequest, "cannot read request body"
			if errors.As(err, &tooBig) {
				status, msg = http.StatusRequestEntityTooLarge, "request body too large"
			}
			ex.answered(status, strconv.Itoa(status))
			writeError(w, status, "invalid_request_error", msg)
			p.record(r, ex, nil)
			return
		}
		var head struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.Unmarshal(body, &head) // whatever this is, upstream gets to reject it
		ex.model, ex.stream = head.Model, head.Stream
		if p.opts.Policy != nil && head.Model != "" && !isManagement(r.URL.Path) {
			ex.autoload = p.opts.Policy.Allow(head.Model)
		}
		if ex.measured && p.opts.Capture {
			ex.reqCapture = bytes.Clone(body[:min(len(body), p.opts.MaxBodyCapture)])
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.TransferEncoding = nil
		r.GetBody = nil
	}
	if ex.model == "" {
		ex.model = r.URL.Query().Get("model")
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(context.WithValue(ctx, ctxKey{}, ex))

	if ex.measured {
		// The upload is the client's time, so the clock starts here.
		ex.started = p.opts.Clock()
		ex.entry = p.reg.add(ex.model, r.URL.Path, ex.started, cancel)
		ex.onFirstByte = func() { p.reg.responding(ex.entry) }
		p.touch(ex.model)
		defer func() {
			slot, task := p.reg.remove(ex.entry)
			p.touch(ex.model)
			ex.settle(ctx.Err() != nil)
			p.record(r, ex, &assignment{slot, task})
		}()
	}
	p.rp.ServeHTTP(w, r)
}

func (p *Proxy) touch(model string) {
	if model != "" {
		p.opts.Touch(model)
	}
}

func (p *Proxy) readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	return io.ReadAll(http.MaxBytesReader(w, r.Body, p.opts.MaxBody))
}

func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	ex, _ := pr.In.Context().Value(ctxKey{}).(*exchange)
	pr.SetURL(ex.upstream)
	pr.Out.URL.RawQuery = withAutoload(pr.Out.URL.RawQuery, ex.autoload)
	if ex.measured {
		pr.Out.Header.Del("Accept-Encoding") // we have to read what comes back
	}
}

// withAutoload makes the proxy's decision the only one upstream sees: a client's own
// autoload=true must not get around a profile that forbids on-demand loading.
func withAutoload(rawQuery string, allow bool) string {
	if rawQuery != "" {
		// Always parsed, because upstream percent-decodes keys: %61utoload is autoload too.
		// A query that does not parse is dropped rather than forwarded for upstream to read.
		q, err := url.ParseQuery(rawQuery)
		if err != nil {
			q = url.Values{}
		}
		q.Del("autoload")
		rawQuery = q.Encode()
	}
	if !allow {
		return rawQuery
	}
	if rawQuery == "" {
		return "autoload=true"
	}
	return rawQuery + "&autoload=true"
}

func (p *Proxy) modifyResponse(resp *http.Response) error {
	stripCORS(resp.Header)
	ex, _ := resp.Request.Context().Value(ctxKey{}).(*exchange)
	if ex == nil || !ex.measured {
		return nil
	}
	if resp.StatusCode == http.StatusSwitchingProtocols {
		ex.answered(resp.StatusCode, strconv.Itoa(resp.StatusCode))
		return nil // ReverseProxy needs the body to stay writable
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	ex.begin(resp.StatusCode, ct == "text/event-stream")
	resp.Body = &meteredBody{ReadCloser: resp.Body, m: &ex.meter}
	return nil
}

func (p *Proxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	ex, _ := r.Context().Value(ctxKey{}).(*exchange)
	if r.Context().Err() != nil {
		// Client gone or slot cancelled. settle classifies it; nobody reads this answer.
		w.WriteHeader(statusCancelled)
		return
	}
	p.log.Warn("upstream unreachable", "error", err)
	if ex != nil {
		ex.answered(http.StatusServiceUnavailable, "503 down")
	}
	writeDown(w)
}

func writeDown(w http.ResponseWriter) {
	w.Header().Set("Retry-After", retryAfterSeconds)
	writeError(w, http.StatusServiceUnavailable, "unavailable_error", "llama-server is not running")
}

// writeError answers in the shape of upstream's own errors.
func writeError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": status, "message": msg, "type": typ}})
}

func (p *Proxy) record(r *http.Request, ex *exchange, a *assignment) {
	if p.opts.Sink == nil || !ex.measured {
		return
	}
	rec := ex.result(p.opts.Clock())
	if ex.entry != nil {
		rec.ID = ex.entry.ID
	} else {
		rec.ID = p.reg.nextID()
	}
	rec.Method, rec.Endpoint = r.Method, r.URL.Path
	if a != nil {
		rec.Slot, rec.Task = a.slot, a.task
	}
	if len(ex.reqCapture)+len(ex.respCapture) > 0 {
		p.opts.Sink.RecordBody(Body{RequestID: rec.ID, Request: ex.reqCapture, Response: ex.respCapture})
	}
	p.opts.Sink.Record(rec)
}

// measuredPaths are the endpoints that do inference. Everything else, such as
// /slots and /metrics, passes through silently.
var measuredPaths = map[string]bool{
	"/v1/chat/completions": true, "/chat/completions": true, "/v1/completions": true, "/completion": true,
	"/completions": true, "/v1/messages": true, "/v1/responses": true, "/v1/embeddings": true,
	"/embeddings": true, "/embedding": true, "/infill": true, "/rerank": true, "/v1/rerank": true, "/tts": true,
}

func isMeasured(r *http.Request) bool {
	return r.Method == http.MethodPost && isInference(r.URL.Path)
}

// isInference excludes dot segments because upstream may resolve them: an allowed
// origin must not reach /models/load through /v1/audio/../../models/load.
func isInference(path string) bool {
	return measuredPaths[path] || (strings.HasPrefix(path, "/v1/audio/") && !strings.Contains(path, ".."))
}

// isManagement says the path acts on the router, not through a model, so it must not load one.
func isManagement(path string) bool {
	for _, prefix := range []string{"/models", "/slots"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// readsBody says a POST carries JSON. An absent or wrong Content-Type still counts,
// since upstream parses JSON regardless and curl -d sends a form type. Uploads don't.
func readsBody(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return !strings.HasPrefix(ct, "multipart/") && !strings.HasPrefix(ct, "audio/") && ct != "application/octet-stream"
}

// SetPaused makes the proxy answer every request with a 503, Retry-After and this
// reason, until it is called with an empty one. Requests already under way go on.
func (p *Proxy) SetPaused(reason string) {
	if reason == "" {
		p.paused.Store(nil)
		return
	}
	p.paused.Store(&reason)
}

// InFlight returns the requests in progress for a model, for the supervisor's idle check.
func (p *Proxy) InFlight(model string) int { return p.reg.count(model) }

// List returns the requests in progress, oldest first.
func (p *Proxy) List() []Inflight { return p.reg.list() }

// AssignTask records that a new task appeared on a model's slot. Call it with
// tasks of slots that are processing: id_task stays set after a task finishes.
// Best effort: the task goes to the model's oldest request without one. It
// reports whether anything was assigned; a pair already known is not.
func (p *Proxy) AssignTask(model string, slot int, task int64) bool {
	return p.reg.assign(model, slot, task)
}

// Cancel closes the request occupying a slot, which stops its generation upstream.
// It reports whether one was found.
func (p *Proxy) Cancel(model string, slot int) bool { return p.reg.cancel(model, slot) }
