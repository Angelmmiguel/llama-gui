package fakellama

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed assets/props.json
var propsJSON []byte

const (
	stateUnloaded = "unloaded"
	stateLoading  = "loading"
	stateLoaded   = "loaded"

	binary       = "/llama/llama-server"
	defaultSlots = 2
	baseModel    = 173263104
)

type slot struct {
	id                                   int
	nCtx                                 int
	processing, hasTask                  bool
	task                                 int
	nPrompt, nProcessed, nCached, nDecod int
	prev                                 []string // words of the last prompt, for cache reuse
	params                               map[string]any
}

type counters struct {
	promptTokens, cachedTokens, predicted, decodes, maxTokens float64
	promptSeconds, predictedSeconds                           float64
}

type model struct {
	presetModel
	state    string
	port     int
	gen      int // bumped on every load and unload so stale loaders back off
	seq      int64
	started  time.Time
	ready    chan struct{}
	slots    []*slot
	ctx      int
	gpu      int // where it landed, with FAKE_LLAMA_GPUS
	counters counters
}

// Server is the fake router.
type Server struct {
	opts Options
	log  *logger

	mu       sync.Mutex // guards every model, slot and counter
	models   []*model
	byID     map[string]*model
	nextPort int
	seq      int64
	task     atomic.Int64
}

// New builds the server; logOut receives the router's log lines.
func New(opts Options, logOut io.Writer) (*Server, error) {
	pm, err := loadPreset(opts.Preset)
	if err != nil {
		return nil, err
	}
	s := &Server{opts: opts, byID: map[string]*model{}, nextPort: 54000,
		log: &logger{w: logOut, start: time.Now(), jsonl: opts.JSONL}}
	for _, p := range pm {
		m := &model{presetModel: p, state: stateUnloaded, ctx: presetCtx(p)}
		s.models = append(s.models, m)
		s.byID[m.id] = m
	}
	return s, nil
}

func presetCtx(p presetModel) int { return presetInt(p, "ctx-size", 4096) }

// slotCount is the preset's parallel, as a real child would split its context.
func (m *model) slotCount() int { return presetInt(m.presetModel, "parallel", defaultSlots) }

func presetInt(p presetModel, key string, def int) int {
	for _, e := range p.kvs {
		if e.key == key {
			if n, err := strconv.Atoi(e.val); err == nil && n > 0 {
				return n
			}
		}
	}
	return def
}

// Banner logs the startup lines a router prints before serving.
func (s *Server) Banner(addr string) {
	l := s.log
	l.router("info", srvMsg("llama_server", "initializing ..."))
	l.router("info", "cmn  common_param: common_params_print_info: verbosity = 3 (adjust with the `-lv N` CLI arg)")
	l.router("info", srvMsg("server_run_r", "starting server in router mode. models will be automatically loaded on-demand"))
	l.router("info", srvMsg("operator()", fmt.Sprintf("Available models (%d):", len(s.models))))
	for _, m := range s.models {
		l.router("info", srvMsg("operator()", fmt.Sprintf("  [%10s] %s", "preset", m.id)))
	}
	l.router("warn", srvMsg("server_setup", "security: router mode enabled - do not expose to untrusted environments"))
	l.router("info", srvMsg("server_on_li", "listening on http://"+addr))
}

// Handler returns the HTTP surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /models", s.handleModels)
	mux.HandleFunc("POST /models/load", s.handleLoad)
	mux.HandleFunc("POST /models/unload", s.handleUnload)
	mux.HandleFunc("GET /v1/models", s.handleV1Models)
	mux.HandleFunc("GET /slots", s.handleSlots)
	mux.HandleFunc("POST /slots/{id}", s.handleSlotAction)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /props", s.handleProps)
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) { s.handleGenerate(w, r, kindChat) })
	mux.HandleFunc("POST /v1/completions", func(w http.ResponseWriter, r *http.Request) { s.handleGenerate(w, r, kindCompletion) })
	mux.HandleFunc("POST /infill", func(w http.ResponseWriter, r *http.Request) { s.handleGenerate(w, r, kindInfill) })
	mux.HandleFunc("POST /v1/embeddings", s.handleEmbeddings)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, typ, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": status, "message": msg, "type": typ}})
}

func (s *Server) unknownModel(w http.ResponseWriter, id string) {
	writeErr(w, 400, "invalid_request_error", fmt.Sprintf("model '%s' not found", id))
}

// pick resolves a model id, falling back to the only model when none is named.
func (s *Server) pick(id string) *model {
	if id == "" && len(s.models) == 1 {
		return s.models[0]
	}
	return s.byID[id]
}

// --- lifecycle ---

func (s *Server) args(m *model) []string {
	args := []string{binary, "--host", s.opts.Host, "--port", strconv.Itoa(m.port)}
	for _, e := range m.kvs {
		args = append(args, "--"+e.key, e.val)
	}
	return args
}

// load starts a model if it is unloaded, evicting the least recently loaded one at --models-max.
func (s *Server) load(m *model) {
	s.mu.Lock()
	if m.state != stateUnloaded {
		s.mu.Unlock()
		return
	}
	if s.opts.ModelsMax > 0 {
		var active []*model
		for _, o := range s.models {
			if o.state != stateUnloaded {
				active = append(active, o)
			}
		}
		sort.Slice(active, func(i, j int) bool { return active[i].seq < active[j].seq })
		for len(active) >= s.opts.ModelsMax {
			s.unloadLocked(active[0])
			active = active[1:]
		}
	}
	s.seq++
	m.seq, m.state, m.gen = s.seq, stateLoading, m.gen+1
	m.port = s.nextPort
	s.nextPort++
	m.ready = make(chan struct{})
	gen, port, started := m.gen, m.port, time.Now()
	args := s.args(m)
	s.mu.Unlock()

	l := s.log
	l.router("info", srvMsg("load", fmt.Sprintf("spawning server instance with name=%s on port %d", m.id, port)))
	l.router("info", srvMsg("load", "spawning server instance with args:"))
	for _, a := range args {
		l.router("info", srvMsg("load", "  "+a))
	}
	go func() {
		l.child(port, started, "info", srvMsg("llama_server", "initializing ..."))
		l.child(port, started, "info", srvMsg("load_model", fmt.Sprintf("loading model '%s'", m.id)))
		time.Sleep(s.opts.LoadDelay)
		s.mu.Lock()
		if m.gen != gen {
			s.mu.Unlock()
			return
		}
		m.gpu = s.emptiestGPU()
		m.state, m.started, m.counters = stateLoaded, started, counters{}
		m.slots = nil
		n := m.slotCount()
		for i := range n {
			m.slots = append(m.slots, &slot{id: i, nCtx: m.ctx / n})
		}
		close(m.ready)
		s.mu.Unlock()
		l.child(port, started, "info", srvMsg("load_model", fmt.Sprintf("initializing, n_slots = %d, n_ctx_slot = %d, kv_unified = 'false'", n, m.ctx/n)))
		l.child(port, started, "info", srvMsg("server_run_i", "model loaded"))
		l.child(port, started, "info", srvMsg("server_on_li", fmt.Sprintf("listening on http://%s:%d", s.opts.Host, port)))
	}()
}

func (s *Server) unloadLocked(m *model) {
	wasLoaded := m.state == stateLoaded
	if m.state == stateLoading {
		close(m.ready)
	}
	port, started, id := m.port, m.started, m.id
	m.state, m.gen, m.port = stateUnloaded, m.gen+1, 0
	go func() {
		s.log.router("info", srvMsg("unload", "stopping model instance name="+id))
		if wasLoaded {
			s.log.child(port, started, "info", srvMsg("operator()", "exit command received, exiting..."))
		}
		s.log.router("info", srvMsg("run", fmt.Sprintf("instance name=%s exited with status 0", id)))
	}()
}

var errNotLoaded = errors.New("not loaded")

// ensureLoaded waits for the model to be loaded, loading it first when autoload
// allows: the router flag, unless the request has its own ?autoload=.
func (s *Server) ensureLoaded(r *http.Request, m *model) error {
	ctx := r.Context()
	s.mu.Lock()
	state := m.state
	s.mu.Unlock()
	if state == stateUnloaded {
		autoload := s.opts.Autoload
		if q := r.URL.Query().Get("autoload"); q != "" {
			autoload = q == "true"
		}
		if !autoload {
			return errNotLoaded
		}
		s.load(m)
	}
	s.mu.Lock()
	ready := m.ready
	s.mu.Unlock()
	if state != stateLoaded {
		select {
		case <-ready:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.state != stateLoaded {
		return errNotLoaded
	}
	return nil
}

func (s *Server) notLoaded(w http.ResponseWriter, m *model) {
	writeErr(w, 400, "invalid_request_error", fmt.Sprintf("model '%s' is not loaded", m.id))
}

// --- models ---

func hostDevice(model, ctx, compute int64) map[string]any {
	return map[string]any{"kind": "host", "name": "Host", "model": model, "context": ctx, "compute": compute}
}

// modelBytes is the size of the preset's model file, so a demo can show realistic
// memory from sparse files; anything unreadable gets the size of the real capture.
func modelBytes(m *model) int64 {
	for line := range strings.Lines(m.text) {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), "model = "); ok {
			if info, err := os.Stat(path); err == nil && info.Size() > 4096 {
				return info.Size()
			}
		}
	}
	return baseModel
}

// emptiestGPU picks where the next model goes, so a demo's models spread over its GPUs
// as they would on real ones rather than piling up past what one can hold.
func (s *Server) emptiestGPU() int {
	if s.opts.GPUs == 0 {
		return 0
	}
	used := make([]int64, s.opts.GPUs)
	for _, m := range s.models {
		if m.state == stateLoaded {
			used[m.gpu] += modelBytes(m)
		}
	}
	return slices.Index(used, slices.Min(used))
}

// devices places a model on the host or, with FAKE_LLAMA_GPUS, on an emulated GPU,
// as a real router reports it.
func (s *Server) devices(m *model, compute int64) []any {
	model, ctx := modelBytes(m), int64(m.ctx)*23040
	if s.opts.GPUs == 0 {
		return []any{hostDevice(model, ctx, compute)}
	}
	return []any{map[string]any{"kind": "device", "name": fmt.Sprintf("CUDA%d", m.gpu), "total": int64(24) << 30,
		"free": int64(24)<<30 - model, "model": model, "context": ctx, "compute": compute}}
}

func (s *Server) modelJSON(m *model) map[string]any {
	est := map[string]any{"n_ctx": m.ctx, "devices": s.devices(m, 110120960)}
	var effective any
	if m.state == stateLoaded {
		effective = map[string]any{"n_ctx": m.ctx, "devices": s.devices(m, 17451040)}
	}
	out := map[string]any{
		"id": m.id, "aliases": []string{}, "tags": []string{}, "object": "model", "owned_by": "llamacpp",
		"created":      1793914943,
		"status":       map[string]any{"value": m.state, "args": s.args(m), "preset": m.text},
		"architecture": map[string]any{"input_modalities": []string{"text"}, "output_modalities": []string{"text"}},
		"source":       "preset", "can_remove": false, "context_length": 8192,
		"memory": map[string]any{"status": "measured", "min": est, "max": est, "effective": effective},
	}
	if m.state == stateLoaded {
		out["meta"] = map[string]any{"n_ctx": m.ctx / m.slotCount(), "n_ctx_train": 8192, "size": baseModel}
	}
	return out
}

// reload re-reads the preset: new sections appear, and sections that went away
// disappear unless they are loaded. A running model keeps what it started with.
func (s *Server) reload() {
	pm, err := loadPreset(s.opts.Preset)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := map[string]bool{}
	for _, p := range pm {
		keep[p.id] = true
		if m, ok := s.byID[p.id]; ok {
			if m.state == stateUnloaded {
				m.presetModel = p
				m.ctx = presetCtx(p)
			}
			continue
		}
		m := &model{presetModel: p, state: stateUnloaded, ctx: presetCtx(p)}
		s.models = append(s.models, m)
		s.byID[m.id] = m
	}
	s.models = slices.DeleteFunc(s.models, func(m *model) bool {
		if keep[m.id] || m.state != stateUnloaded {
			return false
		}
		delete(s.byID, m.id)
		return true
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("reload") == "1" {
		s.reload()
	}
	s.mu.Lock()
	data := make([]any, 0, len(s.models))
	for _, m := range s.models {
		data = append(data, s.modelJSON(m))
	}
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"data": data, "object": "list"})
}

func (s *Server) handleV1Models(w http.ResponseWriter, r *http.Request) {
	data := make([]any, 0, len(s.models))
	for _, m := range s.models {
		data = append(data, map[string]any{"id": m.id, "object": "model", "created": 1793914943, "owned_by": "llamacpp"})
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}

func readJSON(r *http.Request) map[string]any {
	var body map[string]any
	_ = json.NewDecoder(io.LimitReader(r.Body, 32<<20)).Decode(&body)
	return body
}

func str(v any) string { s, _ := v.(string); return s }

func (s *Server) handleLoad(w http.ResponseWriter, r *http.Request) {
	id := str(readJSON(r)["model"])
	m := s.byID[id]
	if m == nil {
		s.unknownModel(w, id)
		return
	}
	s.load(m)
	writeJSON(w, 200, map[string]bool{"success": true})
}

func (s *Server) handleUnload(w http.ResponseWriter, r *http.Request) {
	id := str(readJSON(r)["model"])
	m := s.byID[id]
	if m == nil {
		s.unknownModel(w, id)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.state == stateUnloaded {
		s.notLoaded(w, m)
		return
	}
	s.unloadLocked(m)
	writeJSON(w, 200, map[string]bool{"success": true})
}

// --- per-model endpoints ---

// loadedFromQuery resolves ?model= for endpoints that never autoload.
func (s *Server) loadedFromQuery(w http.ResponseWriter, r *http.Request) *model {
	id := r.URL.Query().Get("model")
	m := s.pick(id)
	if m == nil {
		s.unknownModel(w, id)
		return nil
	}
	s.mu.Lock()
	state := m.state
	s.mu.Unlock()
	if state != stateLoaded {
		s.notLoaded(w, m)
		return nil
	}
	return m
}

func slotJSON(sl *slot) map[string]any {
	out := map[string]any{"id": sl.id, "n_ctx": sl.nCtx, "speculative": false, "is_processing": sl.processing}
	if !sl.hasTask {
		return out
	}
	out["id_task"] = sl.task
	out["n_prompt_tokens"] = sl.nPrompt
	out["n_prompt_tokens_processed"] = sl.nProcessed
	out["n_prompt_tokens_cache"] = sl.nCached
	if sl.params != nil {
		out["params"] = sl.params
	}
	if sl.processing {
		out["next_token"] = []any{map[string]any{"has_next_token": true, "has_new_line": false, "n_remain": 0, "n_decoded": sl.nDecod}}
	}
	return out
}

func (s *Server) handleSlots(w http.ResponseWriter, r *http.Request) {
	m := s.loadedFromQuery(w, r)
	if m == nil {
		return
	}
	s.mu.Lock()
	out := make([]any, 0, len(m.slots))
	for _, sl := range m.slots {
		out = append(out, slotJSON(sl))
	}
	s.mu.Unlock()
	writeJSON(w, 200, out)
}

func (s *Server) handleSlotAction(w http.ResponseWriter, r *http.Request) {
	body := readJSON(r)
	m := s.pick(str(body["model"]))
	if m == nil {
		s.unknownModel(w, str(body["model"]))
		return
	}
	s.mu.Lock()
	state := m.state
	s.mu.Unlock()
	if state != stateLoaded {
		s.notLoaded(w, m)
		return
	}
	n, err := strconv.Atoi(r.PathValue("id"))
	s.mu.Lock()
	var sl *slot
	if err == nil && n >= 0 && n < len(m.slots) {
		sl = m.slots[n]
	}
	s.mu.Unlock()
	if sl == nil {
		writeErr(w, 400, "invalid_request_error", "Invalid slot ID")
		return
	}
	action, filename := r.URL.Query().Get("action"), str(body["filename"])
	if action == "erase" {
		s.mu.Lock()
		erased := sl.nPrompt
		sl.nPrompt, sl.prev = 0, nil
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"id_slot": n, "n_erased": erased})
		return
	}
	if action != "save" && action != "restore" {
		writeErr(w, 400, "invalid_request_error", "Invalid action")
		return
	}
	if s.opts.SlotSavePath == "" {
		writeErr(w, 501, "not_supported_error", "This server does not support slots action. Start it with `--slot-save-path`")
		return
	}
	if filename == "" || filename != filepath.Base(filename) || strings.HasPrefix(filename, ".") {
		writeErr(w, 400, "invalid_request_error", "Invalid filename")
		return
	}
	path := filepath.Join(s.opts.SlotSavePath, filename)
	s.slotFile(w, action, n, filename, path, sl)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.opts.Metrics {
		writeErr(w, 501, "not_supported_error", "This server does not support metrics endpoint. Start it with `--metrics`")
		return
	}
	m := s.loadedFromQuery(w, r)
	if m == nil {
		return
	}
	s.mu.Lock()
	c, busy := m.counters, 0
	for _, sl := range m.slots {
		if sl.processing {
			busy++
		}
	}
	s.mu.Unlock()
	var b strings.Builder
	metric := func(kind, name, help string, v float64) {
		fmt.Fprintf(&b, "# HELP llamacpp:%s %s\n# TYPE llamacpp:%s %s\nllamacpp:%s %s\n", name, help, name, kind, name, strconv.FormatFloat(v, 'g', -1, 64))
	}
	rate := func(n, sec float64) float64 {
		if sec == 0 {
			return 0
		}
		return n / sec
	}
	metric("counter", "prompt_tokens_total", "Number of prompt tokens processed, excluding cached tokens", c.promptTokens)
	metric("counter", "prompt_tokens_cached_total", "Number of prompt tokens reused from the cache", c.cachedTokens)
	metric("counter", "prompt_seconds_total", "Total time spent processing prompts", c.promptSeconds)
	metric("counter", "tokens_predicted_total", "Number of generation tokens processed", c.predicted)
	metric("counter", "tokens_predicted_seconds_total", "Total time spent generating tokens", c.predictedSeconds)
	metric("counter", "n_decode_total", "Total number of llama_decode() calls", c.decodes)
	metric("counter", "n_tokens_max", "Largest observed sequence length (prompt + generation)", c.maxTokens)
	metric("gauge", "prompt_tokens_seconds", "Average prompt throughput in tokens/s", rate(c.promptTokens, c.promptSeconds))
	metric("gauge", "predicted_tokens_seconds", "Average generation throughput in tokens/s", rate(c.predicted, c.predictedSeconds))
	metric("gauge", "requests_processing", "Number of requests processing", float64(busy))
	metric("gauge", "requests_deferred", "Number of requests deferred", 0)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = io.WriteString(w, b.String())
}

func (s *Server) handleProps(w http.ResponseWriter, r *http.Request) {
	m := s.loadedFromQuery(w, r)
	if m == nil {
		return
	}
	var props map[string]any
	_ = json.Unmarshal(propsJSON, &props)
	props["model_alias"], props["total_slots"] = m.id, m.slotCount()
	writeJSON(w, 200, props)
}

// Listen binds the configured address; port 0 picks a free one.
func (s *Server) Listen() (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort(s.opts.Host, strconv.Itoa(s.opts.Port)))
}
