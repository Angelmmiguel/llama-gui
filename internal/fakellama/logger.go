package fakellama

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// logger writes the router's output format: JSONL records, with child output
// forwarded as level "none" and a "[port] " prefix wrapping the child's own JSONL.
type logger struct {
	mu    sync.Mutex
	w     io.Writer
	start time.Time
	jsonl bool
}

type record struct {
	Type  string `json:"type"`
	Time  int64  `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

func marshal(r record) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(r)
	return string(bytes.TrimRight(b.Bytes(), "\n"))
}

// router logs a line from the router process itself.
func (l *logger) router(level, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.jsonl {
		fmt.Fprintln(l.w, msg)
		return
	}
	fmt.Fprintln(l.w, marshal(record{"log", time.Since(l.start).Microseconds(), level, msg + "\n"}))
}

// child logs a line a child process wrote; childStart is when that child began.
func (l *logger) child(port int, childStart time.Time, level, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.jsonl {
		fmt.Fprintf(l.w, "[%d] %s\n", port, msg)
		return
	}
	inner := marshal(record{"log", time.Since(childStart).Microseconds(), level, msg + "\n"})
	fmt.Fprintln(l.w, marshal(record{"log", time.Since(l.start).Microseconds(), "none", fmt.Sprintf("[%d] %s\n", port, inner)}))
}

func fn(name string) string {
	if len(name) > 12 {
		name = name[:12]
	}
	return fmt.Sprintf("%12s", name)
}

func srvMsg(name, text string) string { return "srv  " + fn(name) + ": " + text }

func slotMsg(name string, id, task int, text string) string {
	return fmt.Sprintf("slot %s: id %2d | task %d | %s", fn(name), id, task, text)
}
