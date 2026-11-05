package fakellama

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	kindChat       = "chat"
	kindCompletion = "completion"
	kindInfill     = "infill"

	fingerprint = "b12132-2ada83079"
)

var vocab = []string{" Hello", " from", " the", " fake", " llama", " server", ",", " nice", " to", " meet", " you", "!"}

// words collects the prompt text of any of the supported request shapes.
func words(body map[string]any) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			out = append(out, strings.Fields(t)...)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			walk(t["text"])
		}
	}
	if msgs, ok := body["messages"].([]any); ok {
		for _, m := range msgs {
			if mm, ok := m.(map[string]any); ok {
				walk(mm["content"])
			}
		}
	}
	for _, k := range []string{"prompt", "input", "input_prefix", "input_suffix"} {
		walk(body[k])
	}
	return out
}

func commonPrefix(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func randID(prefix string) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 32)
	for i := range b {
		b[i] = chars[rand.IntN(len(chars))]
	}
	return prefix + string(b)
}

// acquire waits for an idle slot, preferring the one that shares the longest prompt prefix.
func (s *Server) acquire(r *http.Request, m *model, prompt []string) (sl *slot, cached int, byLCP bool, err error) {
	for {
		s.mu.Lock()
		best := -1
		for _, c := range m.slots {
			if c.processing {
				continue
			}
			if n := commonPrefix(c.prev, prompt); best < 0 || n > cached {
				sl, best, cached = c, n, n
			}
		}
		if sl != nil {
			sl.processing, sl.hasTask = true, true
			sl.task = int(s.task.Add(1) - 1)
			s.mu.Unlock()
			return sl, cached, cached > 0, nil
		}
		s.mu.Unlock()
		select {
		case <-r.Context().Done():
			return nil, 0, false, r.Context().Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request, kind string) {
	body := readJSON(r)
	if body == nil {
		writeErr(w, 400, "invalid_request_error", "Invalid JSON body")
		return
	}
	id := str(body["model"])
	m := s.pick(id)
	if m == nil {
		s.unknownModel(w, id)
		return
	}
	if err := s.ensureLoaded(r.Context(), m); err != nil {
		if err == errNotLoaded {
			s.notLoaded(w, m)
		}
		return
	}
	s.mu.Lock()
	port, started := m.port, m.started
	s.mu.Unlock()
	s.log.router("info", srvMsg("proxy_request", fmt.Sprintf("proxying request to model %s on port %d", m.id, port)))

	prompt := words(body)
	if len(prompt) > s.opts.Ctx {
		msg := fmt.Sprintf("request (%d tokens) exceeds the available context size (%d tokens), try increasing it", len(prompt), s.opts.Ctx)
		s.log.child(port, started, "error", srvMsg("send_error", "task id = -1, error: "+msg))
		writeJSON(w, 400, map[string]any{"error": map[string]any{"code": 400, "message": msg, "type": "exceed_context_size_error", "n_prompt_tokens": len(prompt), "n_ctx": s.opts.Ctx}})
		return
	}
	n := s.opts.Tokens
	for _, k := range []string{"max_tokens", "n_predict"} {
		if v, ok := body[k].(float64); ok && v > 0 {
			n = int(v)
		}
	}
	stream, _ := body["stream"].(bool)
	if kind == kindInfill {
		stream = false
	}
	opts, _ := body["stream_options"].(map[string]any)
	usage, _ := opts["include_usage"].(bool)

	sl, cached, byLCP, err := s.acquire(r, m, prompt)
	if err != nil {
		return
	}
	g := &gen{s: s, m: m, sl: sl, w: w, r: r, kind: kind, id: randID("chatcmpl-"), created: time.Now().Unix(),
		model: m.id, prompt: prompt, cached: cached, n: n, stream: stream, usage: usage, port: port, started: started}
	g.run(byLCP, body)
}

type gen struct {
	s       *Server
	m       *model
	sl      *slot
	w       http.ResponseWriter
	r       *http.Request
	kind    string
	id      string
	created int64
	model   string
	prompt  []string
	cached  int
	n       int
	stream  bool
	usage   bool
	port    int
	started time.Time
}

func (g *gen) slotLog(level, name, text string) {
	g.s.log.child(g.port, g.started, level, slotMsg(name, g.sl.id, g.sl.task, text))
}

func (g *gen) run(byLCP bool, body map[string]any) {
	s, sl := g.s, g.sl
	processed := len(g.prompt) - g.cached
	s.mu.Lock()
	sl.nCached, sl.nProcessed, sl.nDecod, sl.nPrompt = g.cached, processed, 0, len(g.prompt)
	sl.params = map[string]any{"seed": 4294967295, "temperature": 0.8, "top_k": 40, "top_p": 0.95, "min_p": 0.05,
		"max_tokens": g.n, "n_predict": g.n, "stream": g.stream, "samplers": []string{"top_k", "top_p", "min_p", "temperature"}}
	task := sl.task
	s.mu.Unlock()

	sel := "selected slot by LRU, n_tokens = 0, t_last = -1"
	if byLCP {
		sel = fmt.Sprintf("selected slot by LCP similarity, f_sim_best = %.3f (> 0.100 thold), f_keep = %.3f", float64(g.cached)/float64(len(g.prompt)), float64(g.cached)/float64(len(g.prompt)))
	}
	s.log.child(g.port, g.started, "info", fmt.Sprintf("slot %s: id %2d | task -1 | %s", fn("get_availabl"), sl.id, sel))
	g.slotLog("info", "launch_slot_", "processing task, is_child = 0")

	t0 := time.Now()
	time.Sleep(time.Duration(processed) * 50 * time.Microsecond)
	promptMS := float64(time.Since(t0).Microseconds()) / 1000

	if g.stream {
		g.w.Header().Set("Content-Type", "text/event-stream")
		g.w.Header().Set("Cache-Control", "no-cache")
		g.w.WriteHeader(200)
		if g.kind == kindChat {
			g.chunk(map[string]any{"role": "assistant", "content": nil}, nil)
		}
	}

	var text strings.Builder
	t1 := time.Now()
	for i := 0; i < g.n; i++ {
		select {
		case <-g.r.Context().Done():
			g.cancelled(i)
			return
		case <-time.After(s.opts.TokenDelay):
		}
		tok := vocab[i%len(vocab)]
		text.WriteString(tok)
		s.mu.Lock()
		sl.nDecod = i + 1
		sl.nPrompt = len(g.prompt) + i + 1
		g.m.counters.predicted++
		g.m.counters.decodes++
		g.m.counters.predictedSeconds += s.opts.TokenDelay.Seconds()
		s.mu.Unlock()
		if g.stream {
			if !g.chunk(map[string]any{"content": tok}, nil) {
				g.cancelled(i + 1)
				return
			}
		}
	}
	predictedMS := float64(time.Since(t1).Microseconds()) / 1000
	total := len(g.prompt) + g.n

	s.mu.Lock()
	sl.processing, sl.nPrompt, sl.nProcessed, sl.nCached, sl.nDecod = false, total, 0, 0, 0
	sl.prev = g.prompt
	c := &g.m.counters
	c.promptTokens += float64(processed)
	c.cachedTokens += float64(g.cached)
	c.promptSeconds += promptMS / 1000
	c.maxTokens = max(c.maxTokens, float64(total))
	s.mu.Unlock()

	timings := timingsJSON(g.cached, processed, promptMS, g.n, predictedMS)
	g.logTimings(task, processed, promptMS, g.n, predictedMS)
	g.slotLog("info", "release", fmt.Sprintf("stop processing: n_tokens = %d, truncated = 0", total))
	g.finish(text.String(), total, timings)
}

func timingsJSON(cacheN, promptN int, promptMS float64, predN int, predMS float64) map[string]any {
	per := func(n int, ms float64) (float64, float64) {
		if n == 0 || ms == 0 {
			return 0, 0
		}
		return ms / float64(n), float64(n) / ms * 1000
	}
	ppt, pps := per(promptN, promptMS)
	gpt, gps := per(predN, predMS)
	return map[string]any{"cache_n": cacheN, "prompt_n": promptN, "prompt_ms": promptMS, "prompt_per_token_ms": ppt,
		"prompt_per_second": pps, "predicted_n": predN, "predicted_ms": predMS, "predicted_per_token_ms": gpt, "predicted_per_second": gps}
}

func (g *gen) logTimings(task, promptN int, promptMS float64, predN int, predMS float64) {
	line := func(label string, ms float64, n int) {
		var per, ps float64
		if n > 0 && ms > 0 {
			per, ps = ms/float64(n), float64(n)/ms*1000
		}
		g.slotLog("info", "print_timing", fmt.Sprintf("%s time = %10.2f ms / %5d tokens (%8.2f ms per token, %8.2f tokens per second)", label, ms, n, per, ps))
	}
	line("prompt eval", promptMS, promptN)
	line("       eval", predMS, predN)
	g.slotLog("info", "print_timing", fmt.Sprintf("      total time = %10.2f ms / %5d tokens", promptMS+predMS, promptN+predN))
	g.slotLog("info", "print_timing", fmt.Sprintf("   graphs reused = %10d", predN))
	g.slotLog("info", "print_timing", fmt.Sprintf("  graph reallocs = %10d    / %5d        (same size) / (changed size)", 0, 0))
}

func (g *gen) cancelled(decoded int) {
	s, sl := g.s, g.sl
	s.mu.Lock()
	total := len(g.prompt) + decoded
	sl.processing, sl.nPrompt, sl.nProcessed, sl.nCached, sl.nDecod = false, total, 0, 0, 0
	sl.prev = g.prompt
	task := sl.task
	s.mu.Unlock()
	s.log.router("error", srvMsg("operator()", "http client error: Failed to read connection"))
	g.s.log.child(g.port, g.started, "warn", srvMsg("stop", fmt.Sprintf("cancel task, id_task = %d", task)))
	g.slotLog("info", "release", fmt.Sprintf("stop processing: n_tokens = %d, truncated = 0", total))
}

func (g *gen) envelope() map[string]any {
	obj := "chat.completion"
	switch {
	case g.kind == kindCompletion:
		obj = "text_completion"
	case g.stream:
		obj += ".chunk"
	}
	return map[string]any{"created": g.created, "id": g.id, "model": g.model, "system_fingerprint": fingerprint, "object": obj}
}

// chunk writes one SSE event; it reports false when the client is gone.
func (g *gen) chunk(delta map[string]any, finish any) bool {
	env := g.envelope()
	choice := map[string]any{"finish_reason": finish, "index": 0}
	if g.kind == kindCompletion {
		choice["text"] = delta["content"]
		if choice["text"] == nil {
			choice["text"] = ""
		}
	} else {
		choice["delta"] = delta
	}
	env["choices"] = []any{choice}
	return g.event(env)
}

func (g *gen) event(v any) bool {
	b, _ := json.Marshal(v)
	if _, err := fmt.Fprintf(g.w, "data: %s\n\n", b); err != nil {
		return false
	}
	g.w.(http.Flusher).Flush()
	return true
}

func (g *gen) finish(text string, total int, timings map[string]any) {
	usage := map[string]any{"completion_tokens": g.n, "prompt_tokens": len(g.prompt), "total_tokens": total,
		"prompt_tokens_details": map[string]any{"cached_tokens": g.cached}}
	if g.stream {
		env := g.envelope()
		choice := map[string]any{"finish_reason": "length", "index": 0}
		if g.kind == kindCompletion {
			choice["text"] = ""
		} else {
			choice["delta"] = map[string]any{}
		}
		env["choices"], env["timings"] = []any{choice}, timings
		g.event(env)
		if g.usage {
			u := g.envelope()
			u["choices"], u["usage"] = []any{}, usage
			g.event(u)
		}
		_, _ = fmt.Fprint(g.w, "data: [DONE]\n\n")
		g.w.(http.Flusher).Flush()
		return
	}
	switch g.kind {
	case kindInfill:
		writeJSON(g.w, 200, map[string]any{"content": text, "model": g.model, "stop": true, "tokens_predicted": g.n,
			"tokens_evaluated": len(g.prompt), "timings": timings})
		return
	case kindCompletion:
		env := g.envelope()
		env["choices"] = []any{map[string]any{"finish_reason": "length", "index": 0, "text": text}}
		env["usage"], env["timings"] = usage, timings
		writeJSON(g.w, 200, env)
	default:
		env := g.envelope()
		env["choices"] = []any{map[string]any{"finish_reason": "length", "index": 0, "message": map[string]any{"role": "assistant", "content": text}}}
		env["usage"], env["timings"] = usage, timings
		writeJSON(g.w, 200, env)
	}
}

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	body := readJSON(r)
	id := str(body["model"])
	m := s.pick(id)
	if m == nil {
		s.unknownModel(w, id)
		return
	}
	if err := s.ensureLoaded(r.Context(), m); err != nil {
		if err == errNotLoaded {
			s.notLoaded(w, m)
		}
		return
	}
	n := len(words(body))
	vec := make([]float64, 8)
	for i := range vec {
		vec[i] = float64(i+1) / 10
	}
	writeJSON(w, 200, map[string]any{"object": "list", "model": m.id,
		"data":  []any{map[string]any{"object": "embedding", "index": 0, "embedding": vec}},
		"usage": map[string]any{"prompt_tokens": n, "total_tokens": n}})
}

// slotFile implements save and restore against --slot-save-path.
func (s *Server) slotFile(w http.ResponseWriter, action string, id int, filename, path string, sl *slot) {
	start := time.Now()
	if action == "save" {
		s.mu.Lock()
		n := sl.nPrompt
		s.mu.Unlock()
		data := []byte(fmt.Sprintf("fake slot state: %d tokens\n", n))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			writeErr(w, 500, "server_error", "failed to save slot")
			return
		}
		writeJSON(w, 200, map[string]any{"id_slot": id, "filename": filename, "n_saved": n, "n_written": len(data),
			"timings": map[string]any{"save_ms": float64(time.Since(start).Microseconds()) / 1000}})
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, 400, "invalid_request_error", "Unable to restore slot, no available space in KV cache or invalid slot save file")
		return
	}
	var n int
	_, _ = fmt.Sscanf(string(data), "fake slot state: %d tokens", &n)
	s.mu.Lock()
	sl.nPrompt = n
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"id_slot": id, "filename": filename, "n_restored": n, "n_read": len(data),
		"timings": map[string]any{"restore_ms": float64(time.Since(start).Microseconds()) / 1000}})
}
