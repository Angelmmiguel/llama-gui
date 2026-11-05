package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/llama"
)

const (
	maxGaps     = 120
	maxJSONBody = 8 << 20 // past this a non-stream response just has no timings
	maxSSELine  = 1 << 20
)

// RequestRecord is the measurement of one finished request. Timings are zero when
// upstream did not report them; Slot and Task are nil when unknown.
type RequestRecord struct {
	ID          uint64
	StartedAt   time.Time
	Method      string
	Endpoint    string
	Model       string
	Status      int
	StatusClass string // the status, plus a kind when known: "200", "400 ctx", "503 busy"
	Stream      bool
	TTFTMs      float64
	TotalMs     float64
	Gaps        []float64 // first 120 inter-token gaps, in ms
	Timings     llama.Timings
	Slot        *int
	Task        *int64
}

// meter watches a response as it passes.
type meter struct {
	started time.Time
	now     func() time.Time

	entry       *entry
	onFirstByte func()
	model       string
	stream      bool // requested, or answered as SSE

	status   int
	class    string // decided early by whoever answered, or by settle
	sse      bool
	sawBytes bool
	sawToken bool
	eof      bool
	readErr  error

	firstByte, firstToken, lastToken time.Time
	gaps                             []float64

	timings  llama.Timings
	midError string // an error sent inside a 200 stream

	line     []byte
	lineOver bool
	buf      []byte // non-stream body, for timings and error classification
	bufOver  bool

	capture     bool
	maxCapture  int
	reqCapture  []byte
	respCapture []byte
}

// answered records an answer the proxy gave itself.
func (m *meter) answered(status int, class string) {
	m.status, m.class = status, class
}

func (m *meter) begin(status int, sse bool) {
	m.status, m.sse = status, sse
	m.stream = m.stream || sse
}

// meteredBody feeds what ReverseProxy reads through the meter on its way to the client.
type meteredBody struct {
	io.ReadCloser
	m *meter
}

func (b *meteredBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.m.observe(p[:n], b.m.now())
	}
	switch {
	case errors.Is(err, io.EOF):
		b.m.finishLine(b.m.now())
		b.m.eof = true
	case err != nil:
		b.m.readErr = err
	}
	return n, err
}

func (m *meter) observe(b []byte, now time.Time) {
	if !m.sawBytes {
		m.sawBytes, m.firstByte = true, now
		if m.onFirstByte != nil {
			m.onFirstByte()
		}
	}
	if m.capture && len(m.respCapture) < m.maxCapture {
		m.respCapture = append(m.respCapture, b[:min(len(b), m.maxCapture-len(m.respCapture))]...)
	}
	if !m.sse {
		if !m.bufOver {
			if len(m.buf)+len(b) > maxJSONBody {
				m.bufOver, m.buf = true, nil
			} else {
				m.buf = append(m.buf, b...)
			}
		}
		return
	}
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			m.appendLine(b)
			return
		}
		m.appendLine(b[:i])
		m.finishLine(now)
		b = b[i+1:]
	}
}

func (m *meter) appendLine(b []byte) {
	if m.lineOver {
		return
	}
	if len(m.line)+len(b) > maxSSELine {
		m.lineOver, m.line = true, m.line[:0]
		return
	}
	m.line = append(m.line, b...)
}

func (m *meter) finishLine(now time.Time) {
	line := m.line
	over := m.lineOver
	m.line, m.lineOver = m.line[:0], false
	if !over && m.sse {
		m.scanEvent(bytes.TrimSpace(line), now)
	}
}

func (m *meter) scanEvent(line []byte, now time.Time) {
	var payload []byte
	if rest, ok := bytes.CutPrefix(line, []byte("data:")); ok {
		payload = bytes.TrimSpace(rest)
	} else if rest, ok := bytes.CutPrefix(line, []byte("error:")); ok {
		m.midError = classify(m.status, bytes.TrimSpace(rest))
		return
	} else {
		return
	}
	if len(payload) == 0 || payload[0] != '{' {
		return // includes [DONE]
	}
	if t, ok := llama.ScanSSELine(line); ok {
		m.timings = t
	}
	var ev sseEvent
	_ = json.Unmarshal(payload, &ev) // keep what parsed: shapes vary by endpoint and build
	if len(ev.Error) > 0 && string(ev.Error) != "null" {
		m.midError = classify(m.status, payload)
	}
	if !ev.hasToken() {
		return
	}
	if !m.sawToken {
		m.sawToken, m.firstToken = true, now
	} else if len(m.gaps) < maxGaps {
		m.gaps = append(m.gaps, ms(now.Sub(m.lastToken)))
	}
	m.lastToken = now
}

// sseEvent covers the streaming shapes: OpenAI chat and completions in choices,
// the native endpoints in content, and Anthropic and Responses in a top-level delta.
type sseEvent struct {
	Content string          `json:"content"`
	Delta   json.RawMessage `json:"delta"`
	Error   json.RawMessage `json:"error"`
	Choices []struct {
		Text  string          `json:"text"`
		Delta json.RawMessage `json:"delta"`
	} `json:"choices"`
}

func (e *sseEvent) hasToken() bool {
	if e.Content != "" || deltaHasToken(e.Delta) {
		return true
	}
	for _, c := range e.Choices {
		if c.Text != "" || deltaHasToken(c.Delta) {
			return true
		}
	}
	return false
}

func deltaHasToken(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	if raw[0] == '"' {
		return len(raw) > 2
	}
	var d struct {
		Content          string            `json:"content"`
		ReasoningContent string            `json:"reasoning_content"`
		Text             string            `json:"text"`
		Thinking         string            `json:"thinking"`
		PartialJSON      string            `json:"partial_json"`
		ToolCalls        []json.RawMessage `json:"tool_calls"`
	}
	_ = json.Unmarshal(raw, &d)
	return d.Content != "" || d.ReasoningContent != "" || d.Text != "" || d.Thinking != "" || d.PartialJSON != "" || len(d.ToolCalls) > 0
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// settle decides how the request ended. cancelled says its context is done. A body
// that stopped short without a read error means writing to the client failed, which
// can show up before the context does.
func (m *meter) settle(cancelled bool) {
	switch {
	case m.class != "":
	case m.status == 0:
		m.status, m.class = statusCancelled, fmt.Sprintf("%d cancelled", statusCancelled)
	case !m.eof && (cancelled || m.readErr == nil):
		m.class = fmt.Sprintf("%d cancelled", m.status)
	case !m.eof:
		m.class = fmt.Sprintf("%d aborted", m.status)
	case m.midError != "":
		m.class = m.midError
	case m.status >= 400:
		m.class = classify(m.status, m.buf)
	default:
		m.class = strconv.Itoa(m.status)
	}
	if !m.sse && !m.bufOver {
		if t, ok := llama.ParseTimings(m.buf); ok {
			m.timings = t
		}
	}
}

func (m *meter) result(end time.Time) RequestRecord {
	rec := RequestRecord{
		StartedAt: m.started, Model: m.model, Status: m.status, StatusClass: m.class, Stream: m.stream,
		TotalMs: ms(end.Sub(m.started)), Gaps: m.gaps, Timings: m.timings,
	}
	switch {
	case m.sawToken:
		rec.TTFTMs = ms(m.firstToken.Sub(m.started))
	case m.sawBytes:
		rec.TTFTMs = ms(m.firstByte.Sub(m.started))
	}
	return rec
}

// classify names an upstream error: the status, plus what went wrong when
// recognisable. It keeps only that kind, never the message, which can quote input.
func classify(status int, body []byte) string {
	var e struct {
		Error   json.RawMessage `json:"error"`
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Type    string          `json:"type"`
	}
	_ = json.Unmarshal(body, &e)
	if len(e.Error) > 0 {
		if e.Error[0] == '"' {
			_ = json.Unmarshal(e.Error, &e.Message)
		} else {
			_ = json.Unmarshal(e.Error, &e)
		}
	}
	msg := strings.ToLower(e.Message)
	if msg == "" {
		msg = strings.ToLower(string(body[:min(len(body), 512)]))
	}
	if status < 400 && e.Code >= 400 {
		status = e.Code // an error sent inside a 200 stream
	}
	kind := ""
	switch {
	case e.Type == "exceed_context_size_error", strings.Contains(msg, "context size"), strings.Contains(msg, "context length"):
		kind = "ctx"
	case status == 503 || e.Type == "unavailable_error":
		kind = "busy"
		if strings.Contains(msg, "loading") {
			kind = "loading"
		}
	case strings.Contains(msg, "not loaded"):
		kind = "not loaded"
	case strings.Contains(msg, "model") && strings.Contains(msg, "not found"):
		kind = "unknown model"
	}
	if kind == "" {
		return strconv.Itoa(status)
	}
	return fmt.Sprintf("%d %s", status, kind)
}
